package repository_test

import (
	"bytes"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/repository"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Export against a real graph: stored candidate status is preserved, a
// tombstoned claim identity is neither exported nor accepted again, and
// another client's task candidates stay invisible.
func TestTransferExportAgainstGraph(t *testing.T) {
	store, _, _ := jobsGraph(t)
	owners := repository.NewIdentityRepository(store)
	identity := service.NewIdentityService(store, owners, gateway.NostrIdentity{})
	caps := []string{"status:read", "retrieval:read", "candidate:write", "memory:export"}
	agent, err := identity.Setup(t.Context(), domain.OwnerIdentity{ID: "owner", PublicKey: "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"}, service.EnrollInput{ClientID: "agent", Role: domain.RoleAgent, ProjectIDs: []string{"demo"}, Capabilities: caps})
	require.NoError(t, err)
	other, err := identity.EnrollHost(t.Context(), service.EnrollInput{ClientID: "other", Role: domain.RoleAgent, ProjectIDs: []string{"demo"}, Capabilities: caps})
	require.NoError(t, err)
	auth := repository.NewRetrievalRepository(store)
	evidence := repository.NewEvidenceRepository(store)
	capture := service.NewCaptureService(store, evidence, repository.NewJobsRepository(store), nil)
	memory := service.NewMemoryService(store, repository.NewMemoryRepository(store), auth, evidence, capture)
	transfer, err := service.NewTransferService(store, repository.NewTransferRepository(store), auth, bytes.Repeat([]byte{3}, 32))
	require.NoError(t, err)
	scope := domain.Scope{ProjectID: "demo", TaskID: "import-task"}
	for _, id := range []string{"imp-a", "imp-b", "imp-c"} {
		_, err = memory.Submit(t.Context(), agent.Principal, domain.CandidateInput{ClaimID: id, Text: "imported " + id, Kind: "imported_note", Scope: scope})
		require.NoError(t, err)
	}
	ids := func(m service.ExportManifest) []string {
		var out []string
		for _, c := range m.Claims {
			out = append(out, c.Claim.ClaimID)
			assert.Equal(t, domain.ClaimCandidate, c.Claim.Status, "export never promotes")
			assert.Equal(t, domain.TrustSubmitted, c.OriginTrust)
		}
		return out
	}

	t.Run("exports stored candidates", func(t *testing.T) {
		got, err := transfer.Export(t.Context(), agent.Principal, scope, "")

		require.NoError(t, err)
		assert.Equal(t, []string{"imp-a", "imp-b", "imp-c"}, ids(got))
		assert.Equal(t, domain.CompletenessComplete, got.Completeness)
	})
	t.Run("another client cannot export task candidates", func(t *testing.T) {
		got, err := transfer.Export(t.Context(), other.Principal, scope, "")

		require.NoError(t, err)
		assert.Empty(t, got.Claims)
		assert.Equal(t, 3, got.Omitted)
	})
	t.Run("deleted source identity cannot resurrect", func(t *testing.T) {
		backupFixtureQuery(t, store, `MERGE (:Tombstone {id:$id})`, map[string]any{"id": "imp-b"})

		got, err := transfer.Export(t.Context(), agent.Principal, scope, "")
		require.NoError(t, err)
		_, resubmit := memory.Submit(t.Context(), agent.Principal, domain.CandidateInput{ClaimID: "imp-b", Text: "imported imp-b", Kind: "imported_note", Scope: scope})

		assert.Equal(t, []string{"imp-a", "imp-c"}, ids(got))
		require.ErrorIs(t, resubmit, domain.ErrForbidden, "a tombstoned claim identity is refused without revealing it existed")
	})
	t.Run("revoked client cannot continue", func(t *testing.T) {
		require.NoError(t, identity.RevokeHost(t.Context(), "other"))

		_, err := transfer.Export(t.Context(), other.Principal, scope, "")

		require.Error(t, err)
	})
}
