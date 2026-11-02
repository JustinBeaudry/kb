package handler

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

// The MCP consumer interfaces are bound to exactly one authenticated client
// credential by the composition root. No method accepts a Principal: identity is
// derived by the authoritative server from that credential, never from tool
// arguments or token text.
type RetrievalReader interface {
	Retrieve(context.Context, domain.RetrievalRequest) (domain.RetrievalResult, error)
	Fetch(context.Context, domain.FetchRequest) (domain.RetrievalResult, error)
}
type CandidateWriter interface {
	Submit(context.Context, domain.CandidateInput) (domain.ClaimRevision, error)
}
type CheckpointWriter interface {
	PutCheckpoint(context.Context, string, domain.Checkpoint) (domain.Checkpoint, error)
}
type StatusReader interface {
	Status(context.Context) (domain.Status, error)
}

// MCPServer is a transport-independent MCP tool dispatcher. Only stdio framing
// is implemented; remote HTTP MCP stays unadvertised until independently
// authenticated and qualified.
type MCPServer struct {
	retrieval   RetrievalReader
	memory      CandidateWriter
	checkpoints CheckpointWriter
	status      StatusReader
	cursorKey   [32]byte
}

func NewMCPServer(retrieval RetrievalReader, memory CandidateWriter, checkpoints CheckpointWriter, status StatusReader) *MCPServer {
	s := &MCPServer{retrieval: retrieval, memory: memory, checkpoints: checkpoints, status: status}
	// An unpredictable per-server key protects the outer frame budget from
	// modification. Cursors are deliberately invalidated on server restart.
	if _, err := rand.Read(s.cursorKey[:]); err != nil {
		panic("cannot generate MCP continuation key: " + err.Error())
	}
	return s
}

const (
	mcpMaxFrame = 1 << 20
	// Bound the encoded ID as well as the input: JSON encoding can expand U+2028/U+2029.
	mcpMaxIDBytes        = 256
	mcpContinuation      = "mcp2."
	mcpBudgetAttempts    = 16
	mcpInvalidParams     = -32602
	mcpMethodNotFound    = -32601
	mcpInvalidRequest    = -32600
	mcpParseError        = -32700
	mcpServerName        = "kb-mcp"
	mcpServerVersion     = "0.1.0"
	mcpTransportGap      = "http_mcp_transport_unavailable"
	mcpDefaultSmallFrame = domain.OrientationBudget
)

var mcpProtocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}
type toolResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError"`
}
type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ServeStdio reads newline-delimited JSON-RPC requests and writes one response
// line per request. Frames never contain embedded newlines. Requests are
// handled in order; it returns nil at end of input and ctx.Err() on cancel.
func (s *MCPServer) ServeStdio(ctx context.Context, in io.Reader, out io.Writer) error {
	type frame struct {
		line      []byte
		oversized bool
		err       error
	}
	frames := make(chan frame)
	stop := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(stop) })
	go func() {
		reader := bufio.NewReaderSize(in, 64<<10)
		for {
			var line []byte
			oversized := false
			for {
				part, prefix, err := reader.ReadLine()
				if err != nil {
					select {
					case frames <- frame{err: err}:
					case <-stop:
					}
					return
				}
				if len(line)+len(part) > mcpMaxFrame {
					oversized, line = true, nil
				} else if !oversized {
					line = append(line, part...)
				}
				if !prefix {
					break
				}
			}
			select {
			case frames <- frame{line: line, oversized: oversized}:
			case <-stop:
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case f := <-frames:
			if f.err != nil {
				if errors.Is(f.err, io.EOF) {
					return nil
				}
				return f.err
			}
			if len(bytes.TrimSpace(f.line)) == 0 && !f.oversized {
				continue
			}
			response, ok := s.handle(ctx, f.line, f.oversized)
			if !ok {
				continue
			}
			if _, err := out.Write(append(response, '\n')); err != nil {
				return err
			}
		}
	}
}

func encode(v any) []byte {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(v)
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}
func failure(id json.RawMessage, code int, message string) []byte {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return encode(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}})
}

// handle returns false for notifications, which never receive a response.
func (s *MCPServer) handle(ctx context.Context, line []byte, oversized bool) ([]byte, bool) {
	if oversized {
		return failure(nil, mcpInvalidRequest, "Request frame too large."), true
	}
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		return failure(nil, mcpInvalidRequest, "Batch requests are not supported."), true
	}
	var request rpcRequest
	if err := json.Unmarshal(trimmed, &request); err != nil {
		return failure(nil, mcpParseError, "Parse error."), true
	}
	// An explicit null ID is discouraged by JSON-RPC, but is not a
	// notification: only an absent ID suppresses the response.
	notification := len(request.ID) == 0
	if !notification && !validRequestID(request.ID) {
		// An invalid/oversized ID cannot be safely echoed. Per JSON-RPC's
		// invalid-request convention, return null; the client cannot correlate it.
		return failure(nil, mcpInvalidRequest, "Invalid request ID (maximum 256 encoded bytes)."), true
	}
	if request.JSONRPC != "2.0" || (request.Method == "" && !notification) {
		return failure(request.ID, mcpInvalidRequest, "Invalid request."), true
	}
	if notification {
		return nil, false
	}
	var result any
	switch request.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(request.Params, &params)
		version := mcpProtocolVersions[0]
		if slices.Contains(mcpProtocolVersions, params.ProtocolVersion) {
			version = params.ProtocolVersion
		}
		result = map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{"listChanged": false}}, "serverInfo": map[string]any{"name": mcpServerName, "version": mcpServerVersion}}
	case "ping":
		result = map[string]any{}
	case "tools/list":
		result = map[string]any{"tools": s.toolDescriptions()}
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil {
			return failure(request.ID, mcpInvalidParams, "Invalid tool call."), true
		}
		frame, err := s.call(ctx, request.ID, params.Name, params.Arguments)
		if err != nil {
			return failure(request.ID, mcpInvalidParams, "Unknown tool or invalid arguments."), true
		}
		return frame, true
	default:
		return failure(request.ID, mcpMethodNotFound, "Method not found."), true
	}
	return encode(rpcResponse{JSONRPC: "2.0", ID: request.ID, Result: result}), true
}

// validRequestID accepts JSON-RPC string, number and null IDs (fractional
// numbers and null are discouraged by JSON-RPC, but valid). Check input and response
// encoding, since the JSON encoder may expand some valid UTF-8 characters.
func validRequestID(id json.RawMessage) bool {
	if len(id) == 0 || len(id) > mcpMaxIDBytes {
		return false
	}
	if string(id) == "null" {
		return true
	}
	if id[0] != '"' && id[0] != '-' && (id[0] < '0' || id[0] > '9') {
		return false
	}
	return len(encode(id)) <= mcpMaxIDBytes
}

// errInvalidArguments is a protocol-level rejection; no service is called.
var errInvalidArguments = errors.New("invalid arguments")

func strict(raw json.RawMessage, v any) error {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage("{}")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.More() {
		return errInvalidArguments
	}
	return nil
}

type applicabilityArgs struct {
	RepositoryRevision string            `json:"repository_revision"`
	DirtyDigest        string            `json:"dirty_digest"`
	Environment        string            `json:"environment"`
	Versions           map[string]string `json:"versions"`
}

func (a *applicabilityArgs) domain() domain.Applicability {
	if a == nil {
		return domain.Applicability{}
	}
	return domain.Applicability{RepositoryRevision: a.RepositoryRevision, DirtyDigest: a.DirtyDigest, Environment: a.Environment, Versions: a.Versions}
}

type retrievalArgs struct {
	ProjectID     string             `json:"project_id"`
	TaskID        string             `json:"task_id"`
	SessionID     string             `json:"session_id"`
	Query         string             `json:"query"`
	EntityIDs     []string           `json:"entity_ids"`
	Applicability *applicabilityArgs `json:"applicability"`
	Mode          string             `json:"mode"`
	BudgetBytes   int                `json:"budget_bytes"`
	MaxDepth      *int               `json:"max_depth"`
	MaxVisited    int                `json:"max_visited"`
	Continuation  string             `json:"continuation"`
}
type fetchArgs struct {
	ArtifactID   string `json:"artifact_id"`
	Locator      string `json:"locator"`
	Mode         string `json:"mode"`
	ProjectID    string `json:"project_id"`
	TaskID       string `json:"task_id"`
	SessionID    string `json:"session_id"`
	StartByte    int64  `json:"start_byte"`
	EndByte      *int64 `json:"end_byte"`
	BudgetBytes  int    `json:"budget_bytes"`
	Continuation string `json:"continuation"`
}
type evidenceArgs struct {
	ArtifactID     string `json:"artifact_id"`
	Digest         string `json:"digest"`
	Locator        string `json:"locator"`
	SourceRevision string `json:"source_revision"`
}
type candidateArgs struct {
	ProjectID        string             `json:"project_id"`
	TaskID           string             `json:"task_id"`
	SessionID        string             `json:"session_id"`
	ClaimID          string             `json:"claim_id"`
	ExpectedRevision string             `json:"expected_revision"`
	Text             string             `json:"text"`
	Kind             string             `json:"kind"`
	Evidence         []evidenceArgs     `json:"evidence"`
	DependsOn        []string           `json:"depends_on"`
	EntityIDs        []string           `json:"entity_ids"`
	Applicability    *applicabilityArgs `json:"applicability"`
	BudgetBytes      int                `json:"budget_bytes"`
}
type checkpointArgs struct {
	CheckpointID        string   `json:"checkpoint_id"`
	ProjectID           string   `json:"project_id"`
	TaskID              string   `json:"task_id"`
	SessionID           string   `json:"session_id"`
	ExpectedRevision    string   `json:"expected_revision"`
	Objective           string   `json:"objective"`
	Completed           []string `json:"completed"`
	Pending             []string `json:"pending"`
	Questions           []string `json:"questions"`
	EvidenceRevisionIDs []string `json:"evidence_revision_ids"`
	BudgetBytes         int      `json:"budget_bytes"`
}
type statusArgs struct {
	BudgetBytes int `json:"budget_bytes"`
}

// call validates arguments strictly, invokes only the authenticated service,
// and renders a complete response frame within the caller budget. Each page
// is fetched and authorized independently; future pages are not prevalidated.
func (s *MCPServer) call(ctx context.Context, id json.RawMessage, name string, raw json.RawMessage) ([]byte, error) {
	switch name {
	case "memory_orient", "memory_search":
		var a retrievalArgs
		if strict(raw, &a) != nil {
			return nil, errInvalidArguments
		}
		limit, mode, allowed := domain.EvidenceBudget, domain.RetrievalEvidence, []string{"evidence", "working"}
		if name == "memory_orient" {
			limit, mode, allowed = domain.OrientationBudget, domain.RetrievalOrientation, []string{"orientation", "working"}
		}
		if a.Mode != "" {
			if !slices.Contains(allowed, a.Mode) {
				return nil, errInvalidArguments
			}
			mode = domain.RetrievalMode(a.Mode)
		}
		budget, ok := frameBudget(a.BudgetBytes, limit)
		if !ok || a.ProjectID == "" || (mode == domain.RetrievalWorking && a.TaskID == "") {
			return nil, errInvalidArguments
		}
		depth := domain.MaxGraphDepth
		if a.MaxDepth != nil {
			depth = *a.MaxDepth
		}
		r := domain.RetrievalRequest{Scope: domain.Scope{ProjectID: a.ProjectID, TaskID: a.TaskID, SessionID: a.SessionID}, Query: a.Query, EntityIDs: a.EntityIDs, Applicability: a.Applicability.domain(), MaxDepth: depth, MaxVisited: a.MaxVisited, Mode: mode}
		if r.MaxVisited == 0 {
			r.MaxVisited = domain.MaxGraphVisited
		}
		return s.bounded(ctx, id, budget, a.Continuation, func(service int, cursor string) (domain.RetrievalResult, error) {
			r.BudgetBytes, r.Continuation = service, cursor
			return s.retrieval.Retrieve(ctx, r)
		}), nil
	case "memory_fetch":
		var a fetchArgs
		if strict(raw, &a) != nil {
			return nil, errInvalidArguments
		}
		budget, ok := frameBudget(a.BudgetBytes, domain.EvidenceBudget)
		mode := domain.FetchPublication
		if a.Mode == "working" {
			mode = domain.FetchWorking
		}
		scoped := a.ProjectID != "" || a.TaskID != "" || a.SessionID != ""
		if !ok || a.ArtifactID == "" || a.Locator == "" || (a.Mode != "" && a.Mode != "publication" && a.Mode != "working") || (mode == domain.FetchWorking && (a.ProjectID == "" || a.TaskID == "")) || (mode == domain.FetchPublication && scoped) {
			return nil, errInvalidArguments
		}
		r := domain.FetchRequest{ArtifactID: a.ArtifactID, Locator: a.Locator, Mode: mode, StartByte: a.StartByte, EndByte: a.EndByte}
		if mode == domain.FetchWorking {
			r.Scope = domain.Scope{ProjectID: a.ProjectID, TaskID: a.TaskID, SessionID: a.SessionID}
		}
		if a.Continuation != "" && a.StartByte != 0 {
			return nil, errInvalidArguments
		}
		return s.bounded(ctx, id, budget, a.Continuation, func(service int, cursor string) (domain.RetrievalResult, error) {
			r.BudgetBytes, r.Continuation = service, cursor
			if cursor != "" {
				r.StartByte = 0
			} else {
				r.StartByte = a.StartByte
			}
			return s.retrieval.Fetch(ctx, r)
		}), nil
	case "memory_submit_candidate":
		var a candidateArgs
		if strict(raw, &a) != nil {
			return nil, errInvalidArguments
		}
		budget, ok := frameBudget(a.BudgetBytes, mcpDefaultSmallFrame)
		if !ok || a.ProjectID == "" || a.TaskID == "" || a.ClaimID == "" || a.Text == "" || a.Kind == "" || len(a.Evidence) > 50 {
			return nil, errInvalidArguments
		}
		if err := mutationPreflight(ctx, s.memory, id, budget); err != nil {
			return mutationPreflightFrame(id, budget, err), nil
		}
		refs := make([]domain.EvidenceRef, 0, len(a.Evidence))
		for _, e := range a.Evidence {
			refs = append(refs, domain.EvidenceRef{ArtifactID: e.ArtifactID, Digest: e.Digest, Locator: e.Locator, SourceRevision: e.SourceRevision})
		}
		c, err := s.memory.Submit(ctx, domain.CandidateInput{ClaimID: a.ClaimID, ExpectedRevision: a.ExpectedRevision, Text: a.Text, Kind: a.Kind, Scope: domain.Scope{ProjectID: a.ProjectID, TaskID: a.TaskID, SessionID: a.SessionID}, Evidence: refs, DependsOn: a.DependsOn, EntityIDs: a.EntityIDs, Applicability: a.Applicability.domain()})
		if err != nil {
			return toolFrame(id, budget, nil, err), nil
		}
		// A compact receipt: the submitted text is not echoed back.
		return mutationFrame(id, budget, c.Revision, map[string]any{"schema_version": "3", "claim_id": c.ClaimID, "revision": c.Revision, "claim_status": c.Status, "origin_trust": domain.TrustSubmitted, "scope": scopeValue(c.Scope), "warnings": []string{"unverified_task_candidate"}}), nil
	case "memory_checkpoint":
		var a checkpointArgs
		if strict(raw, &a) != nil {
			return nil, errInvalidArguments
		}
		budget, ok := frameBudget(a.BudgetBytes, mcpDefaultSmallFrame)
		if !ok || a.CheckpointID == "" || a.ProjectID == "" || a.TaskID == "" {
			return nil, errInvalidArguments
		}
		if err := mutationPreflight(ctx, s.checkpoints, id, budget); err != nil {
			return mutationPreflightFrame(id, budget, err), nil
		}
		c, err := s.checkpoints.PutCheckpoint(ctx, a.ExpectedRevision, domain.Checkpoint{ID: a.CheckpointID, Scope: domain.Scope{ProjectID: a.ProjectID, TaskID: a.TaskID, SessionID: a.SessionID}, Objective: a.Objective, Completed: nonNil(a.Completed), Pending: nonNil(a.Pending), Questions: nonNil(a.Questions), EvidenceRevisionIDs: nonNil(a.EvidenceRevisionIDs)})
		if err != nil {
			return toolFrame(id, budget, nil, err), nil
		}
		return mutationFrame(id, budget, c.Revision, map[string]any{"schema_version": "3", "label": "task_state", "checkpoint": map[string]any{"id": c.ID, "revision": c.Revision, "scope": scopeValue(c.Scope), "updated_at": c.UpdatedAt}}), nil
	case "memory_status":
		var a statusArgs
		if strict(raw, &a) != nil {
			return nil, errInvalidArguments
		}
		budget, ok := frameBudget(a.BudgetBytes, mcpDefaultSmallFrame)
		if !ok {
			return nil, errInvalidArguments
		}
		st, err := s.status.Status(ctx)
		if err != nil {
			return toolFrame(id, budget, nil, err), nil
		}
		gaps := append(slices.Clone(st.CapabilityGaps), mcpTransportGap)
		return toolFrame(id, budget, map[string]any{"schema_version": "3", "connection_state": st.ConnectionState, "authorized_project_ids": nonNil(st.AuthorizedProjectIDs), "authorized_capabilities": nonNil(st.AuthorizedCapabilities), "capability_gaps": gaps}, nil), nil
	}
	return nil, errInvalidArguments
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
func scopeValue(s domain.Scope) map[string]any {
	m := map[string]any{"project_id": s.ProjectID}
	if s.TaskID != "" {
		m["task_id"] = s.TaskID
	}
	if s.SessionID != "" {
		m["session_id"] = s.SessionID
	}
	return m
}
func frameBudget(requested, limit int) (int, bool) {
	if requested == 0 {
		return limit, true
	}
	return requested, requested >= 1 && requested <= limit
}

// toolFrame renders one complete JSON-RPC response line. Retrieval data only
// ever appears inside a text content block, never as an instruction role.
func toolFrame(id json.RawMessage, budget int, payload any, err error) []byte {
	if err != nil {
		p := problemFor(err)
		frame := renderTool(id, map[string]any{"schema_version": "3", "error": map[string]any{"code": p.code, "action": p.action}}, true)
		if len(frame)+1 <= budget {
			return frame
		}
		// Prefer the original closed error code if its compact form fits.
		frame = renderTool(id, map[string]any{"schema_version": "3", "error": map[string]any{"code": p.code}}, true)
		if len(frame)+1 <= budget {
			return frame
		}
		// If even the original compact error does not fit, say why. When
		// the compact budget error cannot fit either, it is the sole bounded
		// exception to the requested budget: an intact JSON-RPC ID and an
		// explicit error cannot be represented in arbitrarily few bytes.
		return renderTool(id, map[string]any{"schema_version": "3", "error": map[string]any{"code": "budget"}}, true)
	}
	frame := renderTool(id, payload, false)
	if len(frame)+1 > budget {
		return toolFrame(id, budget, nil, domain.ErrBudget)
	}
	return frame
}
func renderTool(id json.RawMessage, payload any, isError bool) []byte {
	return encode(rpcResponse{JSONRPC: "2.0", ID: id, Result: toolResult{Content: []toolContent{{Type: "text", Text: string(encode(payload))}}, IsError: isError}})
}

// boundedRevisionWriter is a contract on the writer, not a guess about the
// current implementation. Every successful write must return a nonempty,
// valid-UTF-8 revision of at most MaxRevisionBytes raw bytes. The writer must
// enforce the bound before commit. A remote writer reports the bound its
// server advertises, or zero when none is confirmed.
type boundedRevisionWriter interface {
	MaxRevisionBytes(context.Context) int
}

func compactMutationReceipt(revision string) map[string]any {
	return map[string]any{"schema_version": "3", "receipt": "committed", "revision": revision}
}

var errRevisionBoundUnknown = errors.New("writer has no enforced revision bound")

func mutationPreflight(ctx context.Context, writer any, id json.RawMessage, budget int) error {
	// Even a single-byte revision cannot fit.
	if len(renderTool(id, compactMutationReceipt("x"), false))+1 > budget {
		return domain.ErrBudget
	}
	bounded, ok := writer.(boundedRevisionWriter)
	if !ok {
		return errRevisionBoundUnknown
	}
	n := bounded.MaxRevisionBytes(ctx)
	if n <= 0 {
		return errRevisionBoundUnknown
	}
	// A revision cannot fit if its raw bytes alone exceed the frame. This
	// also bounds the allocation for untrusted/incorrect implementations.
	if n > budget || len(renderTool(id, compactMutationReceipt(strings.Repeat("\x00", n)), false))+1 > budget {
		return domain.ErrBudget
	}
	return nil
}

func mutationPreflightFrame(id json.RawMessage, budget int, err error) []byte {
	if !errors.Is(err, errRevisionBoundUnknown) {
		return toolFrame(id, budget, nil, err)
	}
	frame := renderTool(id, map[string]any{"schema_version": "3", "error": map[string]any{
		"code": "unavailable", "action": "The server revision bound is unknown: the server is unreachable, predates the bound, or this client lacks status:read. No write was attempted.",
	}}, true)
	if len(frame)+1 <= budget {
		return frame
	}
	return toolFrame(id, budget, nil, domain.ErrUnavailable)
}

func mutationFrame(id json.RawMessage, budget int, revision string, payload any) []byte {
	frame := renderTool(id, payload, false)
	if len(frame)+1 <= budget {
		return frame
	}
	// Preflight reserved the worst-case escaped revision before the write.
	// Never report a successful commit without the revision needed to update it.
	return renderTool(id, compactMutationReceipt(revision), false)
}

// Pagination errors must not imply that the unseen rest of the chain was
// checked. A continuation binds both budgets, so restarting is required to
// change either. Small frames fall back to the compact, closed budget code.
func paginationErrorFrame(id json.RawMessage, budget int, continuation bool) []byte {
	action := "Cannot fit this page. Increase the budget within its limit, or narrow the query or byte range."
	if continuation {
		action = "Cannot fit this page at its bound continuation budget. Restart without the continuation with a larger budget, or narrow the query or byte range. For fetch, resume from the last passage end_byte."
	}
	frame := renderTool(id, map[string]any{"schema_version": "3", "error": map[string]any{"code": "budget", "action": action}}, true)
	if len(frame)+1 <= budget {
		return frame
	}
	if continuation {
		frame = renderTool(id, map[string]any{"schema_version": "3", "error": map[string]any{"code": "budget", "action": "Bound page cannot fit. Restart without continuation; narrow the query or byte range."}}, true)
		if len(frame)+1 <= budget {
			return frame
		}
		// Do not substitute the generic "larger budget" advice: the service
		// budget is bound to this cursor and may itself need a restart.
		return renderTool(id, map[string]any{"schema_version": "3", "error": map[string]any{"code": "budget"}}, true)
	}
	return toolFrame(id, budget, nil, domain.ErrBudget)
}

// initialServiceBudget reserves the largest legal request ID and the wrapper,
// then allows for every byte of the service JSON to need another escaping byte
// inside MCP's text block. This is conservative, not a guarantee: a service
// may need more space for an indivisible item, its output may not conform to
// the service budget, or later pages may have different overhead. The measured
// complete frame is always the final authority.
func initialServiceBudget(budget int) int {
	maxID := json.RawMessage(`"` + strings.Repeat("i", mcpMaxIDBytes-2) + `"`)
	overhead := len(renderTool(maxID, "", false)) + 1 - len(encode(""))
	// Account for mcp2.<outer>.<service>.<MAC>. around the service cursor.
	// Both budgets have at most five digits; reserve six apiece including dots.
	wrapper := len(mcpContinuation) + 12 + base64.RawURLEncoding.EncodedLen(sha256.Size) + 1
	return max(1, (budget-overhead-wrapper)/2)
}

// continuationMAC authenticates the framing metadata and the exact inner cursor.
// It never substitutes for service-side validation or authorization.
func (s *MCPServer) continuationMAC(body string) string {
	mac := hmac.New(sha256.New, s.cursorKey[:])
	_, _ = mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *MCPServer) wrapContinuation(outer, service int, inner string) string {
	body := mcpContinuation + strconv.Itoa(outer) + "." + strconv.Itoa(service) + "." + inner
	return body + "." + s.continuationMAC(body)
}

func (s *MCPServer) unwrapContinuation(raw string, budget int) (int, string, bool) {
	// The inner cursor may itself contain dots; the MAC is the last field.
	last := strings.LastIndexByte(raw, '.')
	if last < 0 {
		return 0, "", false
	}
	body, tag := raw[:last], raw[last+1:]
	if !hmac.Equal([]byte(tag), []byte(s.continuationMAC(body))) {
		return 0, "", false
	}
	rest, ok := strings.CutPrefix(body, mcpContinuation)
	outerText, rest, outerOK := strings.Cut(rest, ".")
	serviceText, inner, serviceOK := strings.Cut(rest, ".")
	outer, outerErr := strconv.Atoi(outerText)
	service, serviceErr := strconv.Atoi(serviceText)
	if !ok || !outerOK || !serviceOK || outerErr != nil || serviceErr != nil || outer != budget || service < 1 || service > budget || inner == "" {
		return 0, "", false
	}
	return service, inner, true
}

// bounded performs only bounded work for this page. On an initial request it
// may retry with different service budgets; a continuation gets exactly one
// authenticated service call because its signed budget cannot be changed.
// The outer MAC binds the frame budget and service cursor, not authorization:
// the service must validate the signed inner cursor, parameters, and current
// authority on every call. Restarting the MCP server invalidates its wrappers.
func (s *MCPServer) bounded(ctx context.Context, id json.RawMessage, budget int, continuation string, fetch func(int, string) (domain.RetrievalResult, error)) []byte {
	service, cursor := initialServiceBudget(budget), ""
	if continuation != "" {
		var ok bool
		service, cursor, ok = s.unwrapContinuation(continuation, budget)
		if !ok {
			return toolFrame(id, budget, nil, domain.ErrStale)
		}
	}
	// A service budget error means an indivisible item needs more space; an
	// oversized MCP frame needs less. Packing is not monotonic, so exhausting
	// the bounded search is reported as a budget error, not proof of impossibility.
	low, high := 1, budget
	for range mcpBudgetAttempts {
		if ctx.Err() != nil {
			return toolFrame(id, budget, nil, domain.ErrUnavailable)
		}
		result, err := fetch(service, cursor)
		tooLarge := false
		if err == nil {
			inner := result.Continuation
			// Reject an immediately repeating cursor without maintaining unsafe,
			// unauthenticated state across requests.
			if inner != "" && inner == cursor {
				return toolFrame(id, budget, nil, domain.ErrUnavailable)
			}
			if inner != "" {
				result.Continuation = s.wrapContinuation(budget, service, inner)
			}
			frame := renderTool(id, domain.RetrievalWireValue(result), false)
			if len(frame)+1 <= budget {
				return frame
			}
			err, tooLarge = domain.ErrBudget, true
		}
		if !errors.Is(err, domain.ErrBudget) {
			return toolFrame(id, budget, nil, err)
		}
		if cursor != "" {
			return paginationErrorFrame(id, budget, true)
		}
		if tooLarge {
			high = service - 1
		} else {
			low = service + 1
		}
		if low > high {
			break
		}
		service = low + (high-low)/2
	}
	return paginationErrorFrame(id, budget, false)
}

// toolDescriptions is static: whether writes are possible is decided per call
// from the server-advertised bound, never by a network call during discovery.
func (s *MCPServer) toolDescriptions() []map[string]any {
	return mcpTools()
}

func mcpTools() []map[string]any {
	str := map[string]any{"type": "string", "minLength": 1}
	strs := map[string]any{"type": "array", "items": str, "maxItems": 50}
	scope := func() map[string]any {
		return map[string]any{"project_id": str, "task_id": str, "session_id": str}
	}
	applicability := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"repository_revision": str, "dirty_digest": str, "environment": str, "versions": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}}}
	budget := func(limit int) map[string]any {
		return map[string]any{"type": "integer", "minimum": 1, "maximum": limit, "default": limit, "description": "Complete successful MCP response frame bytes, including JSON-RPC framing and newline, never exceed this budget. If too small for a compact budget error, that error exceeds the budget but is at most 512 bytes including newline. For continuation keep the effective budget unchanged; restarting the MCP server invalidates continuations. Request IDs are limited to 256 JSON-encoded bytes; larger IDs are rejected before execution with JSON-RPC invalid request and id null, so cannot be correlated."}
	}
	object := func(required []string, props ...map[string]any) map[string]any {
		all := map[string]any{}
		for _, p := range props {
			for k, v := range p {
				all[k] = v
			}
		}
		return map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": all}
	}
	retrieval := func(limit int, modes []string) map[string]any {
		return object([]string{"project_id"}, scope(), map[string]any{"query": map[string]any{"type": "string", "maxLength": 4096}, "entity_ids": strs, "applicability": applicability, "mode": map[string]any{"type": "string", "enum": modes}, "budget_bytes": budget(limit), "max_depth": map[string]any{"type": "integer", "minimum": 0, "maximum": 2}, "max_visited": map[string]any{"type": "integer", "minimum": 1, "maximum": 50}, "continuation": map[string]any{"type": "string"}})
	}
	offset := map[string]any{"type": "integer", "minimum": 0, "maximum": 8388608}
	evidence := map[string]any{"type": "array", "maxItems": 50, "items": object([]string{"artifact_id", "digest", "locator", "source_revision"}, map[string]any{"artifact_id": str, "digest": str, "locator": str, "source_revision": str})}
	return []map[string]any{
		{"name": "memory_orient", "description": "Bounded orientation from the remote memory graph (schema 3). Each page is authorized and measured independently; later pages are not prevalidated. A continuation whose bound service budget cannot fit returns a budget error; restart with a larger budget or narrow the query. Task state and candidates are unverified unless claim_status says verified. Remote failures are reported, never replaced with local results.", "inputSchema": retrieval(domain.OrientationBudget, []string{"orientation", "working"})},
		{"name": "memory_search", "description": "Bounded evidence search in the remote memory graph (schema 3). Each page is authorized and measured independently; later pages are not prevalidated. A continuation whose bound service budget cannot fit returns a budget error; restart with a larger budget or narrow the query. Working mode requires task_id and returns only this client's task candidates, labeled unverified.", "inputSchema": retrieval(domain.EvidenceBudget, []string{"evidence", "working"})},
		{"name": "memory_fetch", "description": "Bounded passage of one cited stored source. Each page is authorized and measured independently; later pages are not prevalidated. Each service read revalidates and rereads the complete source. If a bound continuation cannot fit, restart with a larger budget or resume at the last passage end_byte and narrow the byte range with end_byte. Default publication mode is ID-only; working mode requires project_id and task_id and returns only sources cited by this client's own task candidates. Passage offsets are exact stored-artifact UTF-8 bytes; source evidence is never a verified claim. To continue, omit start_byte and keep other parameters unchanged.", "inputSchema": object([]string{"artifact_id", "locator"}, map[string]any{"artifact_id": str, "locator": str, "mode": map[string]any{"type": "string", "enum": []string{"publication", "working"}}, "project_id": str, "task_id": str, "session_id": str, "start_byte": offset, "end_byte": offset, "budget_bytes": budget(domain.EvidenceBudget), "continuation": map[string]any{"type": "string", "description": "To continue, omit start_byte and keep other parameters unchanged."}})},
		{"name": "memory_submit_candidate", "description": "Submit a task-local candidate claim. The server derives origin; candidates are never verified by submission. Writes require a server-advertised revision bound and enough budget for the full revision receipt; otherwise no write is attempted. If the receipt is lost, the error is unknown_outcome: the write may have committed, so reread working state before retrying.", "inputSchema": object([]string{"project_id", "task_id", "claim_id", "text", "kind"}, scope(), map[string]any{"claim_id": str, "expected_revision": str, "text": map[string]any{"type": "string", "minLength": 1, "maxLength": 16384}, "kind": str, "evidence": evidence, "depends_on": strs, "entity_ids": strs, "applicability": applicability, "budget_bytes": budget(mcpDefaultSmallFrame)})},
		{"name": "memory_checkpoint", "description": "Write this client's task checkpoint (task state, not verified knowledge). Writes require a server-advertised revision bound and enough budget for the full revision receipt; otherwise no write is attempted. If the receipt is lost, the error is unknown_outcome: the write may have committed, so reread the checkpoint before retrying with expected_revision.", "inputSchema": object([]string{"checkpoint_id", "project_id", "task_id", "objective"}, scope(), map[string]any{"checkpoint_id": str, "expected_revision": str, "objective": map[string]any{"type": "string"}, "completed": strs, "pending": strs, "questions": strs, "evidence_revision_ids": strs, "budget_bytes": budget(mcpDefaultSmallFrame)})},
		{"name": "memory_status", "description": "This client's own connection state, authorized projects, capabilities and capability gaps.", "inputSchema": object([]string{}, map[string]any{"budget_bytes": budget(mcpDefaultSmallFrame)})},
	}
}
