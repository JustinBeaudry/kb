package service

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type snapshotFixture struct {
	objects []domain.BackupObject
	data    map[string][]byte
	deleted []string
	missing bool
	revoked string
}

func (f *snapshotFixture) InventoryPage(_ context.Context, _ Tx, after string, limit int) ([]domain.BackupObject, error) {
	out := []domain.BackupObject{}
	for _, o := range f.objects {
		if o.ArtifactID > after {
			out = append(out, o)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}
func (f *snapshotFixture) InvalidateRestoredClients(_ context.Context, _ Tx, id string) error {
	f.revoked = id
	return nil
}
func (f *snapshotFixture) Barrier(context.Context, Tx) (domain.JournalBarrier, error) {
	return domain.JournalBarrier{Namespace: "live", Sequence: 9, Digest: "head"}, nil
}
func (f *snapshotFixture) Recover(context.Context) error { return nil }
func (f *snapshotFixture) CurrentHead(context.Context) (domain.JournalBarrier, error) {
	return domain.JournalBarrier{Namespace: "live", Sequence: 9, Digest: "head"}, nil
}
func (f *snapshotFixture) Open(_ context.Context, key string) (io.ReadCloser, error) {
	v, ok := f.data[key]
	if !ok {
		return nil, domain.ErrIncomplete
	}
	return io.NopCloser(bytes.NewReader(v)), nil
}
func (f *snapshotFixture) Put(_ context.Context, key string, r io.Reader, _ string) error {
	v, e := io.ReadAll(r)
	if e != nil {
		return e
	}
	f.data[key] = v
	return nil
}
func (f *snapshotFixture) Delete(_ context.Context, key string) error {
	f.deleted = append(f.deleted, key)
	delete(f.data, key)
	return nil
}
func (f *snapshotFixture) OpenRestoreObject(_ context.Context, _ domain.BackupObject) (io.ReadCloser, error) {
	if f.missing {
		return nil, domain.ErrIncomplete
	}
	return io.NopCloser(bytes.NewBufferString("pinned-support")), nil
}
func snapshotService(t *testing.T, f *snapshotFixture) *BackupSnapshotService {
	t.Helper()
	s, e := NewBackupSnapshotService(&identityFixture{}, f, f, f, f, f, func(context.Context) error { return nil }, domain.BackupManifest{SchemaDigest: TokenDigest("schema"), ToolVersionsDigest: TokenDigest("tools"), Architecture: "linux/arm64", EvidenceNamespace: "fresh"}, nil)
	require.NoError(t, e)
	return s
}
func TestBackupSnapshotIncludesPinnedSourcesAndTombstones(t *testing.T) {
	f := &snapshotFixture{objects: []domain.BackupObject{{ArtifactID: "a", ObjectKey: "source", Generation: "g", Digest: TokenDigest("pinned-support"), Bytes: 14, Available: true, State: "ready"}, {ArtifactID: "b", Deleted: true, State: "deleted"}}}
	s := snapshotService(t, f)
	m, e := s.CaptureManifest(t.Context(), "backup")
	require.NoError(t, e)
	assert.Len(t, m.Objects, 2)
	assert.True(t, m.Objects[1].Deleted)
}
func TestRestoreRejectsMissingEvidence(t *testing.T) {
	o := domain.BackupObject{ArtifactID: "a", ObjectKey: "source", Generation: "g", Digest: TokenDigest("pinned-support"), Bytes: 14, Available: true, State: "ready"}
	f := &snapshotFixture{objects: []domain.BackupObject{o}, missing: true, data: map[string][]byte{}}
	s := snapshotService(t, f)
	require.Error(t, s.VerifyRestore(t.Context(), domain.BackupManifest{Objects: []domain.BackupObject{o}}))
	assert.Empty(t, f.data)
}
func TestRestoreCopiesPinnedSupportAndFencesDeletedSnapshotSources(t *testing.T) {
	old := domain.BackupObject{ArtifactID: "a", ObjectKey: "source", Generation: "g", Digest: TokenDigest("pinned-support"), Bytes: 14, Available: true, State: "ready"}
	deleted := old
	deleted.Deleted = true
	deleted.State = "deleted"
	deleted.Available = false
	f := &snapshotFixture{objects: []domain.BackupObject{deleted}, data: map[string][]byte{}}
	s := snapshotService(t, f)
	require.NoError(t, s.VerifyRestore(t.Context(), domain.BackupManifest{Objects: []domain.BackupObject{old}}))
	assert.Equal(t, []string{"source"}, f.deleted)
	assert.Empty(t, f.data)
}
func TestRestoreVerifiesExactSourceDigest(t *testing.T) {
	o := domain.BackupObject{ArtifactID: "a", ObjectKey: "source", Generation: "g", Digest: TokenDigest("pinned-support"), Bytes: 14, Available: true, State: "ready"}
	f := &snapshotFixture{objects: []domain.BackupObject{o}, data: map[string][]byte{}}
	s := snapshotService(t, f)
	require.NoError(t, s.VerifyRestore(t.Context(), domain.BackupManifest{Objects: []domain.BackupObject{o}}))
	assert.Equal(t, []byte("pinned-support"), f.data["source"])
}
