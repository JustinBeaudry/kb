package repository_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/handler"
	"github.com/JustinBeaudry/kb/server/internal/repository"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectorRealHTTPKillRestartAndLostAck(t *testing.T) {
	store, jobs, _ := jobsGraph(t)
	objects := captureS3(t)
	warmObjects(t, objects)
	evidence := repository.NewEvidenceRepository(store)
	auth := repository.NewRetrievalRepository(store)
	identity := service.NewIdentityService(store, repository.NewIdentityRepository(store), gateway.NostrIdentity{})
	credential, err := identity.Setup(t.Context(), domain.OwnerIdentity{ID: "owner", PublicKey: "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"}, service.EnrollInput{ClientID: "collector", Role: domain.RoleCollector, ProjectIDs: []string{"demo", "missing"}, Capabilities: []string{"policy:read", "events:append", "artifacts:write"}})
	require.NoError(t, err)
	seedCapturePolicy(t, store, domain.Policy{Revision: "p1", SpoolLimitBytes: 256 << 20})
	capture := service.NewCaptureService(store, evidence, jobs, objects)
	e, err := handler.NewPolicyServer(identity, capture, nil, nil, nil, service.NewPolicyService(store, evidence, auth), slog.New(slog.NewJSONHandler(io.Discard, nil)))
	require.NoError(t, err)
	processed := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/events:append" {
			intercept := false
			once.Do(func() { intercept = true })
			if intercept {
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, r)
				if rec.Code != http.StatusAccepted {
					t.Errorf("first append: status %d", rec.Code)
				}
				close(processed)
				<-release
				for key, values := range rec.Header() {
					for _, value := range values {
						w.Header().Add(key, value)
					}
				}
				w.WriteHeader(rec.Code)
				_, _ = w.Write(rec.Body.Bytes())
				return
			}
		}
		e.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }); server.Close() })
	request := func(path, token string) (int, string) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+path, nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		body, err := io.ReadAll(res.Body)
		require.NoError(t, err)
		require.NoError(t, res.Body.Close())
		return res.StatusCode, string(body)
	}
	for _, tc := range []struct {
		name, path string
		status     int
	}{{"authorized", "/v1/policies/demo", 200}, {"cross project denied", "/v1/policies/private", 403}, {"missing policy closed", "/v1/policies/missing", 403}} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := request(tc.path, credential.Bearer)
			assert.Equal(t, tc.status, status, body)
		})
	}
	path := filepath.Join(t.TempDir(), "spool")
	command := func(action string) *exec.Cmd {
		c := exec.CommandContext(t.Context(), "bun", "../../../tests/fixtures/capture-integration.ts", server.URL, path, action)
		c.Stdin = strings.NewReader(credential.Bearer)
		return c
	}
	first := command("enqueue-flush")
	var diagnostics strings.Builder
	first.Stdout = &diagnostics
	first.Stderr = &diagnostics
	require.NoError(t, first.Start())
	t.Cleanup(func() {
		if first.Process != nil {
			_ = first.Process.Kill()
		}
	})
	select {
	case <-processed:
	case <-time.After(30 * time.Second):
		_ = first.Process.Kill()
		_ = first.Wait()
		t.Fatalf("collector failed before committed lost-ack point: %s", diagnostics.String())
	}
	require.NoError(t, first.Process.Kill())
	require.Error(t, first.Wait())
	releaseOnce.Do(func() { close(release) })
	// The killed collector left its unacknowledged filtered payload on disk.
	queued := spoolBytes(t, path)
	assert.Contains(t, queued, "synthetic result", "positive control: payload is queued")
	assert.NotContains(t, queued, "SYNTHETIC-C1-")
	assert.NotContains(t, queued, credential.Bearer)
	output, err := command("flush").CombinedOutput()
	require.NoError(t, err, string(output))
	assert.Contains(t, string(output), `"sent":1`)
	assert.NotContains(t, string(output), credential.Bearer)
	assert.NotContains(t, string(output), "SYNTHETIC-C1-SECRET")
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		native, err := store.Unwrap(tx)
		if err != nil {
			return err
		}
		result, err := native.Run(ctx, `MATCH (e:Event) RETURN e.data,e.origin`, nil)
		if err != nil {
			return err
		}
		rows, err := result.Collect(ctx)
		if err != nil {
			return err
		}
		require.Len(t, rows, 1)
		var event domain.Event
		require.NoError(t, json.Unmarshal([]byte(rows[0].Values[0].(string)), &event))
		a, _, err := evidence.Artifact(ctx, tx, event.PayloadArtifactID)
		if err != nil {
			return err
		}
		assert.Contains(t, a.SourceLocator, "stable-logical-event")
		assert.Contains(t, a.SourceLocator, "synthetic.jsonl#bytes=13-85")
		assert.Contains(t, a.SourceLocator, "filtered_json_utf8")
		assert.Contains(t, rows[0].Values[1].(string), "submitted")
		object, err := objects.Open(ctx, a.ObjectKey)
		if err != nil {
			return err
		}
		stored, err := io.ReadAll(object)
		if err != nil {
			return err
		}
		if err = object.Close(); err != nil {
			return err
		}
		assert.Contains(t, string(stored), "synthetic result", "positive control: actual stored object")
		assert.NotContains(t, string(stored), "SYNTHETIC-C1-")
		assert.NotContains(t, string(stored), credential.Bearer)
		artifacts, err := native.Run(ctx, `MATCH (a:Artifact) RETURN count(a)`, nil)
		if err != nil {
			return err
		}
		single, err := artifacts.Single(ctx)
		if err != nil {
			return err
		}
		assert.EqualValues(t, 1, single.Values[0], "lost-ack replay reuses one artifact")
		return nil
	}))
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		queued, err := jobs.List(ctx, tx, domain.JobQueued, 100)
		if err != nil {
			return err
		}
		extracts := 0
		for _, j := range queued {
			if j.Kind == "evidence.extract" {
				extracts++
			}
		}
		assert.Equal(t, 1, extracts)
		return nil
	}))
	drained := spoolBytes(t, path)
	assert.NotContains(t, drained, "synthetic result", "acknowledged payload is removed from the spool")
	assert.NotContains(t, drained, "SYNTHETIC-C1-")
	require.NoError(t, identity.Revoke(t.Context(), domain.Principal{OwnerID: "owner", Role: domain.RoleOwner}, credential.ClientID))
	status, body := request("/v1/policies/demo", credential.Bearer)
	assert.Equal(t, 401, status, body)
}

// spoolBytes concatenates every regular file in the collector spool directory.
func spoolBytes(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var all strings.Builder
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		require.NoError(t, err)
		all.Write(data)
	}
	return all.String()
}

// warmObjects is fixture readiness only: a listening port does not mean the
// object store has allocated its first volume. Production collector deadlines
// are unchanged; the fixture waits until a write completes well inside them.
func warmObjects(t *testing.T, objects service.ObjectStore) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		err := objects.Put(ctx, "collector-fixture-ready", strings.NewReader("synthetic"), service.TokenDigest("synthetic"))
		if err == nil {
			err = objects.Delete(ctx, "collector-fixture-ready")
		}
		cancel()
		if err == nil {
			return
		}
		require.True(t, time.Now().Before(deadline), "object store fixture never became ready: %v", err)
		time.Sleep(500 * time.Millisecond)
	}
}
