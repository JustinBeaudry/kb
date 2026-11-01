package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServerClosedStartupServesOnlyLiveness(t *testing.T) {
	maintenanceFixture(t, false)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	keyFile := filepath.Join(t.TempDir(), "journal.json")
	require.NoError(t, os.WriteFile(keyFile, []byte(fmt.Sprintf(`{"key":%q}`, base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", 32))))), 0600))
	for k, v := range map[string]string{
		"KB_GRAPH_URI": "bolt://127.0.0.1:1", "KB_GRAPH_DATABASE": "neo4j", "KB_GRAPH_USER": "neo4j", "KB_GRAPH_SECRET": "fixture-only",
		"KB_LISTEN": address, "KB_TLS_CERT": "", "KB_TLS_KEY": "", "KB_RETRIEVAL_HMAC_KEY": strings.Repeat("a", 64),
		"KB_S3_ENDPOINT": "http://127.0.0.1:1", "KB_S3_REGION": "local", "KB_S3_BUCKET": "evidence", "KB_S3_ACCESS_KEY": "fixture", "KB_S3_SECRET_KEY": "fixture-only",
		"KB_JOURNAL_ENDPOINT": "http://127.0.0.1:1", "KB_JOURNAL_REGION": "local", "KB_JOURNAL_BUCKET": "journal", "KB_JOURNAL_PREFIX": "journal", "KB_JOURNAL_NAMESPACE": "fixture", "KB_JOURNAL_ADDRESSING_STYLE": "path", "KB_JOURNAL_ACCESS_KEY": "fixture", "KB_JOURNAL_SECRET_KEY": "fixture-only", "KB_JOURNAL_KEY_ID": "key", "KB_JOURNAL_KEYRING_FILE": keyFile,
	} {
		t.Setenv(k, v)
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	result := make(chan error, 1)
	go func() { result <- run(ctx, slog.New(slog.NewJSONHandler(io.Discard, nil))) }()
	// Own the transport so unused speculative dials cannot outlive this test.
	// The server correctly gives new connections its header-read grace period.
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: time.Second}
	require.Eventually(t, func() bool {
		r, err := client.Get("http://" + address + "/livez")
		if err != nil {
			return false
		}
		_, readErr := io.Copy(io.Discard, r.Body)
		closeErr := r.Body.Close()
		return readErr == nil && closeErr == nil && r.StatusCode == 200
	}, 5*time.Second, 20*time.Millisecond)
	for _, path := range []string{"/readyz", "/v1/status"} {
		response, err := client.Get("http://" + address + path)
		require.NoError(t, err)
		assert.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
		_, err = io.Copy(io.Discard, response.Body)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
	}
	transport.CloseIdleConnections()
	cancel()
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("startup cancellation did not join")
	}
}
