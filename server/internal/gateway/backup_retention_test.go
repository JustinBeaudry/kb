package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Faults are injected only at the backup storage I/O port. Signing, archive
// creation, authenticated completion parsing and retention all remain real.
type retentionStore struct {
	*archiveMemory
	listed       []string
	listErr      error
	openErrKey   string
	closeErrKey  string
	deleteErrKey string
	deleteErrors int
	deleted      []string
}

func (s *retentionStore) List(ctx context.Context, prefix string) ([]string, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	if s.listed != nil {
		return s.listed, nil
	}
	keys, err := s.archiveMemory.List(ctx, prefix)
	slices.Sort(keys)
	return keys, err
}
func (s *retentionStore) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if key == s.openErrKey {
		return nil, domain.ErrUnavailable
	}
	body, err := s.archiveMemory.Open(ctx, key)
	if err == nil && key == s.closeErrKey {
		return &retentionCloseFailure{ReadCloser: body}, nil
	}
	return body, err
}
func (s *retentionStore) Delete(ctx context.Context, key string) error {
	s.deleted = append(s.deleted, key)
	if key == s.deleteErrKey && s.deleteErrors > 0 {
		s.deleteErrors--
		return domain.ErrUnavailable
	}
	return s.archiveMemory.Delete(ctx, key)
}

type retentionCloseFailure struct{ io.ReadCloser }

func (b *retentionCloseFailure) Close() error {
	_ = b.ReadCloser.Close()
	return domain.ErrUnavailable
}

func retentionSetup(t *testing.T) (*BackupArchiveGateway, *retentionStore, func(string, time.Time)) {
	t.Helper()
	_, memory, config, manifest, data, system := archiveSetup(t)
	store := &retentionStore{archiveMemory: memory}
	archive, err := NewBackupArchive(store, memory, config)
	require.NoError(t, err)
	seed := func(id string, createdAt time.Time) {
		t.Helper()
		manifest.BackupID, manifest.CreatedAt = id, createdAt
		require.NoError(t, archive.Write(t.Context(), manifest, data, system))
	}
	return archive, store, seed
}

func TestBackupRetentionDeletesOnlyExpiredAuthenticatedSets(t *testing.T) {
	archive, store, seed := retentionSetup(t)
	cutoff := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	seed("expired", cutoff.Add(-time.Nanosecond))
	seed("boundary", cutoff)
	seed("recent", cutoff.Add(time.Hour))
	// Incomplete uploads have no authenticated completion timestamp. Retention
	// must not infer their age from listing order, names, or another authority.
	store.data["backups/fixture/partial/archive.age"] = []byte("unfinished")
	store.data["backups/another/expired/complete.json"] = []byte("other authority")
	store.data["backups/another/expired/archive.age"] = []byte("other archive")
	store.data["evidence/source"] = []byte("live evidence")
	store.data["journal/head"] = []byte("independent deletion journal")
	expected := map[string][]byte{}
	for key, value := range store.data {
		if !strings.HasPrefix(key, "backups/fixture/expired/") {
			expected[key] = bytes.Clone(value)
		}
	}

	require.NoError(t, archive.Retain(t.Context(), cutoff))

	assert.Equal(t, []string{"backups/fixture/expired/archive.age", "backups/fixture/expired/complete.json"}, store.deleted)
	assert.Equal(t, expected, store.data, "recent/boundary/partial sets, other authority, evidence and journal must remain byte-identical")
}

func TestBackupRetentionRefusesUntrustedCompletionBeforeDeleting(t *testing.T) {
	for _, mutation := range []string{"malformed", "trailing-json", "oversized", "wrong-id", "wrong-namespace", "zero-date", "zero-bytes", "unknown-authority", "forged-mac", "malformed-mac", "open-failure", "close-failure"} {
		t.Run(mutation, func(t *testing.T) {
			archive, store, seed := retentionSetup(t)
			cutoff := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
			seed("expired", cutoff.Add(-time.Hour))
			key := "backups/fixture/expired/complete.json"
			var completion backupCompletion
			require.NoError(t, json.Unmarshal(store.data[key], &completion))
			switch mutation {
			case "malformed":
				store.data[key] = []byte("{")
			case "trailing-json":
				store.data[key] = append(store.data[key], []byte("{}")...)
			case "oversized":
				store.data[key] = bytes.Repeat([]byte(" "), 16385)
			case "open-failure":
				store.openErrKey = key
			case "close-failure":
				store.closeErrKey = key
			default:
				switch mutation {
				case "wrong-id":
					completion.BackupID = "another"
				case "wrong-namespace":
					completion.Namespace = "another"
				case "zero-date":
					completion.CreatedAt = time.Time{}
				case "zero-bytes":
					completion.CipherBytes = 0
				case "unknown-authority":
					completion.AuthorityKeyID = "untrusted"
				case "forged-mac":
					completion.MAC = strings.Repeat("0", 64)
				case "malformed-mac":
					completion.MAC = "not-hex"
				}
				encoded, err := json.Marshal(completion)
				require.NoError(t, err)
				store.data[key] = encoded
			}
			before := map[string][]byte{}
			for key, value := range store.data {
				before[key] = bytes.Clone(value)
			}

			require.Error(t, archive.Retain(t.Context(), cutoff))

			assert.Empty(t, store.deleted)
			assert.Equal(t, before, store.data, "unauthenticated metadata cannot authorize physical deletion")
		})
	}
}

func TestBackupRetentionRetriesInterruptedDeletionWithoutTouchingEvidence(t *testing.T) {
	for _, failedObject := range []string{"archive.age", "complete.json"} {
		t.Run(failedObject, func(t *testing.T) {
			archive, store, seed := retentionSetup(t)
			cutoff := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
			seed("expired", cutoff.Add(-time.Hour))
			archiveKey, completionKey := "backups/fixture/expired/archive.age", "backups/fixture/expired/complete.json"
			store.deleteErrKey, store.deleteErrors = "backups/fixture/expired/"+failedObject, 1

			require.ErrorIs(t, archive.Retain(t.Context(), cutoff), domain.ErrUnavailable)

			assert.Contains(t, store.data, completionKey, "completion authority must remain until the archive deletion settles")
			if failedObject == "archive.age" {
				assert.Contains(t, store.data, archiveKey)
				assert.Equal(t, []string{archiveKey}, store.deleted)
			} else {
				assert.NotContains(t, store.data, archiveKey)
				assert.Equal(t, []string{archiveKey, completionKey}, store.deleted)
			}
			require.NoError(t, archive.Retain(t.Context(), cutoff), "retry must tolerate an already removed archive and finish the same authenticated set")
			assert.NotContains(t, store.data, archiveKey)
			assert.NotContains(t, store.data, completionKey)
			assert.Equal(t, map[string][]byte{"source-generation": []byte("pinned-evidence")}, store.data)
		})
	}
}

func TestBackupRetentionRefusesUnboundedOrEscapingListings(t *testing.T) {
	for _, test := range []struct {
		name string
		keys []string
		err  error
		want error
	}{
		{name: "list-failure", err: domain.ErrUnavailable, want: domain.ErrUnavailable},
		{name: "too-many-keys", keys: make([]string, 100001), want: domain.ErrBudget},
		{name: "nested-completion", keys: []string{"backups/fixture/id/nested/complete.json"}, want: domain.ErrUnavailable},
		{name: "foreign-namespace", keys: []string{"backups/foreign/id/complete.json"}, want: domain.ErrUnavailable},
		{name: "foreign-root", keys: []string{"evidence/fixture/id/complete.json"}, want: domain.ErrUnavailable},
		{name: "unsafe-id", keys: []string{"backups/fixture/bad.id/complete.json"}, want: domain.ErrUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			archive, store, _ := retentionSetup(t)
			store.listed, store.listErr = test.keys, test.err

			require.ErrorIs(t, archive.Retain(t.Context(), time.Now().UTC()), test.want)

			assert.Empty(t, store.deleted)
			assert.Equal(t, map[string][]byte{"source-generation": []byte("pinned-evidence")}, store.data)
		})
	}
}
