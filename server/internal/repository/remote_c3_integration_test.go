package repository_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/handler"
	"github.com/JustinBeaudry/kb/server/internal/repository"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Count actual object reads while retaining the real S3 gateway. This lets the
// denied-path checks prove authorization occurs before source I/O.
type c3CountingObjects struct {
	service.ObjectStore
	opens     atomic.Int64
	mu        sync.Mutex
	blockNext bool
	started   chan struct{}
	release   chan struct{}
}

func (o *c3CountingObjects) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	o.opens.Add(1)
	o.mu.Lock()
	block, started, release := o.blockNext, o.started, o.release
	o.blockNext = false
	o.mu.Unlock()
	if block {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return o.ObjectStore.Open(ctx, key)
}

func (o *c3CountingObjects) blockOneOpen() (<-chan struct{}, chan<- struct{}) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.started, o.release = make(chan struct{}), make(chan struct{})
	o.blockNext = true
	return o.started, o.release
}

type c3Envelope struct {
	SchemaVersion string `json:"schema_version"`
	Chunks        []struct {
		RecordID    string   `json:"record_id"`
		Revision    string   `json:"revision"`
		Text        string   `json:"text"`
		ClaimStatus string   `json:"claim_status"`
		OriginTrust string   `json:"origin_trust"`
		Warnings    []string `json:"warnings"`
		Passage     struct {
			Start       int `json:"start_byte"`
			End         int `json:"end_byte"`
			SourceBytes int `json:"source_bytes"`
		} `json:"passage"`
		Evidence []struct {
			ArtifactID     string `json:"artifact_id"`
			Digest         string `json:"digest"`
			Locator        string `json:"locator"`
			SourceRevision string `json:"source_revision"`
		} `json:"evidence"`
	} `json:"chunks"`
	Continuation string `json:"continuation"`
	Truncated    bool   `json:"truncated"`
}

type c3RPC struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	} `json:"result"`
	Error json.RawMessage `json:"error"`
}

func keysC3(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	return keys
}

// c3Session keeps one executable and its initialized JSON-RPC connection alive
// across discovery, calls, continuations and, for the agent, revocation.
type c3Session struct {
	stdin  io.WriteCloser
	stdout *bufio.Reader
	nextID int
}

func newC3Session(t *testing.T, bin, endpoint, bearer string) *c3Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	read, write, err := os.Pipe()
	require.NoError(t, err)
	_, err = io.WriteString(write, bearer)
	require.NoError(t, err)
	require.NoError(t, write.Close())
	cmd := exec.CommandContext(ctx, bin, "--endpoint", endpoint, "--credential-fd", "3")
	cmd.ExtraFiles = []*os.File{read}
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Start())
	require.NoError(t, read.Close())
	session := &c3Session{stdin: stdin, stdout: bufio.NewReader(stdout)}
	t.Cleanup(func() {
		defer cancel()
		assert.NoError(t, stdin.Close())
		// Includes a check that notifications never emitted an extra frame.
		extra, readErr := io.ReadAll(session.stdout)
		assert.NoError(t, readErr)
		assert.Empty(t, extra, "unexpected extra MCP frames")
		assert.NoError(t, cmd.Wait(), "MCP executable diagnostics: %s", stderr.String())
		assert.Empty(t, stderr.String(), "MCP session must not emit diagnostics or evidence")
	})
	_, init := session.call(t, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "c3-integration", "version": "1"}})
	require.Equal(t, "2025-06-18", init.Result.ProtocolVersion)
	require.Equal(t, "kb-mcp", init.Result.ServerInfo.Name)
	session.notify(t, "notifications/initialized")
	_, list := session.call(t, "tools/list", nil)
	var names []string
	for _, tool := range list.Result.Tools {
		names = append(names, tool.Name)
	}
	require.ElementsMatch(t, []string{"memory_orient", "memory_search", "memory_fetch", "memory_submit_candidate", "memory_checkpoint", "memory_status"}, names)
	return session
}

func (s *c3Session) notify(t *testing.T, method string) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	require.NoError(t, err)
	_, err = s.stdin.Write(append(data, '\n'))
	require.NoError(t, err)
}

func (s *c3Session) call(t *testing.T, method string, params any) ([]byte, c3RPC) {
	t.Helper()
	s.nextID++
	id := s.nextID
	data, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	require.NoError(t, err)
	_, err = s.stdin.Write(append(data, '\n'))
	require.NoError(t, err)
	wire, err := s.stdout.ReadBytes('\n')
	require.NoError(t, err, "read MCP reply: %s", wire)
	var reply c3RPC
	require.NoError(t, json.Unmarshal(bytes.TrimSuffix(wire, []byte("\n")), &reply), string(wire))
	require.Equal(t, "2.0", reply.JSONRPC, string(wire))
	require.JSONEq(t, strconv.Itoa(id), string(reply.ID), "JSON-RPC response must correlate to the request")
	require.Empty(t, reply.Error, string(wire))
	return wire, reply
}

func (s *c3Session) fetch(t *testing.T, args map[string]any) ([]byte, c3RPC) {
	t.Helper()
	wire, reply := s.call(t, "tools/call", map[string]any{"name": "memory_fetch", "arguments": args})
	require.Len(t, reply.Result.Content, 1)
	require.Equal(t, "text", reply.Result.Content[0].Type)
	return wire, reply
}

// TestC3RealHTTPCLIAndMCP traverses the real graph, real local object store,
// real Go handler/identity/retrieval services, and both remote client stacks.
// No cloud resources, real credentials, or source files are involved. The CLI
// command uses an injected, in-memory credential store rather than OS keychain.
func TestC3RealHTTPCLIAndMCP(t *testing.T) {
	store, jobs, _ := jobsGraph(t)
	objects := captureS3(t)
	warmObjects(t, objects)
	evidence := repository.NewEvidenceRepository(store)
	identity := service.NewIdentityService(store, repository.NewIdentityRepository(store), gateway.NostrIdentity{})
	collector, err := identity.Setup(t.Context(), domain.OwnerIdentity{ID: "owner", PublicKey: "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"}, service.EnrollInput{ClientID: "collector", Role: domain.RoleCollector, ProjectIDs: []string{"demo"}, Capabilities: []string{"artifacts:write"}})
	require.NoError(t, err)
	owner := domain.Principal{OwnerID: "owner", Role: domain.RoleOwner}
	enroll := func(id string, caps ...string) service.ClientCredential {
		c, err := identity.Enroll(t.Context(), owner, service.EnrollInput{ClientID: id, Role: domain.RoleAgent, ProjectIDs: []string{"demo"}, Capabilities: caps})
		require.NoError(t, err)
		return c
	}
	agent := enroll("agent", "retrieval:read", "evidence:read", "candidate:write", "status:read")
	other := enroll("other", "retrieval:read", "evidence:read", "status:read")
	limited := enroll("limited", "retrieval:read", "status:read")
	uncitedOwner := enroll("uncited-owner", "retrieval:read", "evidence:read", "checkpoint:write", "candidate:write")
	// Default kb-admin agent capabilities; status:read lets MCP learn the bound.
	writer := enroll("mcp-writer", "status:read", "retrieval:read", "evidence:read", "candidate:write", "checkpoint:write")
	seedCapturePolicy(t, store, domain.Policy{Revision: "p1", SpoolLimitBytes: service.MaxArtifactBytes})
	capture := service.NewCaptureService(store, evidence, jobs, objects)
	retrievalRepo := repository.NewRetrievalRepository(store)
	checkpoints := repository.NewCheckpointRepository(store)
	counting := &c3CountingObjects{ObjectStore: objects}
	retrieval, err := service.NewRetrievalService(store, retrievalRepo, checkpoints, evidence, counting, bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	memory := service.NewMemoryService(store, repository.NewMemoryRepository(store), retrievalRepo, evidence, capture)
	cpService := service.NewCheckpointService(store, checkpoints, retrievalRepo)
	status := service.NewStatusService(store, retrievalRepo)
	serverHandler, err := handler.NewServer(identity, capture, cpService, memory, retrieval, service.NewPolicyService(store, evidence, retrievalRepo), status, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	require.NoError(t, err)
	server := httptest.NewServer(serverHandler)
	t.Cleanup(server.Close)

	// Escaping as well as multibyte characters stresses the final serialized
	// frame, not merely the passage text byte count.
	source := strings.Repeat("a猫\\\"<>&\n", 3000)
	require.Greater(t, len(source), 16*1024)
	scope := domain.Scope{ProjectID: "demo", TaskID: "task"}
	meta := service.ArtifactInput{Scope: scope, Digest: service.TokenDigest(source), Bytes: int64(len(source)), PolicyRevision: "p1", SourceLocator: fmt.Sprintf("synthetic.txt#bytes=0-%d", len(source)), SourcePath: "synthetic.txt"}
	artifact, err := capture.Stage(t.Context(), collector.Principal, meta, strings.NewReader(source))
	require.NoError(t, err)
	_, err = capture.Complete(t.Context(), collector.Principal, artifact.ID, artifact.Digest)
	require.NoError(t, err)
	_, err = memory.Submit(t.Context(), agent.Principal, domain.CandidateInput{ClaimID: "claim", Text: "Synthetic task finding", Kind: "finding", Scope: scope, Evidence: []domain.EvidenceRef{{ArtifactID: artifact.ID, Digest: artifact.Digest, Locator: artifact.SourceLocator, SourceRevision: artifact.Generation}}})
	require.NoError(t, err)

	// A separate client owns a different task via a real checkpoint. Another
	// client's synthetic candidate cites its ready artifact; the owner has no
	// citation. This isolates citation ownership from task ownership, scope,
	// capability, locator and object availability checks.
	uncitedScope := domain.Scope{ProjectID: "demo", TaskID: "uncited-task"}
	uncitedSource := strings.Repeat("UNCITED-猫-\\\"<>&\n", 200)
	uncitedMeta := service.ArtifactInput{Scope: uncitedScope, Digest: service.TokenDigest(uncitedSource), Bytes: int64(len(uncitedSource)), PolicyRevision: "p1", SourceLocator: fmt.Sprintf("uncited.txt#bytes=0-%d", len(uncitedSource)), SourcePath: "uncited.txt"}
	uncitedArtifact, err := capture.Stage(t.Context(), collector.Principal, uncitedMeta, strings.NewReader(uncitedSource))
	require.NoError(t, err)
	_, err = capture.Complete(t.Context(), collector.Principal, uncitedArtifact.ID, uncitedArtifact.Digest)
	require.NoError(t, err)
	_, err = cpService.Put(t.Context(), uncitedOwner.Principal, "", domain.Checkpoint{ID: "uncited-checkpoint", Scope: uncitedScope, Objective: "own task without citations"})
	require.NoError(t, err)
	var ownerSnapshot domain.RetrievalSnapshot
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		var e error
		ownerSnapshot, e = retrievalRepo.Snapshot(ctx, tx, uncitedOwner.Principal, uncitedScope)
		return e
	}))
	require.Equal(t, uncitedOwner.ClientID, ownerSnapshot.TaskOwner)
	// Normal submission correctly forbids another client's candidate in this
	// task, so seed a foreign citation directly to exercise the read-side gate
	// against legacy/imported graph data without changing production code.
	foreign := domain.ClaimRevision{ClaimID: "foreign-claim", Revision: "foreign-revision", Text: "foreign citation", Kind: "finding", Scope: uncitedScope, Status: domain.ClaimCandidate, SubmittedBy: other.ClientID, Evidence: []domain.EvidenceRef{{ArtifactID: uncitedArtifact.ID, Digest: uncitedArtifact.Digest, Locator: uncitedArtifact.SourceLocator, SourceRevision: uncitedArtifact.Generation, Origin: domain.Origin{ClientID: collector.ClientID, Trust: domain.TrustSubmitted}}}}
	foreignData, err := json.Marshal(foreign)
	require.NoError(t, err)
	memoryMutation(t, store, `MATCH (a:Artifact {id:$artifact}) CREATE (c:ClaimRevision {id:$id,claimID:$claim,data:$data,status:'candidate',valid:false,projectID:$project,taskID:$task,clientID:$client,text:$text,kind:$kind}) CREATE (c)-[:EVIDENCE {digest:$digest,locator:$locator}]->(a)`, map[string]any{"id": foreign.Revision, "claim": foreign.ClaimID, "data": string(foreignData), "project": uncitedScope.ProjectID, "task": uncitedScope.TaskID, "client": foreign.SubmittedBy, "text": foreign.Text, "kind": foreign.Kind, "artifact": uncitedArtifact.ID, "digest": uncitedArtifact.Digest, "locator": uncitedArtifact.SourceLocator})
	var ownerSeeds, foreignSeeds []domain.ClaimRevision
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		ref := foreign.Evidence[0]
		var e error
		ownerSeeds, _, e = retrievalRepo.CitationSeeds(ctx, tx, uncitedOwner.Principal, uncitedScope, domain.FetchWorking, ref, domain.MaxGraphVisited)
		if e != nil {
			return e
		}
		foreignSeeds, _, e = retrievalRepo.CitationSeeds(ctx, tx, other.Principal, uncitedScope, domain.FetchWorking, ref, domain.MaxGraphVisited)
		return e
	}))
	require.Empty(t, ownerSeeds)
	require.Len(t, foreignSeeds, 1)
	require.Equal(t, foreign.Revision, foreignSeeds[0].Revision)

	mcpBin := filepath.Join(t.TempDir(), "kb-mcp")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", mcpBin, "../../cmd/kb-mcp")
	buildOut, err := build.CombinedOutput()
	require.NoError(t, err, "build real MCP executable: %s", buildOut)

	requestArtifact := func(t *testing.T, bearer, id string, q url.Values) (int, []byte) {
		t.Helper()
		u := server.URL + "/v1/evidence/" + url.PathEscape(id) + "?" + q.Encode()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, u, nil)
		require.NoError(t, err)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, body
	}
	request := func(t *testing.T, bearer string, q url.Values) (int, []byte) {
		t.Helper()
		return requestArtifact(t, bearer, artifact.ID, q)
	}
	const budget = 8192
	workingQuery := func() url.Values {
		return url.Values{"locator": {artifact.SourceLocator}, "mode": {"working"}, "project_id": {"demo"}, "task_id": {"task"}, "budget_bytes": {strconv.Itoa(budget)}}
	}
	checkPage := func(t *testing.T, value c3Envelope, expectedStart int) int {
		t.Helper()
		require.Equal(t, "3", value.SchemaVersion)
		require.Len(t, value.Chunks, 1)
		c := value.Chunks[0]
		assert.Equal(t, artifact.ID, c.RecordID)
		assert.Equal(t, artifact.Digest, c.Revision)
		assert.Equal(t, "candidate", c.ClaimStatus)
		assert.Equal(t, "submitted", c.OriginTrust)
		assert.Contains(t, c.Warnings, "unverified_task_evidence")
		assert.Contains(t, c.Warnings, "source_evidence_is_not_a_verified_claim")
		assert.Contains(t, c.Warnings, "partial_source_passage")
		require.Len(t, c.Evidence, 1)
		assert.Equal(t, artifact.ID, c.Evidence[0].ArtifactID)
		assert.Equal(t, artifact.Digest, c.Evidence[0].Digest)
		assert.Equal(t, artifact.Generation, c.Evidence[0].SourceRevision)
		assert.Equal(t, artifact.SourceLocator, c.Evidence[0].Locator)
		assert.Equal(t, len(source), c.Passage.SourceBytes)
		assert.Equal(t, expectedStart, c.Passage.Start)
		require.Greater(t, c.Passage.End, c.Passage.Start)
		require.LessOrEqual(t, c.Passage.End, len(source))
		assert.True(t, utf8.ValidString(c.Text))
		assert.Equal(t, source[c.Passage.Start:c.Passage.End], c.Text)
		return c.Passage.End
	}
	decode := func(t *testing.T, data []byte) c3Envelope {
		t.Helper()
		var envelope c3Envelope
		require.NoError(t, json.Unmarshal(data, &envelope), string(data))
		return envelope
	}
	cli := func(t *testing.T, bearer string, register bool, args ...string) ([]byte, []byte, error) {
		t.Helper()
		payload, err := json.Marshal(map[string]any{"origin": server.URL, "token": bearer, "register": register, "args": args})
		require.NoError(t, err)
		cmd := exec.CommandContext(t.Context(), "bun", "../../../tests/fixtures/remote-c3-integration.ts")
		cmd.Stdin = bytes.NewReader(payload)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err = cmd.Run()
		return stdout.Bytes(), stderr.Bytes(), err
	}
	cliArgs := []string{"fetch", "--id", artifact.ID, "--locator", artifact.SourceLocator, "--mode", "working", "--project", "demo", "--task", "task", "--budget", strconv.Itoa(budget)}
	cliPage := func(t *testing.T, data []byte) c3Envelope {
		t.Helper()
		require.LessOrEqual(t, len(data), budget)
		parts := bytes.SplitN(data, []byte("\n"), 2)
		require.Len(t, parts, 2)
		n, err := strconv.Atoi(string(parts[0]))
		require.NoError(t, err)
		require.Equal(t, len(parts[1]), n, "decimal prefix counts UTF-8 body bytes")
		return decode(t, parts[1])
	}
	// All calls for each bearer share an initialized session and monotonic IDs.
	agentMCP := newC3Session(t, mcpBin, server.URL, agent.Bearer)
	otherMCP := newC3Session(t, mcpBin, server.URL, other.Bearer)
	uncitedMCP := newC3Session(t, mcpBin, server.URL, uncitedOwner.Bearer)
	writerMCP := newC3Session(t, mcpBin, server.URL, writer.Bearer)
	mcp := func(t *testing.T, bearer string, args map[string]any) ([]byte, c3RPC) {
		t.Helper()
		switch bearer {
		case agent.Bearer:
			return agentMCP.fetch(t, args)
		case other.Bearer:
			return otherMCP.fetch(t, args)
		case uncitedOwner.Bearer:
			return uncitedMCP.fetch(t, args)
		default:
			t.Fatal("unexpected test bearer")
			return nil, c3RPC{}
		}
	}
	mcpArgs := func() map[string]any {
		return map[string]any{"artifact_id": artifact.ID, "locator": artifact.SourceLocator, "mode": "working", "project_id": "demo", "task_id": "task", "budget_bytes": budget}
	}

	// Verify the whole decoded error schema and every permitted value, rather
	// than searching for one substring of evidence. Unknown fields, chunks,
	// source fragments, or extra content cannot hide in any error response.
	errorObject := func(t *testing.T, data []byte) map[string]any {
		t.Helper()
		var value map[string]any
		require.NoError(t, json.Unmarshal(data, &value), string(data))
		require.NotNil(t, value)
		return value
	}
	httpError := func(t *testing.T, body []byte, status int, code string) {
		t.Helper()
		messages := map[string]string{"unauthorized": "Credential unavailable.", "forbidden": "Operation not authorized.", "budget": "Output exceeds the requested budget.", "stale": "Results changed."}
		actions := map[string]string{"unauthorized": "Reconnect this profile through owner enrollment.", "forbidden": "Use an authorized project and capability.", "budget": "Rerun with a larger budget within the documented limit.", "stale": "Rerun the original scoped retrieval without its continuation."}
		value := errorObject(t, body)
		id, ok := value["request_id"].(string)
		require.True(t, ok)
		require.Regexp(t, `^[A-Z2-7]{26,}$`, id, "request ID must be an opaque random value")
		expected := handler.Problem{SchemaVersion: "3", Type: "about:blank", Title: http.StatusText(status), Status: status, Code: handler.ProblemCode(code), Message: messages[code], Action: actions[code], RequestId: handler.OpaqueID(id), Runbook: "memory-operations#identity"}
		expectedWire, err := json.Marshal(expected)
		require.NoError(t, err)
		assert.Equal(t, string(expectedWire)+"\n", string(body), "HTTP error is exactly the safe problem, including JSON framing")
	}
	cliError := func(t *testing.T, out, stderr []byte, err error, code string) {
		t.Helper()
		assert.Error(t, err)
		assert.Empty(t, out)
		actions := map[string]string{"credential_unavailable": "Register this profile and origin using credential-set with the bearer on stdin.", "unauthorized": "Credential rejected or revoked; enroll and register a current client credential.", "forbidden": "This client lacks the requested scope or capability.", "budget": "The complete output cannot fit; increase the byte budget within the command limit.", "stale": "Repeat the original request without its continuation."}
		expected, marshalErr := json.Marshal(struct {
			Error struct {
				Code   string `json:"code"`
				Action string `json:"action"`
			} `json:"error"`
		}{Error: struct {
			Code   string `json:"code"`
			Action string `json:"action"`
		}{Code: code, Action: actions[code]}})
		require.NoError(t, marshalErr)
		assert.Equal(t, string(expected)+"\n", string(stderr), "CLI stderr is exactly the safe error; stdout is empty")
	}
	mcpError := func(t *testing.T, wire []byte, reply c3RPC, code string) {
		t.Helper()
		assert.True(t, reply.Result.IsError, string(wire))
		outer := errorObject(t, wire)
		assert.ElementsMatch(t, []string{"jsonrpc", "id", "result"}, keysC3(outer))
		result, ok := outer["result"].(map[string]any)
		require.True(t, ok)
		assert.ElementsMatch(t, []string{"content", "isError"}, keysC3(result))
		content, ok := result["content"].([]any)
		require.True(t, ok)
		require.Len(t, content, 1)
		item, ok := content[0].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, map[string]any{"type": "text", "text": reply.Result.Content[0].Text}, item)
		actions := map[string]string{"unauthorized": "Reconnect this profile through owner enrollment.", "forbidden": "Use an authorized project and capability.", "budget": "Rerun with a larger budget within the documented limit.", "stale": "Rerun the original scoped retrieval without its continuation."}
		payload := errorObject(t, []byte(reply.Result.Content[0].Text))
		detail := map[string]any{"code": code}
		if code != "budget" {
			detail["action"] = actions[code]
		}
		assert.Equal(t, map[string]any{"schema_version": "3", "error": detail}, payload)
		expectedPayload, err := json.Marshal(map[string]any{"schema_version": "3", "error": detail})
		require.NoError(t, err)
		// Production MCP disables HTML escaping at both JSON encoding layers.
		var outerWire bytes.Buffer
		encoder := json.NewEncoder(&outerWire)
		encoder.SetEscapeHTML(false)
		expectedFrame := struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Result  struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
				IsError bool `json:"isError"`
			} `json:"result"`
		}{JSONRPC: "2.0", ID: reply.ID}
		expectedFrame.Result.IsError = true
		expectedFrame.Result.Content = append(expectedFrame.Result.Content, struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{Type: "text", Text: string(expectedPayload)})
		require.NoError(t, encoder.Encode(expectedFrame))
		assert.Equal(t, outerWire.String(), string(wire), "MCP wire is exactly the correlated safe error")
	}

	var httpCursor, cliCursor, mcpCursor string
	t.Run("HTTP full continuation and UTF-8 source integrity", func(t *testing.T) {
		q := workingQuery()
		start := 0
		for page := 0; ; page++ {
			require.Less(t, page, 40, "continuation must make progress")
			code, wire := request(t, agent.Bearer, q)
			require.Equal(t, http.StatusOK, code, string(wire))
			require.LessOrEqual(t, len(wire), budget)
			value := decode(t, wire)
			start = checkPage(t, value, start)
			if page == 0 {
				httpCursor = value.Continuation
			}
			if value.Continuation == "" {
				assert.False(t, value.Truncated)
				assert.Equal(t, len(source), start)
				break
			}
			assert.True(t, value.Truncated)
			q.Set("continuation", value.Continuation)
		}
		require.NotEmpty(t, httpCursor)
		require.Greater(t, counting.opens.Load(), int64(0), "positive control: successful fetch reads the real object")
		// Byte offset 2 lies inside the three-byte character; it must advance to
		// its next rune boundary rather than produce malformed UTF-8.
		q = workingQuery()
		q.Set("start_byte", "2")
		code, wire := request(t, agent.Bearer, q)
		require.Equal(t, 200, code, string(wire))
		value := decode(t, wire)
		end := checkPage(t, value, 4)
		require.NotEmpty(t, value.Continuation)
		q.Del("start_byte")
		q.Set("continuation", value.Continuation)
		code, wire = request(t, agent.Bearer, q)
		require.Equal(t, 200, code, string(wire))
		checkPage(t, decode(t, wire), end)
	})
	t.Run("CLI real command and HTTP continuation", func(t *testing.T) {
		out, stderr, err := cli(t, agent.Bearer, true, cliArgs...)
		require.NoError(t, err, "%s", stderr)
		first := cliPage(t, out)
		end := checkPage(t, first, 0)
		cliCursor = first.Continuation
		require.NotEmpty(t, cliCursor)
		cursor := cliCursor
		for page := 1; cursor != ""; page++ {
			require.Less(t, page, 40, "CLI continuation must make progress")
			out, stderr, err = cli(t, agent.Bearer, true, append(append([]string{}, cliArgs...), "--continuation", cursor)...)
			require.NoError(t, err, "%s", stderr)
			value := cliPage(t, out)
			end = checkPage(t, value, end)
			cursor = value.Continuation
		}
		assert.Equal(t, len(source), end)

		nonzero := append(append([]string{}, cliArgs...), "--start", "2")
		out, stderr, err = cli(t, agent.Bearer, true, nonzero...)
		require.NoError(t, err, "%s", stderr)
		partial := cliPage(t, out)
		end = checkPage(t, partial, 4)
		require.NotEmpty(t, partial.Continuation)
		out, stderr, err = cli(t, agent.Bearer, true, append(append([]string{}, cliArgs...), "--continuation", partial.Continuation)...)
		require.NoError(t, err, "%s", stderr)
		checkPage(t, cliPage(t, out), end)
	})
	t.Run("MCP real stdio process and HTTP continuation", func(t *testing.T) {
		args := mcpArgs()
		wire, reply := mcp(t, agent.Bearer, args)
		require.LessOrEqual(t, len(wire), budget)
		require.False(t, reply.Result.IsError, string(wire))
		first := decode(t, []byte(reply.Result.Content[0].Text))
		end := checkPage(t, first, 0)
		mcpCursor = first.Continuation
		require.NotEmpty(t, mcpCursor)
		cursor := mcpCursor
		for page := 1; cursor != ""; page++ {
			require.Less(t, page, 40, "MCP continuation must make progress")
			args["continuation"] = cursor
			wire, reply = mcp(t, agent.Bearer, args)
			require.LessOrEqual(t, len(wire), budget)
			require.False(t, reply.Result.IsError, string(wire))
			value := decode(t, []byte(reply.Result.Content[0].Text))
			end = checkPage(t, value, end)
			cursor = value.Continuation
		}
		assert.Equal(t, len(source), end)

		args = mcpArgs()
		args["start_byte"] = 2
		wire, reply = mcp(t, agent.Bearer, args)
		require.LessOrEqual(t, len(wire), budget)
		require.False(t, reply.Result.IsError, string(wire))
		partial := decode(t, []byte(reply.Result.Content[0].Text))
		end = checkPage(t, partial, 4)
		require.NotEmpty(t, partial.Continuation)
		delete(args, "start_byte")
		args["continuation"] = partial.Continuation
		wire, reply = mcp(t, agent.Bearer, args)
		require.LessOrEqual(t, len(wire), budget)
		require.False(t, reply.Result.IsError, string(wire))
		checkPage(t, decode(t, []byte(reply.Result.Content[0].Text)), end)
	})
	t.Run("MCP writes reach the real server once it advertises a revision bound", func(t *testing.T) {
		tool := func(name string, args map[string]any) (map[string]any, bool) {
			wire, reply := writerMCP.call(t, "tools/call", map[string]any{"name": name, "arguments": args})
			var payload map[string]any
			require.NoError(t, json.Unmarshal([]byte(reply.Result.Content[0].Text), &payload), string(wire))
			return payload, reply.Result.IsError
		}
		writeScope := map[string]any{"project_id": "demo", "task_id": "mcp-write-task"}
		with := func(extra map[string]any) map[string]any {
			out := map[string]any{}
			for k, v := range writeScope {
				out[k] = v
			}
			for k, v := range extra {
				out[k] = v
			}
			return out
		}

		// Without status:read the bound cannot be learned, so no write is tried.
		blocked, isErr := func() (map[string]any, bool) {
			wire, reply := uncitedMCP.call(t, "tools/call", map[string]any{"name": "memory_checkpoint", "arguments": map[string]any{"project_id": "demo", "task_id": "uncited-task", "checkpoint_id": "never-written", "objective": "o"}})
			var payload map[string]any
			require.NoError(t, json.Unmarshal([]byte(reply.Result.Content[0].Text), &payload), string(wire))
			return payload, reply.Result.IsError
		}()
		require.True(t, isErr)
		assert.Equal(t, "unavailable", blocked["error"].(map[string]any)["code"])
		assert.Contains(t, blocked["error"].(map[string]any)["action"], "No write was attempted")

		cp, isError := tool("memory_checkpoint", with(map[string]any{"checkpoint_id": "mcp-checkpoint", "objective": "write through MCP"}))
		require.False(t, isError, cp)
		revision := cp["checkpoint"].(map[string]any)["revision"].(string)
		require.NotEmpty(t, revision)
		require.LessOrEqual(t, len(revision), domain.MaxRevisionBytes)

		stale, isError := tool("memory_checkpoint", with(map[string]any{"checkpoint_id": "mcp-checkpoint", "objective": "stale update", "expected_revision": "not-the-current-revision"}))
		require.True(t, isError)
		assert.Contains(t, []string{"conflict", "stale"}, stale["error"].(map[string]any)["code"], "a stale expected revision must never overwrite")

		claim, isError := tool("memory_submit_candidate", with(map[string]any{"claim_id": "mcp-claim", "text": "MCP submitted finding", "kind": "finding"}))
		require.False(t, isError, claim)
		assert.Equal(t, "candidate", claim["claim_status"])
		require.LessOrEqual(t, len(claim["revision"].(string)), domain.MaxRevisionBytes)

		found, isError := tool("memory_search", with(map[string]any{"mode": "working", "query": "MCP submitted finding"}))
		require.False(t, isError, found)
		var texts []string
		for _, c := range found["chunks"].([]any) {
			chunk := c.(map[string]any)
			texts = append(texts, chunk["text"].(string))
			assert.Equal(t, "candidate", chunk["claim_status"], "a submission is never verified by being written")
		}
		assert.Contains(t, texts, "MCP submitted finding")
	})

	t.Run("denials happen before object reads", func(t *testing.T) {
		for _, tc := range []struct {
			name, bearer string
			q            url.Values
			want         int
		}{
			{"missing bearer", "", workingQuery(), 401},
			{"invalid bearer", strings.Repeat("a", 64), workingQuery(), 401},
			{"other client", other.Bearer, workingQuery(), 403},
			{"missing evidence capability", limited.Bearer, workingQuery(), 403},
			{"wrong task", agent.Bearer, func() url.Values { q := workingQuery(); q.Set("task_id", "other-task"); return q }(), 403},
			{"wrong project", agent.Bearer, func() url.Values { q := workingQuery(); q.Set("project_id", "other-project"); return q }(), 403},
			{"default publication cannot fetch task artifact", agent.Bearer, url.Values{"locator": {artifact.SourceLocator}}, 403},
			{"wrong locator", agent.Bearer, func() url.Values { q := workingQuery(); q.Set("locator", "wrong"); return q }(), 403},
		} {
			t.Run(tc.name, func(t *testing.T) {
				before := counting.opens.Load()
				code, body := request(t, tc.bearer, tc.q)
				assert.Equal(t, tc.want, code, string(body))
				assert.Equal(t, before, counting.opens.Load())
				wantCode := "forbidden"
				if tc.want == http.StatusUnauthorized {
					wantCode = "unauthorized"
				}
				httpError(t, body, tc.want, wantCode)
			})
		}
		before := counting.opens.Load()
		out, stderr, err := cli(t, other.Bearer, true, cliArgs...)
		cliError(t, out, stderr, err, "forbidden")
		assert.Equal(t, before, counting.opens.Load(), "CLI cross-client denial must precede object open")
		before = counting.opens.Load()
		out, stderr, err = cli(t, agent.Bearer, false, cliArgs...)
		cliError(t, out, stderr, err, "credential_unavailable")
		assert.Equal(t, before, counting.opens.Load(), "CLI missing credential must precede object open")
		before = counting.opens.Load()
		wire, reply := mcp(t, other.Bearer, mcpArgs())
		mcpError(t, wire, reply, "forbidden")
		assert.Equal(t, before, counting.opens.Load(), "MCP cross-client denial must precede object open")
	})
	t.Run("task owner cannot fetch another client's citation", func(t *testing.T) {
		q := url.Values{"locator": {uncitedArtifact.SourceLocator}, "mode": {"working"}, "project_id": {uncitedScope.ProjectID}, "task_id": {uncitedScope.TaskID}, "budget_bytes": {strconv.Itoa(budget)}}
		before := counting.opens.Load()
		code, body := requestArtifact(t, uncitedOwner.Bearer, uncitedArtifact.ID, q)
		assert.Equal(t, http.StatusForbidden, code)
		httpError(t, body, http.StatusForbidden, "forbidden")
		assert.Equal(t, before, counting.opens.Load(), "HTTP citation denial must precede object open")

		args := []string{"fetch", "--id", uncitedArtifact.ID, "--locator", uncitedArtifact.SourceLocator, "--mode", "working", "--project", uncitedScope.ProjectID, "--task", uncitedScope.TaskID, "--budget", strconv.Itoa(budget)}
		before = counting.opens.Load()
		out, stderr, err := cli(t, uncitedOwner.Bearer, true, args...)
		cliError(t, out, stderr, err, "forbidden")
		assert.Equal(t, before, counting.opens.Load(), "CLI citation denial must precede object open")

		mcpInput := map[string]any{"artifact_id": uncitedArtifact.ID, "locator": uncitedArtifact.SourceLocator, "mode": "working", "project_id": uncitedScope.ProjectID, "task_id": uncitedScope.TaskID, "budget_bytes": budget}
		before = counting.opens.Load()
		wire, reply := mcp(t, uncitedOwner.Bearer, mcpInput)
		mcpError(t, wire, reply, "forbidden")
		assert.Equal(t, before, counting.opens.Load(), "MCP citation denial must precede object open")

		// The same client and artifact become readable after an own citation;
		// this positive control rules out an unrelated denial or broken fixture.
		_, err = memory.Submit(t.Context(), uncitedOwner.Principal, domain.CandidateInput{ClaimID: "owner-claim", Text: "own citation", Kind: "finding", Scope: uncitedScope, Evidence: []domain.EvidenceRef{{ArtifactID: uncitedArtifact.ID, Digest: uncitedArtifact.Digest, Locator: uncitedArtifact.SourceLocator, SourceRevision: uncitedArtifact.Generation}}})
		require.NoError(t, err)
		before = counting.opens.Load()
		code, body = requestArtifact(t, uncitedOwner.Bearer, uncitedArtifact.ID, q)
		require.Equal(t, http.StatusOK, code, string(body))
		value := decode(t, body)
		require.Len(t, value.Chunks, 1)
		assert.Equal(t, uncitedSource, value.Chunks[0].Text)
		assert.Greater(t, counting.opens.Load(), before)
		before = counting.opens.Load()
		out, stderr, err = cli(t, uncitedOwner.Bearer, true, args...)
		require.NoError(t, err, "%s", stderr)
		value = cliPage(t, out)
		require.Len(t, value.Chunks, 1)
		assert.Equal(t, uncitedArtifact.ID, value.Chunks[0].RecordID)
		require.Positive(t, value.Chunks[0].Passage.End)
		require.LessOrEqual(t, value.Chunks[0].Passage.End, len(uncitedSource))
		assert.Equal(t, uncitedSource[:value.Chunks[0].Passage.End], value.Chunks[0].Text)
		assert.Greater(t, counting.opens.Load(), before)
		before = counting.opens.Load()
		wire, reply = mcp(t, uncitedOwner.Bearer, mcpInput)
		require.False(t, reply.Result.IsError, string(wire))
		value = decode(t, []byte(reply.Result.Content[0].Text))
		require.Len(t, value.Chunks, 1)
		assert.Equal(t, uncitedArtifact.ID, value.Chunks[0].RecordID)
		require.Positive(t, value.Chunks[0].Passage.End)
		require.LessOrEqual(t, value.Chunks[0].Passage.End, len(uncitedSource))
		assert.Equal(t, uncitedSource[:value.Chunks[0].Passage.End], value.Chunks[0].Text)
		assert.Greater(t, counting.opens.Load(), before)
	})
	t.Run("tiny budget returns error without source", func(t *testing.T) {
		before := counting.opens.Load()
		q := workingQuery()
		q.Set("budget_bytes", "100")
		code, body := request(t, agent.Bearer, q)
		assert.Equal(t, 413, code, string(body))
		httpError(t, body, 413, "budget")
		assert.Equal(t, before, counting.opens.Load(), "HTTP tiny budget must precede object open")
		args := append([]string{}, cliArgs...)
		args[len(args)-1] = "100"
		before = counting.opens.Load()
		out, stderr, err := cli(t, agent.Bearer, true, args...)
		cliError(t, out, stderr, err, "budget")
		assert.Equal(t, before, counting.opens.Load(), "CLI tiny budget must precede object open")
		mcpInput := mcpArgs()
		mcpInput["budget_bytes"] = 100
		before = counting.opens.Load()
		wire, reply := mcp(t, agent.Bearer, mcpInput)
		mcpError(t, wire, reply, "budget")
		assert.Equal(t, before, counting.opens.Load(), "MCP tiny budget must precede object open")
	})
	t.Run("epoch change invalidates cursors while credential remains valid", func(t *testing.T) {
		require.NotEmpty(t, httpCursor)
		require.NotEmpty(t, cliCursor)
		require.NotEmpty(t, mcpCursor)
		// The citation control above changed the knowledge epoch. Refresh all
		// three cursors now so this case isolates the subsequent auth change.
		code, body := request(t, agent.Bearer, workingQuery())
		require.Equal(t, 200, code, string(body))
		httpCursor = decode(t, body).Continuation
		out, stderr, err := cli(t, agent.Bearer, true, cliArgs...)
		require.NoError(t, err, "%s", stderr)
		cliCursor = cliPage(t, out).Continuation
		wire, reply := mcp(t, agent.Bearer, mcpArgs())
		require.False(t, reply.Result.IsError, string(wire))
		mcpCursor = decode(t, []byte(reply.Result.Content[0].Text)).Continuation
		require.NotEmpty(t, httpCursor)
		require.NotEmpty(t, cliCursor)
		require.NotEmpty(t, mcpCursor)
		// Revoking a different enrolled client advances the auth epoch, but does
		// not revoke the agent. All previously issued cursors must be stale.
		require.NoError(t, identity.Revoke(t.Context(), owner, other.ClientID))
		before := counting.opens.Load()
		q := workingQuery()
		q.Set("continuation", httpCursor)
		code, body = request(t, agent.Bearer, q)
		assert.Equal(t, 409, code, string(body))
		httpError(t, body, 409, "stale")
		assert.Equal(t, before, counting.opens.Load(), "HTTP stale cursor must precede object open")
		before = counting.opens.Load()
		out, stderr, err = cli(t, agent.Bearer, true, append(append([]string{}, cliArgs...), "--continuation", cliCursor)...)
		cliError(t, out, stderr, err, "stale")
		assert.Equal(t, before, counting.opens.Load(), "CLI stale cursor must precede object open")
		args := mcpArgs()
		args["continuation"] = mcpCursor
		before = counting.opens.Load()
		wire, reply = mcp(t, agent.Bearer, args)
		mcpError(t, wire, reply, "stale")
		assert.Equal(t, before, counting.opens.Load(), "MCP stale cursor must precede object open")

		// A fresh fetch proves the caller remains authorized after epoch change,
		// and gives each transport a current cursor for the revocation case.
		code, body = request(t, agent.Bearer, workingQuery())
		require.Equal(t, 200, code, string(body))
		httpCursor = decode(t, body).Continuation
		out, stderr, err = cli(t, agent.Bearer, true, cliArgs...)
		require.NoError(t, err, "%s", stderr)
		cliCursor = cliPage(t, out).Continuation
		wire, reply = mcp(t, agent.Bearer, mcpArgs())
		require.False(t, reply.Result.IsError, string(wire))
		mcpCursor = decode(t, []byte(reply.Result.Content[0].Text)).Continuation
	})
	t.Run("revocation during object IO blocks the response", func(t *testing.T) {
		started, release := counting.blockOneOpen()
		type result struct {
			code int
			body []byte
			err  error
		}
		done := make(chan result, 1)
		go func() {
			u := server.URL + "/v1/evidence/" + url.PathEscape(artifact.ID) + "?" + workingQuery().Encode()
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, u, nil)
			if err != nil {
				done <- result{err: err}
				return
			}
			req.Header.Set("Authorization", "Bearer "+agent.Bearer)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				done <- result{err: err}
				return
			}
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			done <- result{code: resp.StatusCode, body: body, err: err}
		}()
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		defer unblock()
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("fetch never reached object open")
		}
		// Revoke after the first authorization, but before source bytes are read.
		err := identity.Revoke(t.Context(), owner, agent.ClientID)
		unblock()
		require.NoError(t, err)
		select {
		case response := <-done:
			require.NoError(t, response.err)
			assert.Equal(t, 401, response.code, string(response.body))
			httpError(t, response.body, 401, "unauthorized")
		case <-time.After(10 * time.Second):
			t.Fatal("fetch did not finish after object IO was released")
		}
	})
	t.Run("revoked bearer invalidates existing cursors on every transport", func(t *testing.T) {
		require.NotEmpty(t, httpCursor)
		require.NotEmpty(t, cliCursor)
		require.NotEmpty(t, mcpCursor)
		before := counting.opens.Load()
		q := workingQuery()
		q.Set("continuation", httpCursor)
		code, body := request(t, agent.Bearer, q)
		assert.Equal(t, 401, code, string(body))
		httpError(t, body, 401, "unauthorized")
		assert.Equal(t, before, counting.opens.Load(), "HTTP revoked credential must precede object open")
		before = counting.opens.Load()
		out, stderr, err := cli(t, agent.Bearer, true, append(append([]string{}, cliArgs...), "--continuation", cliCursor)...)
		cliError(t, out, stderr, err, "unauthorized")
		assert.Equal(t, before, counting.opens.Load(), "CLI revoked credential must precede object open")
		args := mcpArgs()
		args["continuation"] = mcpCursor
		before = counting.opens.Load()
		wire, reply := mcp(t, agent.Bearer, args)
		mcpError(t, wire, reply, "unauthorized")
		assert.Equal(t, before, counting.opens.Load(), "MCP revoked credential must precede object open")
	})
}
