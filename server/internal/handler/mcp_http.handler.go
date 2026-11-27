package handler

import (
	"context"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/labstack/echo/v4"
)

// RemoteMCPPath is where the API serves MCP over Streamable HTTP.
const RemoteMCPPath = "/mcp"

// WithRemoteMCP registers MCP over Streamable HTTP at /mcp. It needs the
// retrieval, memory, checkpoint and status services, and is skipped without
// them. Every tool call runs as the principal authenticated from the request's
// bearer, through the same services and authorization checks as the REST API.
func WithRemoteMCP() ServerOption {
	return func(h *IdentityHandler) { h.remoteMCP = true }
}

// principalTools binds the server-side services to the principal that
// Authentication placed on each request's context. Nothing in tool arguments
// can name or change the principal.
type principalTools struct {
	retrieval   RetrievalService
	memory      MemoryService
	checkpoints CheckpointService
	status      StatusService
}

func (t principalTools) Retrieve(ctx context.Context, r domain.RetrievalRequest) (domain.RetrievalResult, error) {
	return t.retrieval.Retrieve(ctx, principal(ctx), r)
}
func (t principalTools) Fetch(ctx context.Context, r domain.FetchRequest) (domain.RetrievalResult, error) {
	return t.retrieval.FetchEvidence(ctx, principal(ctx), r)
}
func (t principalTools) Submit(ctx context.Context, c domain.CandidateInput) (domain.ClaimRevision, error) {
	return t.memory.Submit(ctx, principal(ctx), c)
}
func (t principalTools) PutCheckpoint(ctx context.Context, expected string, c domain.Checkpoint) (domain.Checkpoint, error) {
	return t.checkpoints.Put(ctx, principal(ctx), expected, c)
}
func (t principalTools) Status(ctx context.Context) (domain.Status, error) {
	return t.status.Status(ctx, principal(ctx))
}

// MaxRevisionBytes is known locally: this is the server that issues revisions.
func (principalTools) MaxRevisionBytes(context.Context) int { return domain.MaxRevisionBytes }

// remoteMCP serves one JSON-RPC message per POST and answers with a single JSON
// response, never an event stream. It keeps no session state: the bearer is
// checked on every request, so there is no session ID to steal or fixate.
type remoteMCP struct {
	server *MCPServer
}

func newRemoteMCP(retrieval RetrievalService, memory MemoryService, checkpoints CheckpointService, status StatusService) *remoteMCP {
	tools := principalTools{retrieval: retrieval, memory: memory, checkpoints: checkpoints, status: status}
	return &remoteMCP{server: newMCPServer(tools, tools, tools, tools)}
}

func (m *remoteMCP) register(e *echo.Echo) {
	e.POST(RemoteMCPPath, m.post)
	// No server-initiated stream and no sessions to end.
	e.GET(RemoteMCPPath, mcpMethodNotAllowed)
	e.DELETE(RemoteMCPPath, mcpMethodNotAllowed)
}

func mcpMethodNotAllowed(c echo.Context) error {
	c.Response().Header().Set("Allow", http.MethodPost)
	return c.NoContent(http.StatusMethodNotAllowed)
}

func (m *remoteMCP) post(c echo.Context) error {
	r := c.Request()
	// Browsers attach Origin; MCP clients that hold a bearer do not. Refusing
	// every browser origin closes DNS rebinding and cross-site use of a
	// credential a browser might hold.
	if r.Header.Get("Origin") != "" {
		return problem(c, domain.ErrForbidden)
	}
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		return c.NoContent(http.StatusUnsupportedMediaType)
	}
	if !acceptsJSON(r.Header.Values("Accept")) {
		return c.NoContent(http.StatusNotAcceptable)
	}
	if v := r.Header.Get("MCP-Protocol-Version"); v != "" && !slices.Contains(mcpProtocolVersions, v) {
		return problem(c, domain.ErrInvalidInput)
	}
	// The API's body limit already bounds this read.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	response, ok := m.server.handle(r.Context(), body, len(body) > mcpMaxFrame)
	if !ok {
		// Notifications and client responses get no JSON-RPC reply.
		return c.NoContent(http.StatusAccepted)
	}
	return c.JSONBlob(http.StatusOK, response)
}

// acceptsJSON is lenient: Streamable HTTP clients send application/json and
// text/event-stream, but some send only one, or nothing.
func acceptsJSON(values []string) bool {
	if len(values) == 0 {
		return true
	}
	for _, value := range values {
		for part := range strings.SplitSeq(value, ",") {
			mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(part))
			if err == nil && (mediaType == "application/json" || mediaType == "application/*" || mediaType == "*/*") {
				return true
			}
		}
	}
	return false
}
