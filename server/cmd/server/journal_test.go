package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/platform"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// journalFixture uses a real conditional-write backend. Initialization is explicit
// so callers can first prove missing external history refuses startup.
func journalFixture(t *testing.T) *gateway.S3DeletionJournal {
	t.Helper()
	container, err := testcontainers.GenericContainer(t.Context(), testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{
		Image:        "chrislusf/seaweedfs:4.51@sha256:f7a49e3f3972ebed5be2ed4befa4a0c68eb7bdddba6e692d42729ceb545134b8",
		ExposedPorts: []string{"8333/tcp"}, Cmd: []string{"mini", "-dir=/data", "-ip.bind=0.0.0.0", "-master.telemetry=false"},
		Env:        map[string]string{"AWS_ACCESS_KEY_ID": "fixture", "AWS_SECRET_ACCESS_KEY": "fixture-secret-only", "S3_BUCKET": "evidence"},
		WaitingFor: wait.ForListeningPort("8333/tcp").WithStartupTimeout(90 * time.Second),
	}, Started: true})
	if container != nil {
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			require.NoError(t, container.Terminate(ctx))
		})
	}
	require.NoError(t, err)
	host, err := container.Host(t.Context())
	require.NoError(t, err)
	port, err := container.MappedPort(t.Context(), "8333/tcp")
	require.NoError(t, err)
	endpoint := "http://" + host + ":" + port.Port()
	keyfile := filepath.Join(t.TempDir(), "journal-keys.json")
	keys, err := json.Marshal(map[string]string{"test": base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901"))})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(keyfile, keys, 0600))
	for name, value := range map[string]string{
		"KB_S3_ENDPOINT": endpoint, "KB_S3_REGION": "local", "KB_S3_BUCKET": "evidence", "KB_S3_ACCESS_KEY": "fixture", "KB_S3_SECRET_KEY": "fixture-secret-only",
		"KB_JOURNAL_ENDPOINT": endpoint, "KB_JOURNAL_REGION": "local", "KB_JOURNAL_BUCKET": "evidence", "KB_JOURNAL_PREFIX": "journal", "KB_JOURNAL_NAMESPACE": "composition", "KB_JOURNAL_ACCESS_KEY": "fixture", "KB_JOURNAL_SECRET_KEY": "fixture-secret-only", "KB_JOURNAL_ADDRESSING_STYLE": "path", "KB_JOURNAL_KEY_ID": "test", "KB_JOURNAL_KEYRING_FILE": keyfile,
	} {
		t.Setenv(name, value)
	}
	cfg, err := platform.LoadJournalConfig()
	require.NoError(t, err)
	journal, err := gateway.NewS3DeletionJournal(cfg)
	require.NoError(t, err)
	return journal
}
