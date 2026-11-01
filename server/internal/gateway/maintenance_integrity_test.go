package gateway_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func maintenanceIntegrityPath(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0700))
	return filepath.Join(dir, "maintenance.json")
}

func TestMaintenanceIntegrityFreshCreationCannotOverwriteMalformedHistory(t *testing.T) {
	for index, payload := range []string{
		`{"version":1,"state":{},"permits":[]} {"version":1}`,
		`{"version":1,"state":{},"permits":[],"unexpected_authority":true}`,
		strings.Repeat("x", (4<<20)+1),
	} {
		t.Run([]string{"trailing-document", "unknown-field", "oversized"}[index], func(t *testing.T) {
			path := maintenanceIntegrityPath(t)
			require.NoError(t, os.WriteFile(path, []byte(payload), 0600))
			_, err := gateway.NewMaintenanceStateStore(path, true)
			require.ErrorIs(t, err, domain.ErrUnavailable)
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, payload, string(after))
		})
	}
}

func TestMaintenanceIntegrityFailedFinishReopensAsDurableUnknown(t *testing.T) {
	ctx := context.Background()
	path := maintenanceIntegrityPath(t)
	store, err := gateway.NewMaintenanceStateStore(path, true)
	require.NoError(t, err)
	s, err := service.NewMaintenanceService(ctx, store)
	require.NoError(t, err)
	state, err := s.State(ctx)
	require.NoError(t, err)
	require.NoError(t, s.SetPhase(ctx, state.Fence, "recovering"))
	require.NoError(t, s.MarkRecoveryComplete(ctx, state.Fence))
	require.NoError(t, s.Resume(ctx, state.Fence))
	permit, err := s.Admit(ctx, "upload-process", "upload")
	require.NoError(t, err)
	// Closing this actual directory handle makes atomic persistence unavailable;
	// the previous fsynced document, including the permit, must survive.
	require.NoError(t, store.Close())
	require.ErrorIs(t, s.Finish(ctx, permit), domain.ErrUnavailable)
	_, err = s.Admit(ctx, "next-process", "api")
	require.ErrorIs(t, err, domain.ErrMaintenance)
	reopened, err := gateway.NewMaintenanceStateStore(path, false)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	s, err = service.NewMaintenanceService(ctx, reopened)
	require.NoError(t, err)
	state, err = s.State(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, state.Unknown)
	assert.Zero(t, state.Active)
	require.ErrorIs(t, s.Finish(ctx, permit), domain.ErrUnknownOutcome)
	deadline, cancel := context.WithTimeout(ctx, 5*time.Millisecond)
	t.Cleanup(cancel)
	require.ErrorIs(t, s.WaitDrained(deadline, state.Fence), context.DeadlineExceeded)
}

func TestMaintenanceIntegrityReloadDoesNotTrustCachedDrainOrRecoveryProof(t *testing.T) {
	ctx := context.Background()
	path := maintenanceIntegrityPath(t)
	state := domain.MaintenanceState{Phase: "offline", Fence: domain.MaintenanceFence{OperationID: "backup", Generation: 9}, RecoveryComplete: true, Active: 0, Unknown: 0}
	permit := domain.OperationPermit{ID: "unsettled-put", InstanceID: "old-instance", Kind: "upload", Generation: 8}
	// Cached counters and proof contradict the persisted registration. Registration
	// is authoritative; reload must not permit offline work or resume from them.
	document := struct {
		Version int                      `json:"version"`
		State   domain.MaintenanceState  `json:"state"`
		Permits []domain.OperationPermit `json:"permits"`
	}{1, state, []domain.OperationPermit{permit}}
	data, err := json.Marshal(document)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0600))
	store, err := gateway.NewMaintenanceStateStore(path, false)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	s, err := service.NewMaintenanceService(ctx, store)
	require.NoError(t, err)
	state, err = s.State(ctx)
	require.NoError(t, err)
	assert.Equal(t, "offline", state.Phase)
	assert.False(t, state.RecoveryComplete)
	assert.Equal(t, 1, state.Unknown)
	require.ErrorIs(t, s.Resume(ctx, state.Fence), domain.ErrMaintenance)
	require.ErrorIs(t, s.SetPhase(ctx, state.Fence, "recovering"), domain.ErrMaintenance)
	persisted, permits, err := store.Load(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, persisted.Unknown)
	require.Len(t, permits, 1)
	assert.True(t, permits[0].Unknown)
}

func TestMaintenanceIntegrityRejectedSaveLeavesLastDurableDocumentIntact(t *testing.T) {
	ctx := context.Background()
	path := maintenanceIntegrityPath(t)
	store, err := gateway.NewMaintenanceStateStore(path, true)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	state, permits, err := store.Load(ctx)
	require.NoError(t, err)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	require.ErrorIs(t, store.Save(canceled, state, permits), context.Canceled)
	state.BackupID = strings.Repeat("x", (4<<20)+1)
	require.ErrorIs(t, store.Save(ctx, state, permits), domain.ErrUnavailable)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}
