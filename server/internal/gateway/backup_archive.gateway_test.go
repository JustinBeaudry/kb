package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type archiveMemory struct {
	data map[string][]byte
	fail string
}

func (m *archiveMemory) PutImmutable(_ context.Context, k string, r io.ReadSeeker, _ int64, _ string) error {
	if m.fail == k {
		return domain.ErrUnavailable
	}
	b, e := io.ReadAll(r)
	if e != nil {
		return e
	}
	if old, ok := m.data[k]; ok && !bytes.Equal(old, b) {
		return domain.ErrConflict
	}
	m.data[k] = b
	return nil
}
func (m *archiveMemory) Open(_ context.Context, k string) (io.ReadCloser, error) {
	b, ok := m.data[k]
	if !ok {
		return nil, domain.ErrIncomplete
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}
func (m *archiveMemory) List(_ context.Context, prefix string) ([]string, error) {
	keys := []string{}
	for k := range m.data {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			keys = append(keys, k)
		}
	}
	return keys, nil
}
func (m *archiveMemory) Delete(_ context.Context, k string) error { delete(m.data, k); return nil }
func archiveHash(v []byte) string                                 { s := sha256.Sum256(v); return hex.EncodeToString(s[:]) }
func archiveSetup(t *testing.T) (*BackupArchiveGateway, *archiveMemory, BackupArchiveConfig, domain.BackupManifest, string, string) {
	t.Helper()
	id, e := age.GenerateX25519Identity()
	require.NoError(t, e)
	var key [32]byte
	copy(key[:], bytes.Repeat([]byte{9}, 32))
	scratchParent, e := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, e)
	c := BackupArchiveConfig{Namespace: "fixture", ScratchRoot: filepath.Join(scratchParent, "scratch"), AuthorityKeyID: "manifest-1", AuthorityKeys: map[string][32]byte{"manifest-1": key}, Recipients: map[string]string{"old": id.Recipient().String()}, Identities: map[string]string{"old": id.String()}, MaxArchiveBytes: 32 << 20}
	store := &archiveMemory{data: map[string][]byte{"source-generation": []byte("pinned-evidence")}}
	a, e := NewBackupArchive(store, store, c)
	require.NoError(t, e)
	dir := t.TempDir()
	data, system := filepath.Join(dir, "neo4j.dump"), filepath.Join(dir, "system.dump")
	require.NoError(t, os.WriteFile(data, []byte("data-dump"), 0600))
	require.NoError(t, os.WriteFile(system, []byte("system-dump"), 0600))
	m := domain.BackupManifest{Version: 1, BackupID: "backup-1", EvidenceNamespace: "source-bucket", SchemaDigest: archiveHash([]byte("schema")), ToolVersionsDigest: archiveHash([]byte("tools")), Architecture: "linux/arm64", CreatedAt: time.Now().UTC(), Journal: domain.JournalBarrier{Namespace: "independent", Sequence: 4, Digest: archiveHash([]byte("head"))}, Objects: []domain.BackupObject{{ArtifactID: "pinned", ObjectKey: "source-generation", Generation: "generation", Digest: archiveHash([]byte("pinned-evidence")), Bytes: 15, Available: true, State: "ready"}}}
	return a, store, c, m, data, system
}
func TestBackupArchiveAuthenticatedRoundTrip(t *testing.T) {
	a, store, _, m, data, system := archiveSetup(t)
	require.NoError(t, a.Write(t.Context(), m, data, system))
	verified, e := a.Verify(t.Context(), m.BackupID)
	require.NoError(t, e)
	assert.True(t, verified.Completed)
	assert.NotEmpty(t, verified.SystemDumpDigest)
	target := filepath.Join(a.config.ScratchRoot, "restore-fixture")
	restored, e := a.Restore(t.Context(), m.BackupID, target)
	require.NoError(t, e)
	assert.Equal(t, verified, restored)
	body, e := a.OpenRestoreObject(t.Context(), m.Objects[0])
	require.NoError(t, e)
	got, e := io.ReadAll(body)
	require.NoError(t, e)
	require.NoError(t, body.Close())
	assert.Equal(t, "pinned-evidence", string(got))
	for k, v := range store.data {
		if k == "source-generation" {
			continue
		}
		assert.NotContains(t, string(v), "pinned-evidence")
		assert.NotContains(t, string(v), "source-generation")
	}
}
func TestBackupArchiveRejectsCorruptionAndTruncation(t *testing.T) {
	for _, mutation := range []string{"truncate", "tamper", "completion", "wrong-authority"} {
		t.Run(mutation, func(t *testing.T) {
			a, s, c, m, data, system := archiveSetup(t)
			require.NoError(t, a.Write(t.Context(), m, data, system))
			for k, v := range s.data {
				if filepath.Base(k) == "archive.age" {
					if mutation == "truncate" {
						s.data[k] = v[:len(v)-1]
					}
					if mutation == "tamper" {
						v[len(v)-1] ^= 1
						s.data[k] = v
					}
				}
				if filepath.Base(k) == "complete.json" && mutation == "completion" {
					v[len(v)/2] ^= 1
					s.data[k] = v
				}
			}
			if mutation == "wrong-authority" {
				c.AuthorityKeys = map[string][32]byte{"manifest-1": {1}}
				var e error
				a, e = NewBackupArchive(s, s, c)
				require.NoError(t, e)
			}
			target := filepath.Join(a.config.ScratchRoot, "restore-fixture")
			_, e := a.Restore(t.Context(), m.BackupID, target)
			require.Error(t, e)
			_, e = os.Stat(target)
			assert.True(t, os.IsNotExist(e), "unverified plaintext must never publish a stage")
		})
	}
}
func TestEncryptionKeyRotationPreservesPinnedEvidence(t *testing.T) {
	a, s, c, m, data, system := archiveSetup(t)
	require.NoError(t, a.Write(t.Context(), m, data, system))
	next, e := age.GenerateX25519Identity()
	require.NoError(t, e)
	c.Recipients = map[string]string{"new": next.Recipient().String()}
	c.Identities["new"] = next.String()
	rotated, e := NewBackupArchive(s, s, c)
	require.NoError(t, e)
	_, e = rotated.Verify(t.Context(), m.BackupID)
	require.NoError(t, e)
	m.BackupID = "backup-2"
	require.NoError(t, rotated.Write(t.Context(), m, data, system))
	c.Identities = map[string]string{"new": next.String()}
	newOnly, e := NewBackupArchive(s, s, c)
	require.NoError(t, e)
	_, e = newOnly.Verify(t.Context(), "backup-1")
	require.Error(t, e)
	_, e = newOnly.Verify(t.Context(), "backup-2")
	require.NoError(t, e)
}
func TestBackupArchiveNeverCompletesMissingSource(t *testing.T) {
	a, s, _, m, data, system := archiveSetup(t)
	delete(s.data, "source-generation")
	require.Error(t, a.Write(t.Context(), m, data, system))
	for k := range s.data {
		assert.NotEqual(t, "complete.json", filepath.Base(k))
	}
}

func TestBackupArchiveRequiresAuthenticatedFinalAgeChunk(t *testing.T) {
	a, s, _, m, data, system := archiveSetup(t)
	require.NoError(t, a.Write(t.Context(), m, data, system))
	cipherKey := a.prefix(m.BackupID) + "archive.age"
	completionKey := a.prefix(m.BackupID) + "complete.json"
	payload := s.data[cipherKey]
	payload = payload[:len(payload)-1]
	s.data[cipherKey] = payload
	var completion backupCompletion
	require.NoError(t, json.Unmarshal(s.data[completionKey], &completion))
	completion.CipherBytes = int64(len(payload))
	completion.CipherDigest = backupDigest(payload)
	completion.MAC = a.signCompletion(completion)
	encoded, e := json.Marshal(completion)
	require.NoError(t, e)
	s.data[completionKey] = encoded
	_, e = a.Restore(t.Context(), m.BackupID, filepath.Join(a.config.ScratchRoot, "restore-final-chunk"))
	require.Error(t, e)
}
