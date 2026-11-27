package handler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const remoteMCPBearer = "remote-mcp-fixture"

type remoteMCPIdentity struct{}

func (remoteMCPIdentity) Authenticate(_ context.Context, bearer string) (domain.Principal, error) {
	if bearer != remoteMCPBearer {
		return domain.Principal{}, domain.ErrUnauthorized
	}
	return domain.Principal{ClientID: "agent-a", Role: domain.RoleAgent, ProjectIDs: []string{"demo"}, Capabilities: []string{"retrieval:read", "candidate:write", "checkpoint:write", "status:read"}}, nil
}
func (remoteMCPIdentity) Enroll(context.Context, domain.Principal, service.EnrollInput) (service.ClientCredential, error) {
	return service.ClientCredential{}, domain.ErrForbidden
}
func (remoteMCPIdentity) Revoke(context.Context, domain.Principal, string) error {
	return domain.ErrForbidden
}

// remoteMCPServices authorize like the real services and record who called.
type remoteMCPServices struct {
	principals []string
}

func (s *remoteMCPServices) seen(p domain.Principal) { s.principals = append(s.principals, p.ClientID) }
func (s *remoteMCPServices) Retrieve(_ context.Context, p domain.Principal, r domain.RetrievalRequest) (domain.RetrievalResult, error) {
	s.seen(p)
	if err := service.Authorize(p, "retrieval:read", []string{r.Scope.ProjectID}); err != nil {
		return domain.RetrievalResult{}, err
	}
	return domain.RetrievalResult{SchemaVersion: "3", Chunks: []domain.EvidenceChunk{{RecordID: "c", Revision: "r", Text: "remembered", ClaimStatus: domain.ClaimVerified, OriginTrust: domain.TrustOwnerAuthenticated}}}, nil
}
func (s *remoteMCPServices) FetchEvidence(_ context.Context, p domain.Principal, r domain.FetchRequest) (domain.RetrievalResult, error) {
	s.seen(p)
	return domain.RetrievalResult{}, service.Authorize(p, "evidence:read", []string{r.Scope.ProjectID})
}
func (s *remoteMCPServices) Submit(_ context.Context, p domain.Principal, c domain.CandidateInput) (domain.ClaimRevision, error) {
	s.seen(p)
	if err := service.Authorize(p, "candidate:write", []string{c.Scope.ProjectID}); err != nil {
		return domain.ClaimRevision{}, err
	}
	return domain.ClaimRevision{ClaimID: c.ClaimID, Revision: "rev-1", Scope: c.Scope, Status: domain.ClaimCandidate}, nil
}
func (s *remoteMCPServices) Put(_ context.Context, p domain.Principal, _ string, c domain.Checkpoint) (domain.Checkpoint, error) {
	s.seen(p)
	if err := service.Authorize(p, "checkpoint:write", []string{c.Scope.ProjectID}); err != nil {
		return domain.Checkpoint{}, err
	}
	c.Revision = "cp-1"
	return c, nil
}
func (s *remoteMCPServices) Get(context.Context, domain.Principal, string) (domain.Checkpoint, error) {
	return domain.Checkpoint{}, domain.ErrUnavailable
}
func (s *remoteMCPServices) Status(_ context.Context, p domain.Principal) (domain.Status, error) {
	s.seen(p)
	if err := service.Authorize(p, "status:read", nil); err != nil {
		return domain.Status{}, err
	}
	return domain.Status{MaxRevisionBytes: domain.MaxRevisionBytes, ConnectionState: "available", AuthorizedProjectIDs: p.ProjectIDs, AuthorizedCapabilities: p.Capabilities}, nil
}

func remoteMCPServer(t *testing.T, opts ...ServerOption) (*echo.Echo, *remoteMCPServices) {
	t.Helper()
	services := &remoteMCPServices{}
	e, err := NewServer(remoteMCPIdentity{}, nil, services, services, services, nil, services, nil, slog.New(slog.NewJSONHandler(io.Discard, nil)), opts...)
	require.NoError(t, err)
	return e, services
}

func mcpPost(e http.Handler, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+remoteMCPBearer)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range headers {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// toolPayload returns the JSON payload inside a tools/call text block.
func httpToolPayload(t *testing.T, rec *httptest.ResponseRecorder) (map[string]any, bool) {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var response struct {
		Result toolResult `json:"result"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	require.Len(t, response.Result.Content, 1)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(response.Result.Content[0].Text), &payload))
	return payload, response.Result.IsError
}

func toolCall(name, args string) string {
	return `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`
}

func TestRemoteMCPHandshakeAndToolList(t *testing.T) {
	e, _ := remoteMCPServer(t, WithRemoteMCP())
	rec := mcpPost(e, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"fixture","version":"1"}}}`, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "application/json", strings.Split(rec.Header().Get("Content-Type"), ";")[0])
	assert.Empty(t, rec.Header().Get("Mcp-Session-Id"), "stateless: no session to fixate")
	assert.Contains(t, rec.Body.String(), `"protocolVersion":"2025-06-18"`)
	assert.Contains(t, rec.Body.String(), `"name":"kb-mcp"`)

	rec = mcpPost(e, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, map[string]string{"MCP-Protocol-Version": "2025-06-18"})
	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.Empty(t, rec.Body.String())

	rec = mcpPost(e, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	for _, name := range []string{"memory_orient", "memory_search", "memory_fetch", "memory_submit_candidate", "memory_checkpoint", "memory_status"} {
		assert.Contains(t, rec.Body.String(), `"name":"`+name+`"`)
	}
}

func TestRemoteMCPRunsToolsAsTheAuthenticatedClient(t *testing.T) {
	e, services := remoteMCPServer(t, WithRemoteMCP())
	payload, isError := httpToolPayload(t, mcpPost(e, toolCall("memory_search", `{"project_id":"demo","query":"deploy"}`), nil))
	assert.False(t, isError, payload)
	assert.Contains(t, payload["chunks"].([]any)[0].(map[string]any)["text"], "remembered")

	payload, isError = httpToolPayload(t, mcpPost(e, toolCall("memory_submit_candidate", `{"project_id":"demo","task_id":"t","claim_id":"c","text":"x","kind":"finding"}`), nil))
	assert.False(t, isError, payload)
	assert.Equal(t, "rev-1", payload["revision"])

	payload, isError = httpToolPayload(t, mcpPost(e, toolCall("memory_checkpoint", `{"checkpoint_id":"cp","project_id":"demo","task_id":"t","objective":"resume"}`), nil))
	assert.False(t, isError, payload)

	payload, isError = httpToolPayload(t, mcpPost(e, toolCall("memory_status", `{}`), nil))
	assert.False(t, isError, payload)
	assert.NotContains(t, payload["capability_gaps"], mcpTransportGap, "served over HTTP, so the gap does not apply")

	require.NotEmpty(t, services.principals)
	for _, client := range services.principals {
		assert.Equal(t, "agent-a", client)
	}
}

func TestRemoteMCPEnforcesProjectAndCapabilityScopes(t *testing.T) {
	e, _ := remoteMCPServer(t, WithRemoteMCP())
	for name, args := range map[string]string{
		"memory_search":           `{"project_id":"other","query":"deploy"}`,
		"memory_submit_candidate": `{"project_id":"other","task_id":"t","claim_id":"c","text":"x","kind":"finding"}`,
		"memory_checkpoint":       `{"checkpoint_id":"cp","project_id":"other","task_id":"t","objective":"resume"}`,
		"memory_fetch":            `{"artifact_id":"a","locator":"file","mode":"working","project_id":"demo","task_id":"t"}`, // lacks evidence:read
	} {
		t.Run(name, func(t *testing.T) {
			payload, isError := httpToolPayload(t, mcpPost(e, toolCall(name, args), nil))
			assert.True(t, isError)
			assert.Equal(t, "forbidden", payload["error"].(map[string]any)["code"])
		})
	}
	// A client identity in the arguments is refused, never adopted.
	rec := mcpPost(e, toolCall("memory_submit_candidate", `{"project_id":"demo","task_id":"t","claim_id":"c","text":"x","kind":"finding","client_id":"owner"}`), nil)
	assert.Contains(t, rec.Body.String(), `"code":-32602`)
}

func TestRemoteMCPRefusesUnauthenticatedAndMalformedRequests(t *testing.T) {
	e, services := remoteMCPServer(t, WithRemoteMCP())
	list := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	for _, tc := range []struct {
		name    string
		headers map[string]string
		body    string
		want    int
	}{
		{"no bearer", map[string]string{"Authorization": ""}, list, http.StatusUnauthorized},
		{"wrong bearer", map[string]string{"Authorization": "Bearer nope"}, list, http.StatusUnauthorized},
		{"browser origin", map[string]string{"Origin": "https://attacker.example"}, list, http.StatusForbidden},
		{"form body", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, list, http.StatusUnsupportedMediaType},
		{"html only", map[string]string{"Accept": "text/html"}, list, http.StatusNotAcceptable},
		{"unknown protocol version", map[string]string{"MCP-Protocol-Version": "1999-01-01"}, list, http.StatusBadRequest},
		{"oversized", nil, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"pad":"` + strings.Repeat("x", 70000) + `"}}`, http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := mcpPost(e, tc.body, tc.headers)
			assert.Equal(t, tc.want, rec.Code, rec.Body.String())
		})
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		req := httptest.NewRequest(method, "/mcp", nil)
		req.Header.Set("Authorization", "Bearer "+remoteMCPBearer)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code, method)
		assert.Equal(t, http.MethodPost, rec.Header().Get("Allow"))
	}
	// Parse errors are JSON-RPC errors, and no service ran for any of the above.
	rec := mcpPost(e, `{not json`, nil)
	assert.Contains(t, rec.Body.String(), `"code":-32700`)
	assert.Empty(t, services.principals)
}

func TestRemoteMCPIsOffUnlessEnabled(t *testing.T) {
	e, _ := remoteMCPServer(t)
	rec := mcpPost(e, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil)
	assert.NotEqual(t, http.StatusOK, rec.Code)
}

func TestStdioMCPStillReportsTheTransportGap(t *testing.T) {
	assert.Equal(t, []string{mcpTransportGap}, NewMCPServer(nil, nil, nil, nil).transportGaps)
}
