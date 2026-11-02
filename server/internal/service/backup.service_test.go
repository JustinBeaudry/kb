package service

import (
	"context"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type backupFixture struct {
	calls     []string
	failure   string
	manifest  domain.BackupManifest
	state     domain.BackupOperation
	resumed   bool
	unknown   int
	uncertain bool
}

func (f *backupFixture) step(s string) error {
	f.calls = append(f.calls, s)
	if f.failure == s {
		return domain.ErrUnavailable
	}
	return nil
}
func (f *backupFixture) Begin(context.Context, string) (domain.MaintenanceFence, error) {
	return domain.MaintenanceFence{OperationID: "op", Generation: 2}, f.step("begin")
}
func (f *backupFixture) WaitDrained(context.Context, domain.MaintenanceFence) error {
	return f.step("drain")
}
func (f *backupFixture) State(context.Context) (domain.MaintenanceState, error) {
	return domain.MaintenanceState{Fence: domain.MaintenanceFence{OperationID: "op", Generation: 2}, Phase: "recovering", Unknown: f.unknown}, nil
}

func TestBackupRecoveryNeverClearsUnknownPermit(t *testing.T) {
	f := &backupFixture{unknown: 1, state: domain.BackupOperation{Version: 1, ID: "op", BackupID: "op", Kind: "backup", Stage: "failed", Fence: domain.MaintenanceFence{OperationID: "op", Generation: 2}}}
	s := fixtureBackupService(t, f)
	require.ErrorIs(t, s.Recover(t.Context(), "op"), domain.ErrMaintenance)
	assert.NotContains(t, f.calls, "start")
	assert.False(t, f.resumed)
}
func TestBackupRecoveryReopensOnlyAfterLiveReplay(t *testing.T) {
	f := &backupFixture{state: domain.BackupOperation{Version: 1, ID: "op", BackupID: "op", Kind: "backup", Stage: "failed", Fence: domain.MaintenanceFence{OperationID: "op", Generation: 2}}}
	s := fixtureBackupService(t, f)
	require.NoError(t, s.Recover(t.Context(), "op"))
	assert.True(t, f.resumed)
	assert.Equal(t, "recovered", f.state.Stage)
}
func (f *backupFixture) SetPhase(_ context.Context, _ domain.MaintenanceFence, p string) error {
	return f.step(p)
}
func (f *backupFixture) Resume(context.Context, domain.MaintenanceFence) error {
	if err := f.step("resume"); err != nil {
		return err
	}
	f.resumed = true
	return nil
}
func (f *backupFixture) AdmitRecovery(context.Context, domain.MaintenanceFence, string, string) (domain.OperationPermit, error) {
	return domain.OperationPermit{ID: "serial"}, f.step("recovery-permit")
}
func (f *backupFixture) Finish(context.Context, domain.OperationPermit) error {
	return f.step("finish")
}
func (f *backupFixture) MarkRecoveryComplete(context.Context, domain.MaintenanceFence) error {
	return f.step("recovery-complete")
}
func (f *backupFixture) RecordBackup(context.Context, domain.MaintenanceFence, string, time.Time) error {
	return f.step("record")
}
func (f *backupFixture) ReconcileJournal(ctx context.Context) (domain.JournalBarrier, error) {
	if f.uncertain {
		domain.MarkEffectUncertain(ctx)
	}
	return domain.JournalBarrier{Namespace: "live", Sequence: 7, Digest: "head"}, f.step("journal")
}
func (f *backupFixture) MarkUnknown(context.Context, domain.OperationPermit) error {
	return f.step("mark-unknown")
}
func (f *backupFixture) CaptureManifest(context.Context, string) (domain.BackupManifest, error) {
	return f.manifest, f.step("capture")
}
func (f *backupFixture) VerifyRestore(context.Context, domain.BackupManifest) error {
	return f.step("restore-evidence")
}
func (f *backupFixture) InvalidateRestoredClients(context.Context, string) error {
	return f.step("revoke")
}
func (f *backupFixture) InitializeAndRecover(context.Context) error { return f.step("schema-recover") }
func (f *backupFixture) ValidateRestoredSnapshot(context.Context, domain.BackupManifest) error {
	return f.step("snapshot-integrity")
}
func (f *backupFixture) Stop(context.Context) error        { return f.step("stop") }
func (f *backupFixture) WaitStopped(context.Context) error { return f.step("wait-stopped") }
func (f *backupFixture) Dump(context.Context, string) (string, string, error) {
	return "neo4j.dump", "system.dump", f.step("dump")
}
func (f *backupFixture) Load(context.Context, string, string) error { return f.step("load-both") }
func (f *backupFixture) Start(context.Context) error                { return f.step("start") }
func (f *backupFixture) Write(context.Context, domain.BackupManifest, string, string) error {
	return f.step("write")
}
func (f *backupFixture) Verify(context.Context, string) (domain.BackupManifest, error) {
	return f.manifest, f.step("verify")
}
func (f *backupFixture) Restore(context.Context, string, string) (domain.BackupManifest, error) {
	return f.manifest, f.step("decrypt-stage")
}
func (f *backupFixture) Retain(context.Context, time.Time) error { return f.step("retain") }
func (f *backupFixture) LoadOperation(context.Context) (domain.BackupOperation, error) {
	return f.state, nil
}
func (f *backupFixture) SaveOperation(_ context.Context, v domain.BackupOperation) error {
	f.state = v
	return f.step("persist:" + v.Stage)
}
func fixtureBackupService(t *testing.T, f *backupFixture) *BackupService {
	t.Helper()
	s, err := NewBackupService(f, f, f, f, f, BackupConfig{ScratchRoot: t.TempDir(), EvidenceNamespace: "fresh-evidence", InstanceID: "supervisor"}, func() time.Time { return time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC) })
	require.NoError(t, err)
	return s
}
func TestBackupNeverStopsGraphBeforeDurableDrain(t *testing.T) {
	f := &backupFixture{failure: "drain"}
	s := fixtureBackupService(t, f)
	_, err := s.Run(t.Context(), "backup-1")
	require.Error(t, err)
	assert.NotContains(t, f.calls, "stop")
	assert.False(t, f.resumed)
}
func TestBackupRestartsClosedAndReplaysLiveJournalBeforeResume(t *testing.T) {
	f := &backupFixture{manifest: domain.BackupManifest{EvidenceNamespace: "old-evidence"}}
	s := fixtureBackupService(t, f)
	_, err := s.Run(t.Context(), "backup-1")
	require.NoError(t, err)
	assert.True(t, f.resumed)
	assert.Subset(t, f.calls, []string{"dump", "write", "verify", "record", "schema-recover", "journal", "recovery-complete", "resume"})
	assert.Less(t, indexCall(f.calls, "verify"), indexCall(f.calls, "start"))
	assert.Less(t, indexCall(f.calls, "journal", 1), indexCall(f.calls, "resume"))
}
func TestRestoreReplaysDeletionBeforeReadiness(t *testing.T) {
	f := &backupFixture{manifest: domain.BackupManifest{BackupID: "old", EvidenceNamespace: "old-evidence"}}
	s := fixtureBackupService(t, f)
	_, err := s.Restore(t.Context(), "old", "restore-1")
	require.NoError(t, err)
	assert.Less(t, indexCall(f.calls, "revoke"), indexCall(f.calls, "journal"))
	assert.Less(t, indexCall(f.calls, "journal"), indexCall(f.calls, "restore-evidence"))
	assert.Less(t, indexCall(f.calls, "restore-evidence"), indexCall(f.calls, "resume"))
}
func TestRestoreFailureNeverResumes(t *testing.T) {
	for _, stage := range []string{"decrypt-stage", "load-both", "revoke", "journal", "restore-evidence", "finish", "recovery-complete"} {
		t.Run(stage, func(t *testing.T) {
			f := &backupFixture{failure: stage, manifest: domain.BackupManifest{EvidenceNamespace: "old"}}
			s := fixtureBackupService(t, f)
			_, err := s.Restore(t.Context(), "old", "restore-1")
			require.Error(t, err)
			assert.False(t, f.resumed)
		})
	}
}
func TestRestoreRejectsOriginalEvidenceNamespace(t *testing.T) {
	f := &backupFixture{manifest: domain.BackupManifest{EvidenceNamespace: "fresh-evidence"}}
	s := fixtureBackupService(t, f)
	_, err := s.Restore(t.Context(), "old", "restore-1")
	require.ErrorIs(t, err, domain.ErrConflict)
	assert.NotContains(t, f.calls, "load-both")
	assert.False(t, f.resumed)
}
func TestBackupFailedArchiveKeepsAdmissionClosed(t *testing.T) {
	f := &backupFixture{failure: "verify"}
	s := fixtureBackupService(t, f)
	_, err := s.Run(t.Context(), "backup-1")
	require.Error(t, err)
	assert.NotContains(t, f.calls, "record")
	assert.False(t, f.resumed)
	assert.Equal(t, "failed", f.state.Stage)
}
func indexCall(calls []string, s string, skip ...int) int {
	n := 0
	if len(skip) > 0 {
		n = skip[0]
	}
	for i, v := range calls {
		if v == s {
			if n == 0 {
				return i
			}
			n--
		}
	}
	return -1
}

func TestCompletedBackupAndRestoreRetriesDoNotRepeatEffects(t *testing.T) {
	for _, kind := range []string{"backup", "restore"} {
		t.Run(kind, func(t *testing.T) {
			f := &backupFixture{manifest: domain.BackupManifest{EvidenceNamespace: "old"}}
			s := fixtureBackupService(t, f)
			invoke := func() (domain.BackupResult, error) {
				if kind == "backup" {
					return s.Run(t.Context(), "id")
				}
				return s.Restore(t.Context(), "snapshot", "id")
			}
			first, e := invoke()
			require.NoError(t, e)
			count := len(f.calls)
			second, e := invoke()
			require.NoError(t, e)
			assert.Equal(t, first, second)
			assert.Len(t, f.calls, count)
		})
	}
}
