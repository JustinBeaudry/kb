package service

import (
	"context"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This boundary reproduces the repository's persisted idempotency marker:
// restoring a snapshot also restores that marker and its then-valid clients.
type restoredMarkerSnapshot struct {
	*backupFixture
	graphMarker string
	clientValid bool
}

func (s *restoredMarkerSnapshot) InvalidateRestoredClients(_ context.Context, marker string) error {
	if s.graphMarker != marker {
		s.clientValid = false
		s.graphMarker = marker
	}
	return s.step("revoke")
}
func incarnationService(t *testing.T, f *backupFixture, snapshot *restoredMarkerSnapshot) *BackupService {
	t.Helper()
	s, e := NewBackupService(f, snapshot, f, f, f, BackupConfig{ScratchRoot: t.TempDir(), EvidenceNamespace: "fresh", InstanceID: "fixture"}, time.Now)
	require.NoError(t, e)
	return s
}
func TestReusedOperatorRestoreIDCannotReviveSnapshotClients(t *testing.T) {
	f := &backupFixture{manifest: domain.BackupManifest{EvidenceNamespace: "old"}}
	snapshot := &restoredMarkerSnapshot{backupFixture: f, clientValid: true}
	s := incarnationService(t, f, snapshot)
	_, err := s.Restore(t.Context(), "backup-A", "operator-R")
	require.NoError(t, err)
	assert.False(t, snapshot.clientValid)
	savedMarker, firstDirectory := snapshot.graphMarker, f.state.Directory
	// Owner enrolls C, then B captures both valid C and the previous marker. A
	// later revocation is absent from B; local last-operation state now names B.
	snapshot.clientValid = true
	f.state = domain.BackupOperation{Version: 1, ID: "backup-B", BackupID: "backup-B", Kind: "backup", Stage: "complete"}
	snapshot.graphMarker = savedMarker
	_, err = s.Restore(t.Context(), "backup-B", "operator-R")
	require.NoError(t, err)
	assert.False(t, snapshot.clientValid, "restored marker must never skip revocation for a new restore incarnation")
	assert.NotEqual(t, savedMarker, snapshot.graphMarker)
	assert.NotEqual(t, firstDirectory, f.state.Directory, "fresh restore must not reuse a prior graph load target")
}
func TestExactRestoreRetryPreservesItsIncarnationAndNewEnrollment(t *testing.T) {
	f := &backupFixture{failure: "restore-evidence", manifest: domain.BackupManifest{EvidenceNamespace: "old"}}
	snapshot := &restoredMarkerSnapshot{backupFixture: f, clientValid: true}
	s := incarnationService(t, f, snapshot)
	_, err := s.Restore(t.Context(), "backup-A", "operator-R")
	require.Error(t, err)
	marker, directory := snapshot.graphMarker, f.state.Directory
	snapshot.clientValid = true
	f.failure = ""
	_, err = s.Restore(t.Context(), "backup-A", "operator-R")
	require.NoError(t, err)
	assert.Equal(t, marker, snapshot.graphMarker)
	assert.Equal(t, directory, f.state.Directory)
	assert.True(t, snapshot.clientValid, "retry must preserve a client enrolled after this incarnation's revocation")
}
