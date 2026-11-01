package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func supervisorEnv(t *testing.T) (string, string) {
	t.Helper()
	root, e := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, e)
	identity, e := age.GenerateX25519Identity()
	require.NoError(t, e)
	ring := map[string]any{"authority_key_id": "manifest-1", "authority_keys": map[string]string{"manifest-1": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))}, "recipients": map[string]string{"age-1": identity.Recipient().String()}, "identities": map[string]string{"age-1": identity.String()}}
	encoded, e := json.Marshal(ring)
	require.NoError(t, e)
	keyring := filepath.Join(root, "backup-keyring")
	require.NoError(t, os.WriteFile(keyring, encoded, 0600))
	control := filepath.Join(root, "control-key")
	require.NoError(t, os.WriteFile(control, bytes.Repeat([]byte("x"), 32), 0600))
	settings := map[string]string{"KB_SUPERVISOR_ROOT": filepath.Join(root, "data"), "KB_SUPERVISOR_CONTROL_LISTEN": "127.0.0.1:8091", "KB_SUPERVISOR_INSTANCE_ID": "fixture-supervisor", "KB_SUPERVISOR_RESTORE_MODE": "0", "KB_EVIDENCE_NAMESPACE": "", "KB_GRAPH_URI": "bolt://127.0.0.1:7687", "KB_GRAPH_DATABASE": "neo4j", "KB_GRAPH_USER": "neo4j", "KB_GRAPH_SECRET": "fixture-graph-password", "KB_GRAPH_SECRET_FILE": "", "KB_LISTEN": "127.0.0.1:8080", "KB_INGRESS_TRANSPORT": "direct", "KB_TLS_CERT": "", "KB_TLS_KEY": "", "KB_MAINTENANCE_SECRET_FILE": control, "KB_BACKUP_ENDPOINT": "http://127.0.0.1:9000", "KB_BACKUP_REGION": "fixture", "KB_BACKUP_BUCKET": "backup-only", "KB_BACKUP_ACCESS_KEY": "fixture-access", "KB_BACKUP_ACCESS_KEY_FILE": "", "KB_BACKUP_SECRET_KEY": "fixture-secret", "KB_BACKUP_SECRET_KEY_FILE": "", "KB_BACKUP_ADDRESSING_STYLE": "path", "KB_BACKUP_NAMESPACE": "fixture", "KB_BACKUP_KEYRING_FILE": keyring, "KB_BACKUP_MAX_BYTES": "", "KB_RETRIEVAL_HMAC_KEY": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "KB_RETRIEVAL_HMAC_KEY_FILE": ""}
	for k, v := range settings {
		t.Setenv(k, v)
	}
	return root, keyring
}

func TestSupervisorRejectsBackupRoleOverlapBeforeStartingChild(t *testing.T) {
	for _, variant := range []string{"evidence-bucket", "journal-bucket", "journal-authority"} {
		t.Run(variant, func(t *testing.T) {
			root, _ := supervisorEnv(t)
			key := bytes.Repeat([]byte{8}, 32)
			if variant == "journal-authority" {
				key = bytes.Repeat([]byte{7}, 32)
			}
			keyring := filepath.Join(root, "journal-keyring")
			encoded, e := json.Marshal(map[string]string{"journal-1": base64.StdEncoding.EncodeToString(key)})
			require.NoError(t, e)
			require.NoError(t, os.WriteFile(keyring, encoded, 0600))
			for name, value := range map[string]string{"KB_S3_ENDPOINT": "http://127.0.0.1:9000", "KB_S3_REGION": "fixture", "KB_S3_BUCKET": "evidence", "KB_S3_ACCESS_KEY": "evidence-access", "KB_S3_SECRET_KEY": "evidence-secret", "KB_S3_ACCESS_KEY_FILE": "", "KB_S3_SECRET_KEY_FILE": "", "KB_S3_ADDRESSING_STYLE": "path", "KB_JOURNAL_ENDPOINT": "http://127.0.0.1:9000", "KB_JOURNAL_REGION": "fixture", "KB_JOURNAL_BUCKET": "journal", "KB_JOURNAL_PREFIX": "deletions", "KB_JOURNAL_NAMESPACE": "independent-fixture", "KB_JOURNAL_ACCESS_KEY": "journal-access", "KB_JOURNAL_SECRET_KEY": "journal-secret", "KB_JOURNAL_ACCESS_KEY_FILE": "", "KB_JOURNAL_SECRET_KEY_FILE": "", "KB_JOURNAL_ADDRESSING_STYLE": "path", "KB_JOURNAL_KEY_ID": "journal-1", "KB_JOURNAL_KEYRING_FILE": keyring} {
				t.Setenv(name, value)
			}
			if variant == "evidence-bucket" {
				t.Setenv("KB_BACKUP_BUCKET", "evidence")
			}
			if variant == "journal-bucket" {
				t.Setenv("KB_BACKUP_BUCKET", "journal")
			}
			require.ErrorIs(t, serveSupervisor(t.Context()), domain.ErrInvalidInput)
			assert.NoDirExists(t, filepath.Join(root, "data", "graph"))
		})
	}
}
func TestSupervisorCLIRejectsUnknownFlagsBeforeAnyIO(t *testing.T) {
	require.ErrorIs(t, run(context.Background(), []string{"serve", "unexpected"}, io.Discard), domain.ErrInvalidInput)
	require.ErrorIs(t, run(context.Background(), []string{"backup", "--shell-command", "bad"}, io.Discard), domain.ErrInvalidInput)
}
func TestSupervisorConfigLoadsExplicitProtectedBackupAuthority(t *testing.T) {
	_, _ = supervisorEnv(t)
	c, e := loadSupervisorConfig()
	require.NoError(t, e)
	assert.EqualValues(t, 4<<30, c.Archive.MaxArchiveBytes)
	assert.Equal(t, "manifest-1", c.Archive.AuthorityKeyID)
	assert.Len(t, c.Archive.AuthorityKeys, 1)
	assert.Len(t, c.Archive.Identities, 1)
	assert.Equal(t, "fixture-supervisor", c.InstanceID)
}
func TestSupervisorConfigRefusesUnsafeOrAmbiguousCustody(t *testing.T) {
	for _, variant := range []string{"public-keyring", "invalid-key", "unknown-field", "trailing-json", "bad-capacity", "bad-listen", "bad-root", "mixed-credential", "restore-mode"} {
		t.Run(variant, func(t *testing.T) {
			_, path := supervisorEnv(t)
			switch variant {
			case "public-keyring":
				require.NoError(t, os.Chmod(path, 0644))
			case "invalid-key":
				require.NoError(t, os.WriteFile(path, []byte(`{"authority_keys":{"a":"bad"}}`), 0600))
			case "unknown-field":
				require.NoError(t, os.WriteFile(path, []byte(`{"unsafe":"input"}`), 0600))
			case "trailing-json":
				require.NoError(t, os.WriteFile(path, []byte(`{} {}`), 0600))
			case "bad-capacity":
				t.Setenv("KB_BACKUP_MAX_BYTES", "not-a-number")
			case "bad-listen":
				t.Setenv("KB_SUPERVISOR_CONTROL_LISTEN", "bad")
			case "bad-root":
				t.Setenv("KB_SUPERVISOR_ROOT", "relative")
			case "mixed-credential":
				t.Setenv("KB_BACKUP_SECRET_KEY_FILE", path)
			case "restore-mode":
				t.Setenv("KB_SUPERVISOR_RESTORE_MODE", "unexpected")
			}
			_, e := loadSupervisorConfig()
			require.Error(t, e)
			assert.NotContains(t, e.Error(), "fixture-secret")
		})
	}
}
func TestHistoricalSupervisorRootCannotBecomeFreshByMissingState(t *testing.T) {
	root, e := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, e)
	require.NoError(t, os.Chmod(root, 0700))
	assert.True(t, freshSupervisorRoot(root))
	require.NoError(t, os.Mkdir(filepath.Join(root, "control"), 0700))
	assert.True(t, freshSupervisorRoot(root))
	require.NoError(t, os.WriteFile(filepath.Join(root, "control", "old-operation"), []byte("opaque"), 0600))
	assert.False(t, freshSupervisorRoot(root))
	require.NoError(t, os.Remove(filepath.Join(root, "control", "old-operation")))
	require.NoError(t, os.WriteFile(filepath.Join(root, "backup.json"), []byte("historical"), 0600))
	assert.False(t, freshSupervisorRoot(root))
}
func TestSupervisorPrivateDirectoryRejectsPublicModeAndSymlink(t *testing.T) {
	root, e := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, e)
	path := filepath.Join(root, "private")
	require.NoError(t, supervisorPrivateDir(path))
	require.NoError(t, os.Chmod(path, 0755))
	require.ErrorIs(t, supervisorPrivateDir(path), domain.ErrForbidden)
	require.NoError(t, os.Chmod(path, 0700))
	link := filepath.Join(root, "alias")
	require.NoError(t, os.Symlink(path, link))
	require.ErrorIs(t, supervisorPrivateDir(link), domain.ErrForbidden)
}
func TestRestoreRequiresRotatedCursorAuthorityAndExactToolContract(t *testing.T) {
	current := domain.BackupManifest{SchemaDigest: "schema", ToolVersionsDigest: "tools", Architecture: "linux/amd64", RetrievalKeyDigest: "new-key"}
	r := &runtimeSnapshot{metadata: current}
	old := current
	old.RetrievalKeyDigest = "old-key"
	require.NoError(t, r.ValidateBackup(t.Context(), old))
	require.ErrorIs(t, r.ValidateBackup(t.Context(), current), domain.ErrConflict)
	old.ToolVersionsDigest = "different-tools"
	require.ErrorIs(t, r.ValidateBackup(t.Context(), old), domain.ErrConflict)
}
func TestRuntimeManifestUsesConfiguredAuthorityWithoutLoggingKeys(t *testing.T) {
	root, _ := supervisorEnv(t)
	tools := filepath.Join(root, "versions.json")
	require.NoError(t, os.WriteFile(tools, []byte(`{"graph":"fixture-version"}`), 0600))
	t.Setenv("KB_TOOL_VERSIONS_FILE", tools)
	c, e := loadSupervisorConfig()
	require.NoError(t, e)
	c.EvidenceNamespace = "actual-bucket-digest"
	r, e := newRuntimeSnapshot(c, nil, nil, nil)
	require.NoError(t, e)
	assert.Len(t, r.metadata.RetrievalKeyDigest, 64)
	assert.Equal(t, c.EvidenceNamespace, r.metadata.EvidenceNamespace)
	assert.NotEmpty(t, r.metadata.SchemaDigest)
	assert.NotContains(t, r.metadata.RetrievalKeyDigest, os.Getenv("KB_RETRIEVAL_HMAC_KEY"))
}
