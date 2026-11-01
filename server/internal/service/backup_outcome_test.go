package service

import (
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestBackupRefusesSnapshotAfterMaskedUncertainJournalEffect(t *testing.T) {
	f := &backupFixture{uncertain: true}
	s := fixtureBackupService(t, f)
	_, err := s.Run(t.Context(), "backup-uncertain")
	require.ErrorIs(t, err, domain.ErrUnknownOutcome)
	assert.False(t, f.resumed)
	assert.NotContains(t, f.calls, "stop")
}
func TestRestoreRetainsUnknownRecoveryPermitAfterMaskedUncertainty(t *testing.T) {
	f := &backupFixture{uncertain: true, manifest: domain.BackupManifest{EvidenceNamespace: "old"}}
	s := fixtureBackupService(t, f)
	_, err := s.Restore(t.Context(), "old", "restore-uncertain")
	require.ErrorIs(t, err, domain.ErrUnknownOutcome)
	assert.Contains(t, f.calls, "mark-unknown")
	assert.False(t, f.resumed)
}

func TestRestoreRetryPreservesDurableSnapshotValidation(t *testing.T) {
	f := &backupFixture{failure: "restore-evidence", manifest: domain.BackupManifest{EvidenceNamespace: "old"}}
	s := fixtureBackupService(t, f)
	_, err := s.Restore(t.Context(), "old", "restore-retry")
	require.Error(t, err)
	f.failure = ""
	_, err = s.Restore(t.Context(), "old", "restore-retry")
	require.NoError(t, err)
	checks := 0
	for _, call := range f.calls {
		if call == "snapshot-integrity" {
			checks++
		}
	}
	assert.Equal(t, 1, checks, "live replay after a verified snapshot must not be confused with archive corruption on exact retry")
}
