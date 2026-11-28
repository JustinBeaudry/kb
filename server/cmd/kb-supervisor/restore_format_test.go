package main

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func deployedVersions(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("../../../deploy/versions.json")
	require.NoError(t, err)
	var v map[string]any
	require.NoError(t, json.Unmarshal(raw, &v))
	return v
}

func formatOf(t *testing.T, v map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	d, err := restoreFormatDigest(raw)
	require.NoError(t, err)
	return d
}

func TestRestoreFormatSurvivesGoAndLibraryPatchReleases(t *testing.T) {
	base := formatOf(t, deployedVersions(t))
	for key, value := range map[string]any{
		"go":                       "1.27.5",
		"build_image":              "golang:1.27.5-bookworm@sha256:0000000000000000000000000000000000000000000000000000000000000000",
		"api_worker_runtime_image": "debian:bookworm-slim@sha256:1111111111111111111111111111111111111111111111111111111111111111",
		"echo":                     "github.com/labstack/echo/v4@v4.16.7",
		"encryption":               "filippo.io/age@v1.3.3",
		"linter":                   "github.com/golangci/golangci-lint/v2@v2.16.0",
		"bun":                      "1.4.4",
		"qualified_on":             "2026-12-01",
	} {
		v := deployedVersions(t)
		v[key] = value
		assert.Equal(t, base, formatOf(t, v), key)
	}
	v := deployedVersions(t)
	graph := v["graph"].(map[string]any)
	graph["image"] = "neo4j:2026.10.1-community@sha256:2222222222222222222222222222222222222222222222222222222222222222"
	graph["driver"] = "github.com/neo4j/neo4j-go-driver/v6@v6.4.1"
	assert.Equal(t, base, formatOf(t, v), "a rebuilt image of the same graph version and a driver patch")
}

func TestRestoreFormatChangesWithTheGraphThatWritesTheDump(t *testing.T) {
	base := formatOf(t, deployedVersions(t))
	for key, value := range map[string]string{"version": "2026.11.0", "edition": "enterprise"} {
		v := deployedVersions(t)
		v["graph"].(map[string]any)[key] = value
		assert.NotEqual(t, base, formatOf(t, v), key)
	}
}

func TestRestoreFormatRefusesAManifestWithoutTheGraph(t *testing.T) {
	for _, raw := range []string{``, `{`, `{}`, `{"graph":"2026.10.1"}`, `{"graph":{"edition":"community"}}`, `{"graph":{"version":"2026.10.1"}}`} {
		_, err := restoreFormatDigest([]byte(raw))
		assert.ErrorIs(t, err, domain.ErrInvalidInput, raw)
	}
}

func TestBackupsFromBeforeTheFormatDigestStillRestore(t *testing.T) {
	current := domain.BackupManifest{SchemaDigest: "schema", ToolVersionsDigest: "format", Architecture: "linux/amd64", RetrievalKeyDigest: "new-key"}
	r := &runtimeSnapshot{metadata: current, legacyToolsDigest: "whole-manifest"}
	for _, digest := range []string{"format", "whole-manifest"} {
		old := current
		old.RetrievalKeyDigest = "old-key"
		old.ToolVersionsDigest = digest
		assert.NoError(t, r.ValidateBackup(t.Context(), old), digest)
	}
	for _, digest := range []string{"", "other"} {
		old := current
		old.RetrievalKeyDigest = "old-key"
		old.ToolVersionsDigest = digest
		assert.ErrorIs(t, r.ValidateBackup(t.Context(), old), domain.ErrConflict, digest)
	}
	empty := &runtimeSnapshot{metadata: current}
	old := current
	old.RetrievalKeyDigest = "old-key"
	old.ToolVersionsDigest = ""
	assert.ErrorIs(t, empty.ValidateBackup(t.Context(), old), domain.ErrConflict, "an empty legacy digest never matches an empty backup digest")
}

func TestOnlyTheLegacyFormatAcceptsLegacyDigests(t *testing.T) {
	assert.True(t, acceptsLegacyDigest(backupFormat), "this build writes the legacy format")
	assert.False(t, acceptsLegacyDigest("kb-backup/2"), "a format bump refuses backups that carry only the whole-manifest digest")
}
