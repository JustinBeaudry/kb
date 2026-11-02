package repository_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/JustinBeaudry/kb/server/internal/repository"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func backupFixtureQuery(t *testing.T, store *graph.Store, query string, params map[string]any) {
	t.Helper()
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		native, err := store.Unwrap(tx)
		if err != nil {
			return err
		}
		result, err := native.Run(ctx, query, params)
		if err != nil {
			return err
		}
		_, err = result.Consume(ctx)
		return err
	}))
}

// Repository-level fixtures seed projection rows deliberately: they test actual
// Cypher pagination, all tombstone predicates and corrupt persistence handling,
// without presenting those projections as an application acceptance workflow.
func TestBackupRepositoryInventoryAndRestoreAuthority(t *testing.T) {
	store, jobs, _ := jobsGraph(t)
	owners := repository.NewIdentityRepository(store)
	identity := service.NewIdentityService(store, owners, gateway.NostrIdentity{})
	initial, err := identity.Setup(t.Context(), domain.OwnerIdentity{ID: "backup-owner", PublicKey: "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"}, service.EnrollInput{ClientID: "before-restore", Role: domain.RoleCollector, ProjectIDs: []string{"demo"}, Capabilities: []string{"artifacts:write"}})
	require.NoError(t, err)
	repo := repository.NewBackupRepository(store)
	lifecycleRepo := repository.NewLifecycleRepository(store)

	t.Run("inventory pages preserve unavailable and every deletion fence", func(t *testing.T) {
		rows := make([]map[string]any, 0, 105)
		for i := 1; i <= 105; i++ {
			id := fmt.Sprintf("backup-artifact-%03d", i)
			rows = append(rows, map[string]any{"id": id, "objectKey": "evidence/" + id + "/generation", "generation": "generation", "digest": service.TokenDigest(id), "bytes": int64(i), "state": "ready", "available": true, "projectID": "demo", "sessionID": fmt.Sprintf("session-%03d", i), "taskID": fmt.Sprintf("task-%03d", i)})
		}
		rows[1]["state"] = "staged"
		rows[1]["available"] = false
		rows[2]["state"] = "deleted"
		rows[3]["state"] = "abandoned"
		rows[4]["state"] = "abandoning"
		backupFixtureQuery(t, store, `UNWIND $rows AS row CREATE (a:Artifact) SET a=row`, map[string]any{"rows": rows})
		backupFixtureQuery(t, store, `CREATE (:Tombstone {id:'backup-artifact-006'}),(:SourceTombstone {projectID:'demo',digest:$digest}),(:SessionTombstone {projectID:'demo',id:'session-008'}),(:TaskTombstone {projectID:'demo',id:'task-009'})`, map[string]any{"digest": rows[6]["digest"]})
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			page, e := repo.InventoryPage(ctx, tx, "", 100)
			require.NoError(t, e)
			require.Len(t, page, 100)
			assert.Equal(t, "backup-artifact-001", page[0].ArtifactID)
			assert.False(t, page[1].Available)
			assert.Equal(t, "staged", page[1].State)
			assert.False(t, page[1].Deleted)
			for i := 2; i < 9; i++ {
				assert.True(t, page[i].Deleted, "deletion projection %d", i+1)
			}
			assert.False(t, page[9].Deleted)
			assert.Equal(t, int64(10), page[9].Bytes)
			tail, e := repo.InventoryPage(ctx, tx, page[len(page)-1].ArtifactID, 100)
			require.NoError(t, e)
			require.Len(t, tail, 5)
			empty, e := repo.InventoryPage(ctx, tx, tail[len(tail)-1].ArtifactID, 100)
			require.NoError(t, e)
			assert.Empty(t, empty)
			_, e = repo.InventoryPage(ctx, tx, "", 0)
			assert.ErrorIs(t, e, domain.ErrInvalidInput)
			_, e = repo.InventoryPage(ctx, tx, "", 101)
			assert.ErrorIs(t, e, domain.ErrInvalidInput)
			return nil
		}))
		_, e := repo.InventoryPage(t.Context(), nil, "", 1)
		require.Error(t, e, "an unlocked/foreign transaction cannot query inventory")
	})
	t.Run("barrier requires authenticated typed persistence", func(t *testing.T) {
		expected := domain.JournalBarrier{Namespace: "independent-component", Sequence: 7, Digest: service.TokenDigest("journal-head")}
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			_, e := repo.Barrier(ctx, tx)
			assert.ErrorIs(t, e, domain.ErrUnavailable)
			return lifecycleRepo.SetBarrier(ctx, tx, expected)
		}))
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			barrier, e := repo.Barrier(ctx, tx)
			require.NoError(t, e)
			assert.Equal(t, expected, barrier)
			return nil
		}))
		backupFixtureQuery(t, store, `MATCH (o:OwnerState {id:'owner'}) SET o.journalBarrier='invalid-json'`, nil)
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			_, e := repo.Barrier(ctx, tx)
			assert.ErrorIs(t, e, domain.ErrUnavailable)
			return lifecycleRepo.SetBarrier(ctx, tx, expected)
		}))
		_, e := repo.Barrier(t.Context(), nil)
		require.Error(t, e)
	})
	t.Run("first policy gate distinguishes owner from configured policy", func(t *testing.T) {
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			assert.ErrorIs(t, repo.BootstrapPolicyReady(ctx, tx), domain.ErrIncomplete)
			return nil
		}))
		lifecycle := service.NewLifecycleService(store, lifecycleRepo, owners, jobs, nil, nil, time.Now)
		_, e := lifecycle.UpdatePolicyHost(t.Context(), "demo", "", domain.Policy{Revision: "backup-policy", SpoolLimitBytes: 1 << 20})
		require.NoError(t, e)
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error { return repo.BootstrapPolicyReady(ctx, tx) }))
		require.Error(t, repo.BootstrapPolicyReady(t.Context(), nil))
	})
	t.Run("restore revokes old bearers but retry preserves reenrollment", func(t *testing.T) {
		_, e := identity.Authenticate(t.Context(), initial.Bearer)
		require.NoError(t, e)
		invalidate := func(id string) error {
			return store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error { return repo.InvalidateRestoredClients(ctx, tx, id) })
		}
		require.NoError(t, invalidate("restore-one"))
		_, e = identity.Authenticate(t.Context(), initial.Bearer)
		require.ErrorIs(t, e, domain.ErrUnauthorized)
		renewed, e := identity.EnrollHost(t.Context(), service.EnrollInput{ClientID: "after-restore", Role: domain.RoleCollector, ProjectIDs: []string{"demo"}, Capabilities: []string{"artifacts:write"}})
		require.NoError(t, e)
		require.NoError(t, invalidate("restore-one"))
		_, e = identity.Authenticate(t.Context(), renewed.Bearer)
		require.NoError(t, e, "retry must not revoke post-restore enrollment")
		require.NoError(t, invalidate("restore-two"))
		_, e = identity.Authenticate(t.Context(), renewed.Bearer)
		require.ErrorIs(t, e, domain.ErrUnauthorized)
		require.ErrorIs(t, invalidate(""), domain.ErrInvalidInput)
		require.Error(t, repo.InvalidateRestoredClients(t.Context(), nil, "restore-three"))
	})
	t.Run("malformed artifact projection refuses a backup page", func(t *testing.T) {
		backupFixtureQuery(t, store, `MATCH (a:Artifact {id:'backup-artifact-105'}) SET a.available='invalid-bool'`, nil)
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			_, e := repo.InventoryPage(ctx, tx, "backup-artifact-104", 100)
			assert.ErrorIs(t, e, domain.ErrUnavailable)
			return nil
		}))
	})
}
