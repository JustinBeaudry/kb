//go:build acceptance

package acceptance

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBackupReadabilityWaitsForPersistedBodiesWithoutMutation(t *testing.T) {
	var phase, mutations atomic.Int32
	unavailable := make(chan struct{}, 1)
	partial := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mutations.Add(1)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		switch r.URL.Path {
		case "/backups/backups/fixture-backups/completed/complete.json":
			if phase.Load() == 0 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte("<Error><Code>ServiceUnavailable</Code></Error>"))
				select {
				case unavailable <- struct{}{}:
				default:
				}
				return
			}
			_, _ = w.Write([]byte("synthetic completion body"))
		case "/backups/backups/fixture-backups/completed/archive.age":
			if phase.Load() == 1 {
				w.Header().Set("Content-Length", "64")
				_, _ = w.Write([]byte("short"))
				select {
				case partial <- struct{}{}:
				default:
				}
				return
			}
			_, _ = w.Write([]byte("synthetic encrypted archive body"))
		default:
			t.Errorf("unexpected dependency path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client := backupReadabilityClient(server.URL, server.Client())
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() {
		done <- waitForBackupReadability(ctx, client, "backups", "fixture-backups", "completed", 1024, 5*time.Millisecond)
	}()
	await := func(reached <-chan struct{}) {
		t.Helper()
		select {
		case <-reached:
		case err := <-done:
			t.Fatalf("dependency gate passed before complete bodies: %v", err)
		case <-ctx.Done():
			t.Fatal("dependency boundary was not reached")
		}
		select {
		case err := <-done:
			t.Fatalf("dependency gate passed before complete bodies: %v", err)
		default:
		}
	}
	await(unavailable)
	phase.Store(1)
	await(partial)
	// A second request proves the first incomplete body was rejected and joined
	// before readiness is allowed to change; an HTTP-header-only gate returns.
	await(partial)
	phase.Store(2)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("readable dependency bodies never opened the gate")
	}
	assert.Zero(t, mutations.Load())
}

func TestBackupReadabilityFailsClosedForUnavailableOrIncompleteObjects(t *testing.T) {
	for _, name := range []string{"missing completion", "missing archive", "truncated completion", "truncated archive", "empty completion", "empty archive", "oversized completion", "oversized archive"} {
		t.Run(name, func(t *testing.T) {
			kind, target, _ := strings.Cut(name, " ")
			var mutations atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					mutations.Add(1)
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				completion := r.URL.Path == "/backups/backups/fixture-backups/completed/complete.json"
				broken := completion == (target == "completion")
				if broken {
					if kind == "missing" {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					if kind == "empty" {
						w.Header().Set("Content-Length", "0")
						return
					}
					if kind == "truncated" {
						w.Header().Set("Content-Length", "64")
						_, _ = w.Write([]byte("short"))
						return
					}
					w.Header().Set("Content-Length", "20000")
					_, _ = fmt.Fprint(w, "oversized declared body")
					return
				}
				_, _ = w.Write([]byte("readable synthetic dependency"))
			}))
			t.Cleanup(server.Close)
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Millisecond)
			t.Cleanup(cancel)
			err := waitForBackupReadability(ctx, backupReadabilityClient(server.URL, server.Client()), "backups", "fixture-backups", "completed", 1024, time.Millisecond)
			assert.ErrorIs(t, err, context.DeadlineExceeded)
			assert.Zero(t, mutations.Load())
		})
	}
}

func backupReadabilityClient(endpoint string, httpClient *http.Client) *s3.Client {
	return s3.New(s3.Options{BaseEndpoint: aws.String(endpoint), Region: "us-east-1", UsePathStyle: true, Credentials: credentials.NewStaticCredentialsProvider("fixture", "fixture-secret-only", ""), HTTPClient: httpClient, Retryer: aws.NopRetryer{}})
}
