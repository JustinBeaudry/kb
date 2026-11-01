package gateway

import (
	"bytes"
	"context"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBackupS3UsesImmutableWritesAndSeparatePhysicalRetention(t *testing.T) {
	var stored []byte
	conditional, deleted := false, false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "PUT":
			conditional = r.Header.Get("If-None-Match") == "*"
			stored, _ = io.ReadAll(r.Body)
		case "GET":
			_, _ = w.Write(stored)
		case "DELETE":
			deleted = true
			w.WriteHeader(204)
		}
	}))
	t.Cleanup(srv.Close)
	g, e := NewS3BackupStore(S3Config{Endpoint: srv.URL, Region: "fixture", Bucket: "backup-only", AccessKey: "fixture-access", SecretKey: "fixture-secret"})
	require.NoError(t, e)
	data := []byte("encrypted-payload")
	require.NoError(t, g.PutImmutable(t.Context(), "backups/fixture/id/archive.age", bytes.NewReader(data), int64(len(data)), backupDigest(data)))
	assert.True(t, conditional)
	body, e := g.Open(t.Context(), "backups/fixture/id/archive.age")
	require.NoError(t, e)
	v, e := io.ReadAll(body)
	require.NoError(t, e)
	require.NoError(t, body.Close())
	assert.Equal(t, data, v)
	require.NoError(t, g.Delete(t.Context(), "backups/fixture/id/archive.age"))
	assert.True(t, deleted)
}

func TestBackupS3UnknownWriteAcknowledgementIsStickyAndNotRetried(t *testing.T) {
	var puts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			puts.Add(1)
			w.WriteHeader(503)
			return
		}
		_, _ = io.WriteString(w, "payload")
	}))
	t.Cleanup(srv.Close)
	g, err := NewS3BackupStore(S3Config{Endpoint: srv.URL, Region: "fixture", Bucket: "backup-only", AccessKey: "fixture-access", SecretKey: "fixture-secret"})
	require.NoError(t, err)
	ctx := domain.WithOperationOutcome(t.Context())
	err = g.PutImmutable(ctx, "backups/fixture/id/archive.age", bytes.NewReader([]byte("payload")), 7, backupDigest([]byte("payload")))
	require.Error(t, err)
	assert.EqualValues(t, 1, puts.Load())
	assert.False(t, domain.OperationSettled(ctx))
	body, err := g.Open(ctx, "backups/fixture/id/archive.age")
	require.NoError(t, err)
	_, err = io.ReadAll(body)
	require.NoError(t, err)
	require.NoError(t, body.Close())
	assert.False(t, domain.OperationSettled(ctx))
}

func TestBackupS3ImmutableConflictRequiresVerifiedCiphertext(t *testing.T) {
	payload := []byte("encrypted-payload")
	for _, tc := range []struct {
		name        string
		stored      []byte
		getStatus   int
		interrupted bool
		want        error
	}{
		{name: "identical ciphertext", stored: payload},
		{name: "changed ciphertext", stored: []byte("encrypted-corrupt"), want: domain.ErrConflict},
		{name: "short ciphertext", stored: payload[:len(payload)-1], want: domain.ErrConflict},
		{name: "extended ciphertext", stored: append(append([]byte(nil), payload...), 'x'), want: domain.ErrConflict},
		{name: "incomplete transport", stored: payload[:len(payload)-1], interrupted: true, want: domain.ErrUnavailable},
		{name: "readback denied", getStatus: http.StatusForbidden, want: domain.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var puts, gets atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/backup-only/backups/fixture/id/archive.age", r.URL.Path)
				switch r.Method {
				case http.MethodPut:
					puts.Add(1)
					assert.Equal(t, "*", r.Header.Get("If-None-Match"))
					body, err := io.ReadAll(r.Body)
					assert.NoError(t, err)
					assert.Equal(t, payload, body)
					w.WriteHeader(http.StatusPreconditionFailed)
					_, _ = io.WriteString(w, `<Error><Code>PreconditionFailed</Code></Error>`)
				case http.MethodGet:
					gets.Add(1)
					if tc.getStatus != 0 {
						w.WriteHeader(tc.getStatus)
						_, _ = io.WriteString(w, `<Error><Code>AccessDenied</Code></Error>`)
						return
					}
					if tc.interrupted {
						w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
					}
					_, _ = w.Write(tc.stored)
				default:
					t.Errorf("unexpected method %s", r.Method)
					w.WriteHeader(http.StatusMethodNotAllowed)
				}
			}))
			t.Cleanup(srv.Close)
			store, err := NewS3BackupStore(S3Config{Endpoint: srv.URL, Region: "fixture", Bucket: "backup-only", AccessKey: "fixture-access", SecretKey: "fixture-secret"})
			require.NoError(t, err)
			ctx := domain.WithOperationOutcome(t.Context())

			err = store.PutImmutable(ctx, "backups/fixture/id/archive.age", bytes.NewReader(payload), int64(len(payload)), backupDigest(payload))

			require.ErrorIs(t, err, tc.want)
			assert.EqualValues(t, 1, puts.Load(), "conditional rejection must not repeat the mutation")
			assert.EqualValues(t, 1, gets.Load(), "an existing object must be verified")
			assert.True(t, domain.OperationSettled(ctx), "known conditional rejection and read-only verification do not create mutation uncertainty")
		})
	}
}

func TestBackupS3ImmutableConflictWaitsForCompleteReadback(t *testing.T) {
	payload := []byte("encrypted-payload")
	readStarted, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusPreconditionFailed)
			_, _ = io.WriteString(w, `<Error><Code>PreconditionFailed</Code></Error>`)
			return
		}
		assert.Equal(t, http.MethodGet, r.Method)
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload[:3])
		w.(http.Flusher).Flush()
		close(readStarted)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write(payload[3:])
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(domain.WithOperationOutcome(t.Context()), 5*time.Second)
	t.Cleanup(cancel)
	store, err := NewS3BackupStore(S3Config{Endpoint: srv.URL, Region: "fixture", Bucket: "backup-only", AccessKey: "fixture-access", SecretKey: "fixture-secret"})
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		done <- store.PutImmutable(ctx, "backups/fixture/id/archive.age", bytes.NewReader(payload), int64(len(payload)), backupDigest(payload))
	}()
	select {
	case <-readStarted:
	case <-ctx.Done():
		t.Fatal("readback was not reached")
	}
	select {
	case earlyErr := <-done:
		assert.NoError(t, earlyErr)
		t.Fatal("immutable retry returned before verifying the full object")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case err = <-done:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("readback did not finish")
	}
	assert.True(t, domain.OperationSettled(ctx))
}

func TestBackupS3LostDeleteAcknowledgementRemainsUncertainAfterRetry(t *testing.T) {
	var deletes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		if deletes.Add(1) == 1 {
			// The provider applies retention, then the transport loses its reply.
			connection, _, err := w.(http.Hijacker).Hijack()
			if !assert.NoError(t, err) {
				return
			}
			_ = connection.Close()
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	store, err := NewS3BackupStore(S3Config{Endpoint: srv.URL, Region: "fixture", Bucket: "backup-only", AccessKey: "fixture-access", SecretKey: "fixture-secret"})
	require.NoError(t, err)
	ctx := domain.WithOperationOutcome(t.Context())

	err = store.Delete(ctx, "backups/fixture/id/archive.age")

	require.ErrorIs(t, err, domain.ErrUnavailable)
	assert.EqualValues(t, 1, deletes.Load(), "SDK must not hide an unknown physical attempt with a retry")
	assert.False(t, domain.OperationSettled(ctx))
	require.NoError(t, store.Delete(ctx, "backups/fixture/id/archive.age"))
	assert.EqualValues(t, 2, deletes.Load())
	assert.False(t, domain.OperationSettled(ctx), "a later successful delete must not clear earlier uncertainty")
}
