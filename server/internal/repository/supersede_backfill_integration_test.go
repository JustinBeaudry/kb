package repository_test

import (
	"context"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Data written before publication superseded prior revisions can hold several
// verified project revisions of one claim. The schema backfill keeps only the
// newest publication verified and is safe to rerun on every startup.
func TestSupersedeBackfillKeepsNewestPublishedRevision(t *testing.T) {
	store, _, _ := jobsGraph(t)
	type revision struct {
		id, claim, project, task, status string
		checkedAt                        string
		head                             bool
	}
	seed := []revision{
		// Head is verified: it wins even though an older row sorts after it by id.
		{id: "a-z-old", claim: "claim-a", project: "demo", status: "verified", checkedAt: "2026-01-01T00:00:00Z"},
		{id: "a-a-new", claim: "claim-a", project: "demo", status: "verified", checkedAt: "2026-02-01T00:00:00.5Z", head: true},
		// Head was invalidated: the most recently published verified revision wins.
		{id: "b-1", claim: "claim-b", project: "demo", status: "verified", checkedAt: "2026-01-01T00:00:00Z"},
		{id: "b-2", claim: "claim-b", project: "demo", status: "verified", checkedAt: "2026-03-01T00:00:00.123456789Z"},
		{id: "b-3", claim: "claim-b", project: "demo", status: "invalidated", checkedAt: "2026-04-01T00:00:00Z", head: true},
		// Disputed and task-scoped revisions are never retired by the backfill.
		{id: "c-1", claim: "claim-c", project: "demo", status: "disputed", checkedAt: "2026-01-01T00:00:00Z"},
		{id: "c-2", claim: "claim-c", project: "demo", status: "verified", checkedAt: "2026-02-01T00:00:00Z", head: true},
		{id: "c-task", claim: "claim-c", project: "demo", task: "task", status: "verified", checkedAt: "2026-03-01T00:00:00Z"},
		// A lone verified revision, and the same claim id in another project.
		{id: "d-1", claim: "claim-d", project: "demo", status: "verified", checkedAt: "2026-01-01T00:00:00Z", head: true},
		{id: "d-other", claim: "claim-d", project: "other", status: "verified", checkedAt: "2025-01-01T00:00:00Z"},
		// Without a parseable verification time the id breaks the tie.
		{id: "e-1", claim: "claim-e", project: "demo", status: "verified"},
		{id: "e-2", claim: "claim-e", project: "demo", status: "verified", checkedAt: "not-a-time"},
	}
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		native, e := store.Unwrap(tx)
		if e != nil {
			return e
		}
		for _, r := range seed {
			if _, e = native.Run(ctx, `MERGE (h:Claim {id:$claim}) CREATE (c:ClaimRevision {id:$id,claimID:$claim,projectID:$project,taskID:$task,status:$status,valid:true}) FOREACH (_ IN CASE WHEN $head THEN [1] ELSE [] END | CREATE (h)-[:HEAD]->(c)) FOREACH (_ IN CASE WHEN $checked<>'' THEN [1] ELSE [] END | CREATE (c)-[:VERIFIED_BY]->(:Verification {id:$id+'-v',claimRevision:$id,data:'{"ID":"'+$id+'-v","Method":"owner_adjudicated","CheckedAt":"'+$checked+'"}'}))`, map[string]any{"id": r.id, "claim": r.claim, "project": r.project, "task": r.task, "status": r.status, "head": r.head, "checked": r.checkedAt}); e != nil {
				return e
			}
		}
		return nil
	}))
	statuses := func() map[string]string {
		t.Helper()
		out := map[string]string{}
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			native, e := store.Unwrap(tx)
			if e != nil {
				return e
			}
			res, e := native.Run(ctx, `MATCH (c:ClaimRevision) RETURN c.id, c.status`, nil)
			if e != nil {
				return e
			}
			rows, e := res.Collect(ctx)
			if e != nil {
				return e
			}
			for _, row := range rows {
				out[row.Values[0].(string)] = row.Values[1].(string)
			}
			return nil
		}))
		return out
	}
	want := map[string]string{
		"a-z-old": "superseded", "a-a-new": "verified",
		"b-1": "superseded", "b-2": "verified", "b-3": "invalidated",
		"c-1": "disputed", "c-2": "verified", "c-task": "verified",
		"d-1": "verified", "d-other": "verified",
		"e-1": "superseded", "e-2": "verified",
	}

	for run := range 2 {
		require.NoError(t, store.InitializeSchema(t.Context()))

		assert.Equal(t, want, statuses(), "run %d", run+1)
	}
}
