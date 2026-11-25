package service

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// transferGraph is a repository-boundary fake: the export page lists head
// identities in claim order and the authorizer reports which revisions the
// current principal may still see (tombstoned or unauthorized ones are absent).
type transferGraph struct {
	heads        []ExportHead
	eligible     map[string]domain.ClaimRevision
	unavailable  bool
	snapshotErr  error
	pageErr      error
	eligibleErr  error
	pageAfters   []string
	eligibleIDs  [][]string
	snapshotSeen int
}

func (g *transferGraph) ExportPage(_ context.Context, _ Tx, _ domain.Scope, after string, limit int) ([]ExportHead, error) {
	g.pageAfters = append(g.pageAfters, after)
	if g.pageErr != nil {
		return nil, g.pageErr
	}
	var out []ExportHead
	for _, h := range g.heads {
		if h.ClaimID > after && len(out) < limit {
			out = append(out, h)
		}
	}
	return out, nil
}

func (g *transferGraph) Snapshot(_ context.Context, _ Tx, p domain.Principal, _ domain.Scope) (RetrievalSnapshot, error) {
	g.snapshotSeen++
	if g.snapshotErr != nil {
		return RetrievalSnapshot{}, g.snapshotErr
	}
	return RetrievalSnapshot{Principal: p}, nil
}

func (g *transferGraph) Eligible(_ context.Context, _ Tx, _ domain.Principal, _ domain.Scope, ids []string) (EligibilityResult, error) {
	g.eligibleIDs = append(g.eligibleIDs, ids)
	if g.eligibleErr != nil {
		return EligibilityResult{}, g.eligibleErr
	}
	var out EligibilityResult
	for _, id := range ids {
		if c, ok := g.eligible[id]; ok {
			out.Claims = append(out.Claims, c)
		}
	}
	out.Unavailable = g.unavailable
	return out, nil
}

func exportPrincipal(client string) domain.Principal {
	return domain.Principal{ClientID: client, Role: domain.RoleAgent, ProjectIDs: []string{"demo"}, Capabilities: []string{"memory:export", "retrieval:read"}}
}

func exportClaim(id string, status domain.ClaimStatus, trust ...domain.OriginTrust) domain.ClaimRevision {
	c := domain.ClaimRevision{ClaimID: id, Revision: "rev-" + id, Text: "text " + id, Kind: "fact", Scope: domain.Scope{ProjectID: "demo"}, Status: status}
	for i, t := range trust {
		c.Evidence = append(c.Evidence, domain.EvidenceRef{ArtifactID: "artifact-" + id, Digest: strings.Repeat("a", 64), Locator: "loc", SourceRevision: "gen", Origin: domain.Origin{ClientID: "collector", Trust: t, AttestationID: map[bool]string{true: "att"}[i == 0 && t != domain.TrustSubmitted]}})
	}
	return c
}

func newTransferFixture(t *testing.T, claims ...domain.ClaimRevision) (*TransferService, *transferGraph) {
	t.Helper()
	g := &transferGraph{eligible: map[string]domain.ClaimRevision{}}
	for _, c := range claims {
		g.heads = append(g.heads, ExportHead{ClaimID: c.ClaimID, Revision: c.Revision})
		g.eligible[c.Revision] = c
	}
	s, err := NewTransferService(directTx{}, g, g, bytes.Repeat([]byte{9}, 32))
	require.NoError(t, err)
	return s, g
}

func TestTransferExport(t *testing.T) {
	scope := domain.Scope{ProjectID: "demo"}
	t.Run("export import preserves provenance without trust escalation", func(t *testing.T) {
		candidate := exportClaim("a", domain.ClaimCandidate, domain.TrustSubmitted)
		verified := exportClaim("b", domain.ClaimVerified, domain.TrustCollectorAttested, domain.TrustSubmitted)
		disputed := exportClaim("c", domain.ClaimDisputed, domain.TrustOwnerAuthenticated)
		s, _ := newTransferFixture(t, candidate, verified, disputed)

		got, err := s.Export(t.Context(), exportPrincipal("agent"), scope, "")

		require.NoError(t, err)
		require.Len(t, got.Claims, 3)
		assert.Equal(t, candidate, got.Claims[0].Claim)
		assert.Equal(t, domain.TrustSubmitted, got.Claims[0].OriginTrust)
		assert.Equal(t, verified, got.Claims[1].Claim)
		assert.Equal(t, domain.TrustSubmitted, got.Claims[1].OriginTrust, "weakest evidence origin is reported, never the strongest")
		assert.Equal(t, domain.ClaimDisputed, got.Claims[2].Claim.Status)
		assert.Equal(t, domain.TrustOwnerAuthenticated, got.Claims[2].OriginTrust)
		assert.Equal(t, domain.CompletenessComplete, got.Completeness)
		assert.Empty(t, got.Continuation)
		assert.Zero(t, got.Omitted)
	})
	t.Run("claim without evidence reports submitted origin", func(t *testing.T) {
		s, _ := newTransferFixture(t, exportClaim("a", domain.ClaimCandidate))

		got, err := s.Export(t.Context(), exportPrincipal("agent"), scope, "")

		require.NoError(t, err)
		require.Len(t, got.Claims, 1)
		assert.Equal(t, domain.TrustSubmitted, got.Claims[0].OriginTrust)
	})
	t.Run("deleted source identity cannot resurrect", func(t *testing.T) {
		kept, deleted := exportClaim("a", domain.ClaimVerified, domain.TrustSubmitted), exportClaim("b", domain.ClaimVerified, domain.TrustSubmitted)
		s, g := newTransferFixture(t, kept, deleted)
		delete(g.eligible, deleted.Revision) // tombstoned: the authorizer withholds it

		got, err := s.Export(t.Context(), exportPrincipal("agent"), scope, "")

		require.NoError(t, err)
		require.Len(t, got.Claims, 1)
		assert.Equal(t, "a", got.Claims[0].Claim.ClaimID)
		assert.Equal(t, 1, got.Omitted)
		assert.Equal(t, domain.CompletenessPartial, got.Completeness)
	})
	t.Run("unavailable evidence reports partial completeness", func(t *testing.T) {
		s, g := newTransferFixture(t, exportClaim("a", domain.ClaimVerified, domain.TrustSubmitted))
		g.unavailable = true

		got, err := s.Export(t.Context(), exportPrincipal("agent"), scope, "")

		require.NoError(t, err)
		assert.Equal(t, domain.CompletenessPartial, got.Completeness)
	})
	t.Run("claim outside requested scope is withheld", func(t *testing.T) {
		other := exportClaim("a", domain.ClaimCandidate)
		other.Scope = domain.Scope{ProjectID: "demo", TaskID: "other"}
		s, _ := newTransferFixture(t, other)

		got, err := s.Export(t.Context(), exportPrincipal("agent"), domain.Scope{ProjectID: "demo", TaskID: "task"}, "")

		require.NoError(t, err)
		assert.Empty(t, got.Claims)
		assert.Equal(t, 1, got.Omitted)
	})
}

func TestTransferExportResumesAndAuthorizesEachChunk(t *testing.T) {
	scope := domain.Scope{ProjectID: "demo"}
	var claims []domain.ClaimRevision
	for i := range ExportPageSize + 3 {
		claims = append(claims, exportClaim(string(rune('A'+i/26))+string(rune('a'+i%26)), domain.ClaimVerified, domain.TrustSubmitted))
	}
	t.Run("resumes after the last exported claim", func(t *testing.T) {
		s, g := newTransferFixture(t, claims...)
		first, err := s.Export(t.Context(), exportPrincipal("agent"), scope, "")
		require.NoError(t, err)
		require.Len(t, first.Claims, ExportPageSize)
		require.NotEmpty(t, first.Continuation)

		second, err := s.Export(t.Context(), exportPrincipal("agent"), scope, first.Continuation)

		require.NoError(t, err)
		assert.Len(t, second.Claims, 3)
		assert.Empty(t, second.Continuation)
		assert.Equal(t, claims[ExportPageSize-1].ClaimID, g.pageAfters[1])
		assert.Equal(t, 2, g.snapshotSeen, "every page re-reads authority")
	})
	t.Run("revocation between chunks stops the export", func(t *testing.T) {
		s, g := newTransferFixture(t, claims...)
		first, err := s.Export(t.Context(), exportPrincipal("agent"), scope, "")
		require.NoError(t, err)
		g.snapshotErr = domain.ErrUnauthorized

		_, err = s.Export(t.Context(), exportPrincipal("agent"), scope, first.Continuation)

		require.ErrorIs(t, err, domain.ErrUnauthorized)
		assert.Len(t, g.pageAfters, 1, "no records read after revocation")
	})
	t.Run("narrowed grant between chunks is forbidden", func(t *testing.T) {
		s, g := newTransferFixture(t, claims...)
		first, err := s.Export(t.Context(), exportPrincipal("agent"), scope, "")
		require.NoError(t, err)
		narrowed := &narrowingAuthorizer{transferGraph: g}
		s.auth = narrowed

		_, err = s.Export(t.Context(), exportPrincipal("agent"), scope, first.Continuation)

		require.ErrorIs(t, err, domain.ErrForbidden)
	})
	for _, tc := range []struct {
		name  string
		p     domain.Principal
		scope domain.Scope
		tamp  func(string) string
	}{
		{"cursor from another client is stale", exportPrincipal("other"), scope, func(c string) string { return c }},
		{"cursor for another scope is stale", exportPrincipal("agent"), domain.Scope{ProjectID: "demo", TaskID: "t"}, func(c string) string { return c }},
		{"tampered cursor is stale", exportPrincipal("agent"), scope, func(c string) string { return "x" + c }},
		{"malformed cursor is stale", exportPrincipal("agent"), scope, func(string) string { return "not-a-cursor" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, g := newTransferFixture(t, claims...)
			first, err := s.Export(t.Context(), exportPrincipal("agent"), scope, "")
			require.NoError(t, err)

			_, err = s.Export(t.Context(), tc.p, tc.scope, tc.tamp(first.Continuation))

			require.ErrorIs(t, err, domain.ErrStale)
			assert.Len(t, g.pageAfters, 1)
		})
	}
}

type narrowingAuthorizer struct{ *transferGraph }

func (n *narrowingAuthorizer) Snapshot(_ context.Context, _ Tx, p domain.Principal, _ domain.Scope) (RetrievalSnapshot, error) {
	p.Capabilities = []string{"retrieval:read"}
	return RetrievalSnapshot{Principal: p}, nil
}

func TestTransferExportRejectsBeforeReading(t *testing.T) {
	for _, tc := range []struct {
		name   string
		p      domain.Principal
		scope  domain.Scope
		mutate func(*transferGraph)
		want   error
	}{
		{"missing export capability", domain.Principal{ClientID: "agent", ProjectIDs: []string{"demo"}, Capabilities: []string{"retrieval:read"}}, domain.Scope{ProjectID: "demo"}, nil, domain.ErrForbidden},
		{"ungranted project", exportPrincipal("agent"), domain.Scope{ProjectID: "private"}, nil, domain.ErrForbidden},
		{"empty project", exportPrincipal("agent"), domain.Scope{}, nil, domain.ErrInvalidInput},
		{"blank task", exportPrincipal("agent"), domain.Scope{ProjectID: "demo", TaskID: " "}, nil, domain.ErrInvalidInput},
		{"page failure", exportPrincipal("agent"), domain.Scope{ProjectID: "demo"}, func(g *transferGraph) { g.pageErr = domain.ErrUnavailable }, domain.ErrUnavailable},
		{"eligibility failure", exportPrincipal("agent"), domain.Scope{ProjectID: "demo"}, func(g *transferGraph) { g.eligibleErr = domain.ErrStale }, domain.ErrStale},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, g := newTransferFixture(t, exportClaim("a", domain.ClaimVerified, domain.TrustSubmitted))
			if tc.mutate != nil {
				tc.mutate(g)
			}

			_, err := s.Export(t.Context(), tc.p, tc.scope, "")

			require.ErrorIs(t, err, tc.want)
			if tc.mutate == nil {
				assert.Empty(t, g.pageAfters)
			}
		})
	}
}

func TestNewTransferServiceRequiresKey(t *testing.T) {
	_, err := NewTransferService(directTx{}, &transferGraph{}, &transferGraph{}, []byte("short"))

	require.ErrorIs(t, err, domain.ErrUnavailable)
}
