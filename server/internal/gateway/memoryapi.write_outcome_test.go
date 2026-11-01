package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/handler"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	writeCandidate  = `{"schema_version":"3","claim":{"claim_id":"c","revision":"r1","text":"x","kind":"finding","scope":{"project_id":"demo","task_id":"task"},"evidence":[],"depends_on":[],"applicability":{},"claim_status":"candidate","origin_trust":"submitted"}}`
	writeCheckpoint = `{"schema_version":"3","checkpoint":{"id":"cp","revision":"r2","scope":{"project_id":"demo","task_id":"task"},"objective":"o","completed":[],"pending":[],"questions":[],"evidence_revision_ids":[],"updated_at":"2026-10-30T00:00:00Z"}}`
)

func submitInput() domain.CandidateInput {
	return domain.CandidateInput{ClaimID: "c", Text: "x", Kind: "finding", Scope: domain.Scope{ProjectID: "demo", TaskID: "task"}}
}
func checkpointInput() domain.Checkpoint {
	return domain.Checkpoint{ID: "cp", Scope: domain.Scope{ProjectID: "demo", TaskID: "task"}, Objective: "o"}
}

func TestMemoryAPIStatusReportsAdvertisedRevisionBound(t *testing.T) {
	f := &apiFixture{status: 200, body: `{"schema_version":"3","connection_state":"available","authorized_project_ids":["demo"],"authorized_capabilities":["status:read"],"capability_gaps":[],"max_revision_bytes":64}`}

	status, err := f.serve(t).Status(t.Context())

	require.NoError(t, err)
	assert.Equal(t, 64, status.MaxRevisionBytes)
}

func TestMemoryAPIStatusKeepsLargerFutureBoundReadable(t *testing.T) {
	f := &apiFixture{status: 200, body: `{"schema_version":"3","connection_state":"available","authorized_project_ids":["demo"],"authorized_capabilities":["status:read"],"capability_gaps":[],"max_revision_bytes":128}`}

	status, err := f.serve(t).Status(t.Context())

	require.NoError(t, err)
	assert.Equal(t, 128, status.MaxRevisionBytes)
}

func TestMemoryAPIStatusRejectsInvalidRevisionBound(t *testing.T) {
	for _, bound := range []string{"0", "-1", "1.5", `"64"`, "null"} {
		t.Run(bound, func(t *testing.T) {
			f := &apiFixture{status: 200, body: `{"schema_version":"3","connection_state":"available","authorized_project_ids":["demo"],"authorized_capabilities":["status:read"],"capability_gaps":[],"max_revision_bytes":` + bound + `}`}

			_, err := f.serve(t).Status(t.Context())

			assert.ErrorIs(t, err, domain.ErrUnavailable)
		})
	}
}

// A write whose request may have reached the server cannot be reported as a
// definite failure: it may have committed. Definite rejections stay definite.
func TestMemoryAPIWriteOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       error
	}{
		{"server error after send", `{"code":"unavailable"}`, 503, domain.ErrUnknownOutcome},
		{"gateway timeout", ``, 504, domain.ErrUnknownOutcome},
		{"malformed success receipt", `{"schema_version":"3"}`, 0, domain.ErrUnknownOutcome},
		{"oversized success revision", "", 0, domain.ErrUnknownOutcome},
		{"definite conflict", `{"code":"conflict"}`, 409, domain.ErrConflict},
		{"definite forbidden", `{"code":"forbidden"}`, 403, domain.ErrForbidden},
		{"definite invalid", `{"code":"invalid_input"}`, 400, domain.ErrInvalidInput},
	} {
		for _, op := range []string{"submit", "checkpoint"} {
			t.Run(op+"/"+tc.name, func(t *testing.T) {
				body, status := tc.body, tc.status
				success, receipt := 201, writeCandidate
				if op == "checkpoint" {
					success, receipt = 200, writeCheckpoint
				}
				if status == 0 {
					status = success
				}
				if tc.name == "oversized success revision" {
					long := strings.Repeat("r", domain.MaxRevisionBytes+1)
					body = strings.Replace(strings.Replace(receipt, `"revision":"r1"`, `"revision":"`+long+`"`, 1), `"revision":"r2"`, `"revision":"`+long+`"`, 1)
				}
				api := (&apiFixture{status: status, body: body}).serve(t)

				var err error
				if op == "submit" {
					_, err = api.Submit(t.Context(), submitInput())
				} else {
					_, err = api.PutCheckpoint(t.Context(), "", checkpointInput())
				}

				assert.ErrorIs(t, err, tc.want)
			})
		}
	}
}

func TestMemoryAPIWriteLostResponseIsUnknownOutcome(t *testing.T) {
	// The server reads the full request, then drops the connection without a
	// response: the write may have committed.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		require.True(t, ok)
		conn, _, err := hijacker.Hijack()
		require.NoError(t, err)
		_ = conn.Close()
	}))
	t.Cleanup(server.Close)
	api, err := NewMemoryAPI(server.URL, syntheticBearer, time.Second)
	require.NoError(t, err)

	_, submitErr := api.Submit(t.Context(), submitInput())
	_, checkpointErr := api.PutCheckpoint(t.Context(), "", checkpointInput())

	assert.ErrorIs(t, submitErr, domain.ErrUnknownOutcome)
	assert.ErrorIs(t, checkpointErr, domain.ErrUnknownOutcome)
}

func TestMemoryAPIWriteFailedDialIsDefinitelyUnavailable(t *testing.T) {
	// A failed TCP dial sends no request bytes, so nothing can have committed.
	server := httptest.NewServer(http.NotFoundHandler())
	origin := server.URL
	server.Close()
	api, err := NewMemoryAPI(origin, syntheticBearer, time.Second)
	require.NoError(t, err)

	_, err = api.Submit(t.Context(), submitInput())

	assert.ErrorIs(t, err, domain.ErrUnavailable)
	assert.NotErrorIs(t, err, domain.ErrUnknownOutcome)
}

func TestMemoryAPIMCPWritesWhenServerAdvertisesBound(t *testing.T) {
	for _, tc := range []struct{ name, request, method, path, body, revision string }{
		{"candidate", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_submit_candidate","arguments":{"project_id":"demo","task_id":"task","claim_id":"c","text":"x","kind":"finding"}}}`, "POST", "/v1/candidates", writeCandidate, "r1"},
		{"checkpoint", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_checkpoint","arguments":{"project_id":"demo","task_id":"task","checkpoint_id":"cp","objective":"o"}}}`, "PUT", "/v1/checkpoints/cp", writeCheckpoint, "r2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var writes []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/v1/status" {
					_, _ = w.Write([]byte(`{"schema_version":"3","connection_state":"available","authorized_project_ids":["demo"],"authorized_capabilities":["status:read"],"capability_gaps":[],"max_revision_bytes":64}`))
					return
				}
				writes = append(writes, r.Method+" "+r.URL.Path)
				if r.Method == "POST" {
					w.WriteHeader(http.StatusCreated)
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(server.Close)
			api, err := NewMemoryAPI(server.URL, syntheticBearer, time.Second)
			require.NoError(t, err)
			remote := service.NewRemoteMemoryService(api)
			var output strings.Builder

			err = handler.NewMCPServer(remote, remote, remote, remote).ServeStdio(t.Context(), strings.NewReader(tc.request+"\n"), &output)

			require.NoError(t, err)
			assert.Equal(t, []string{tc.method + " " + tc.path}, writes)
			var frame struct {
				Result struct {
					IsError bool `json:"isError"`
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"result"`
			}
			require.NoError(t, json.Unmarshal([]byte(output.String()), &frame))
			require.False(t, frame.Result.IsError, output.String())
			assert.Contains(t, frame.Result.Content[0].Text, `"revision":"`+tc.revision+`"`)
		})
	}
}
