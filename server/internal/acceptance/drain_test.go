//go:build acceptance

package acceptance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/handler"
	"github.com/stretchr/testify/require"
)

type proxyState struct {
	Reached, Completed bool
	MatchedCount       int `json:"matched_count"`
	UpstreamStatus     int `json:"upstream_status"`
}
type asyncResult struct {
	Body   []byte
	Status int
	Err    error
}

func (f *compiledFixture) proxy(method, path string, input any) proxyState {
	f.t.Helper()
	raw, err := json.Marshal(input)
	require.NoError(f.t, err)
	req, err := http.NewRequestWithContext(f.ctx, method, f.runtime["proxy_url"]+path, bytes.NewReader(raw))
	require.NoError(f.t, err)
	req.Header.Set("Content-Type", "application/json")
	response, err := f.client.Do(req)
	require.NoError(f.t, err)
	defer func() { _ = response.Body.Close() }()
	require.Equal(f.t, http.StatusOK, response.StatusCode)
	var state proxyState
	require.NoError(f.t, json.NewDecoder(response.Body).Decode(&state))
	return state
}
func (f *compiledFixture) awaitHeld() {
	f.t.Helper()
	require.Eventually(f.t, func() bool { return f.proxy(http.MethodGet, "/state", nil).Reached }, 30*time.Second, 100*time.Millisecond)
}
func (f *compiledFixture) startHTTP(token, path string, input any) <-chan asyncResult {
	f.t.Helper()
	raw, err := json.Marshal(input)
	require.NoError(f.t, err)
	req, err := http.NewRequestWithContext(f.ctx, http.MethodPost, f.runtime["api_url"]+path, bytes.NewReader(raw))
	require.NoError(f.t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	done := make(chan asyncResult, 1)
	go func() {
		response, err := f.client.Do(req)
		if err != nil {
			done <- asyncResult{Err: err}
			return
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		closeErr := response.Body.Close()
		if err == nil {
			err = closeErr
		}
		done <- asyncResult{Body: body, Status: response.StatusCode, Err: err}
	}()
	return done
}
func (f *compiledFixture) startAdmin(input any, args ...string) <-chan asyncResult {
	f.t.Helper()
	raw, err := json.Marshal(input)
	require.NoError(f.t, err)
	command := append([]string{"exec", "-T", "graph", "/usr/local/bin/kb-runtime-entrypoint", "/usr/local/bin/kb-admin"}, args...)
	done := make(chan asyncResult, 1)
	go func() { body, err := f.composeTry(raw, command...); done <- asyncResult{Body: body, Err: err} }()
	return done
}
func (f *compiledFixture) finishAsync(done <-chan asyncResult) asyncResult {
	f.t.Helper()
	select {
	case result := <-done:
		require.NoError(f.t, result.Err)
		return result
	case <-time.After(5 * time.Minute):
		f.t.Fatal("compiled operation did not settle")
		return asyncResult{}
	}
}
func (f *compiledFixture) uploadInput(id string) handler.ArtifactInput {
	text := "Synthetic held I/O evidence " + id
	sum := sha256.Sum256([]byte(text))
	task, session, path := "drain-"+id, "drain-session-"+id, "fixture.txt"
	return handler.ArtifactInput{Bytes: int64(len(text)), ContentBase64: []byte(text), Digest: hex.EncodeToString(sum[:]), PolicyRevision: "fixture-policy-1", Scope: handler.Scope{ProjectId: "fixture-project", TaskId: &task, SessionId: &session}, SourceLocator: id + "/source", SourcePath: &path}
}
func (f *compiledFixture) drainHeld(agent, id string) {
	f.t.Helper()
	done := make(chan asyncResult, 1)
	go func() {
		body, err := f.composeTry(nil, "exec", "-T", "graph", "/usr/local/bin/kb-runtime-entrypoint", "/usr/local/bin/kb-supervisor", "backup", "--id", id)
		done <- asyncResult{Body: body, Err: err}
	}()
	require.Eventually(f.t, func() bool {
		state := f.operator("status").Status.Maintenance
		return state.Phase == "draining" && state.Active > 0
	}, 20*time.Second, 200*time.Millisecond)
	// Repeated observation proves an active caller cannot be mistaken for drained.
	for i := 0; i < 3; i++ {
		state := f.operator("status").Status.Maintenance
		require.Equal(f.t, "draining", state.Phase)
		require.Positive(f.t, state.Active)
		status, _ := f.http(agent, http.MethodGet, "/v1/status", nil)
		require.Equal(f.t, http.StatusServiceUnavailable, status)
		status, _ = f.http("", http.MethodGet, "/livez", nil)
		require.Equal(f.t, http.StatusOK, status)
		select {
		case <-done:
			f.t.Fatal("backup completed while a physical storage call was held")
		default:
		}
		time.Sleep(200 * time.Millisecond)
	}
	f.proxy(http.MethodPost, "/release", map[string]string{"id": id})
	result := f.finishAsync(done)
	var backup operatorResult
	require.NoError(f.t, json.Unmarshal(result.Body, &backup))
	require.Equal(f.t, "ok", backup.Code)
	f.waitReady()
	require.Eventually(f.t, func() bool { return f.proxy(http.MethodGet, "/state", nil).Completed }, 10*time.Second, 100*time.Millisecond)
}
func (f *compiledFixture) exerciseDrain(agent, collector string) {
	f.t.Helper()
	id := "held-api-upload"
	f.proxy(http.MethodPost, "/arm", map[string]string{"id": id, "method": "PUT", "path_prefix": "/evidence/evidence/", "mode": "hold"})
	upload := f.startHTTP(collector, "/v1/artifacts", f.uploadInput(id))
	f.awaitHeld()
	f.drainHeld(agent, id)
	result := f.finishAsync(upload)
	require.Equal(f.t, http.StatusCreated, result.Status)
	for _, kind := range []string{"journal", "purge"} {
		id = "held-" + kind
		input := f.uploadInput(id)
		status, raw := f.http(collector, http.MethodPost, "/v1/artifacts", input)
		require.Equal(f.t, http.StatusCreated, status)
		var artifact handler.ArtifactResult
		require.NoError(f.t, json.Unmarshal(raw, &artifact))
		status, _ = f.http(collector, http.MethodPost, "/v1/artifacts/"+artifact.Artifact.Id+":complete", handler.CompleteArtifactInput{Digest: input.Digest})
		require.Equal(f.t, http.StatusOK, status)
		selection := domain.DeleteSelection{ProjectID: "fixture-project", IDs: []string{artifact.Artifact.Id}, IdempotencyKey: id}
		var preview domain.DeletionPreview
		f.admin(&preview, selection, "delete", "--preview")
		selection.ExpectedSelectionDigest = preview.SelectionDigest
		rule := map[string]string{"id": id, "method": "PUT", "mode": "hold"}
		if kind == "journal" {
			rule["path_prefix"] = "/journal/"
			rule["path_suffix"] = "/head"
		} else {
			rule["path_prefix"] = "/evidence/evidence/"
			rule["header_name"] = "x-amz-meta-kb-deleted"
			rule["header_value"] = "1"
		}
		f.proxy(http.MethodPost, "/arm", rule)
		deletion := f.startAdmin(selection, "delete")
		f.awaitHeld()
		f.drainHeld(agent, id)
		receiptRaw := f.finishAsync(deletion)
		var receipt domain.DeletionReceipt
		require.NoError(f.t, json.Unmarshal(receiptRaw.Body, &receipt))
		require.Eventually(f.t, func() bool {
			var current domain.DeletionReceipt
			f.admin(&current, map[string]string{"ID": receipt.ID}, "deletion-status")
			return current.State == "purge_complete"
		}, 2*time.Minute, time.Second)
	}
}

func (f *compiledFixture) exerciseLostAcknowledgement(collector string) {
	f.t.Helper()
	id := "lost-upload-ack"
	f.proxy(http.MethodPost, "/arm", map[string]string{"id": id, "method": "PUT", "path_prefix": "/restored-evidence/evidence/", "mode": "drop_ack"})
	result := f.finishAsync(f.startHTTP(collector, "/v1/artifacts", f.uploadInput(id)))
	require.Equal(f.t, http.StatusServiceUnavailable, result.Status)
	require.Eventually(f.t, func() bool { return f.proxy(http.MethodGet, "/state", nil).Completed }, 20*time.Second, 100*time.Millisecond)
	observed := f.proxy(http.MethodGet, "/state", nil)
	require.True(f.t, observed.Reached)
	require.True(f.t, observed.Completed)
	require.Equal(f.t, 1, observed.MatchedCount)
	require.GreaterOrEqual(f.t, observed.UpstreamStatus, 200)
	require.Less(f.t, observed.UpstreamStatus, 300)
	require.Positive(f.t, f.operator("status").Status.Maintenance.Active)
	f.compose(nil, "stop", "api", "worker")
	f.compose(nil, "restart", "graph")
	require.Eventually(f.t, func() bool {
		raw, err := f.composeTry(nil, "exec", "-T", "graph", "/usr/local/bin/kb-runtime-entrypoint", "/usr/local/bin/kb-supervisor", "status")
		if err != nil {
			return false
		}
		var state operatorResult
		return json.Unmarshal(raw, &state) == nil && state.Code == "ok" && state.Status.Maintenance.Unknown > 0
	}, 3*time.Minute, time.Second)
	// The known unresolved operation must refuse backup without reaching offline.
	raw, err := f.composeTry(nil, "exec", "-T", "graph", "/usr/local/bin/kb-runtime-entrypoint", "/usr/local/bin/kb-supervisor", "backup", "--id", "blocked-by-unknown")
	require.Error(f.t, err)
	var refused operatorResult
	require.NoError(f.t, json.Unmarshal(raw, &refused))
	require.NotEqual(f.t, "ok", refused.Code)
	state := f.operator("status").Status.Maintenance
	require.Positive(f.t, state.Unknown)
	require.NotEqual(f.t, "offline", state.Phase)
	require.NotEqual(f.t, "open", state.Phase)
}
