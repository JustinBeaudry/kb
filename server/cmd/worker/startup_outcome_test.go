package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/platform"
	"github.com/stretchr/testify/require"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStartupConnectionFailureReleasesAdmission(t *testing.T) {
	control := maintenanceFixture(t, true)
	keyFile := filepath.Join(t.TempDir(), "journal.json")
	require.NoError(t, os.WriteFile(keyFile, []byte(fmt.Sprintf(`{"key":%q}`, base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", 32))))), 0600))
	for k, v := range map[string]string{
		"KB_GRAPH_URI": "bolt://127.0.0.1:1", "KB_GRAPH_DATABASE": "neo4j", "KB_GRAPH_USER": "neo4j", "KB_GRAPH_SECRET": "fixture-only",
		"KB_LISTEN": "127.0.0.1:0", "KB_TLS_CERT": "", "KB_TLS_KEY": "", "KB_RETRIEVAL_HMAC_KEY": strings.Repeat("a", 64),
		"KB_S3_ENDPOINT": "http://127.0.0.1:1", "KB_S3_REGION": "local", "KB_S3_BUCKET": "evidence", "KB_S3_ACCESS_KEY": "fixture", "KB_S3_SECRET_KEY": "fixture-only",
		"KB_JOURNAL_ENDPOINT": "http://127.0.0.1:1", "KB_JOURNAL_REGION": "local", "KB_JOURNAL_BUCKET": "journal", "KB_JOURNAL_PREFIX": "journal", "KB_JOURNAL_NAMESPACE": "fixture", "KB_JOURNAL_ADDRESSING_STYLE": "path", "KB_JOURNAL_ACCESS_KEY": "fixture", "KB_JOURNAL_SECRET_KEY": "fixture-only", "KB_JOURNAL_KEY_ID": "key", "KB_JOURNAL_KEYRING_FILE": keyFile,
	} {
		t.Setenv(k, v)
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	err := run(t.Context(), logger, map[string]platform.RunJob{"verify": func(context.Context, domain.Job, domain.Lease) error { return nil }})
	require.ErrorIs(t, err, domain.ErrUnavailable)
	state, err := control.State(t.Context())
	require.NoError(t, err)
	require.Zero(t, state.Active)
	require.Zero(t, state.Unknown)
	fence, err := control.Begin(t.Context(), "backup")
	require.NoError(t, err)
	require.NoError(t, control.WaitDrained(t.Context(), fence))
}
