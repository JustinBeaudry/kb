package repository

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
)

type RetrievalRepository struct{ store *graph.Store }

func NewRetrievalRepository(s *graph.Store) *RetrievalRepository { return &RetrievalRepository{s} }
func (r *RetrievalRepository) query(ctx context.Context, tx domain.Tx, q string, p map[string]any) ([]*neo4j.Record, error) {
	native, e := r.store.Unwrap(tx)
	if e != nil {
		return nil, e
	}
	res, e := native.Run(ctx, q, p)
	if e != nil {
		return nil, domain.ErrUnavailable
	}
	rows, e := res.Collect(ctx)
	if e != nil {
		return nil, domain.ErrUnavailable
	}
	return rows, nil
}
func (r *RetrievalRepository) Snapshot(ctx context.Context, tx domain.Tx, p domain.Principal, scope domain.Scope) (domain.RetrievalSnapshot, error) {
	rows, e := r.query(ctx, tx, `MATCH (o:OwnerState {id:'owner'}),(c:Client {id:$client}) OPTIONAL MATCH (t:TaskCheckpointOwner {projectID:$project,taskID:$task}) RETURN o.authEpoch,o.revisionEpoch,o.deletionEpoch,c.ownerID,c.role,c.projectIDs,c.capabilities,c.revokedAt,t.clientID,t.headRevision`, map[string]any{"client": p.ClientID, "project": scope.ProjectID, "task": scope.TaskID})
	if e != nil {
		return domain.RetrievalSnapshot{}, e
	}
	if len(rows) != 1 {
		return domain.RetrievalSnapshot{}, domain.ErrUnauthorized
	}
	v := rows[0].Values
	auth, a := v[0].(int64)
	knowledge, b := v[1].(int64)
	deletion, c := v[2].(int64)
	owner, d := v[3].(string)
	role, f := v[4].(string)
	projects, g := stringList(v[5])
	caps, h := stringList(v[6])
	if !a || !b || !c || !d || !f || !g || !h || auth < 0 || knowledge < 0 || deletion < 0 {
		return domain.RetrievalSnapshot{}, domain.ErrUnavailable
	}
	if v[7] != nil {
		return domain.RetrievalSnapshot{}, domain.ErrUnauthorized
	}
	principal := domain.Principal{ClientID: p.ClientID, OwnerID: owner, Role: domain.Role(role), ProjectIDs: projects, Capabilities: caps, AuthEpoch: uint64(auth)}
	if p.AuthEpoch != uint64(auth) {
		return domain.RetrievalSnapshot{}, domain.ErrStale
	}
	s := domain.RetrievalSnapshot{Principal: principal, AuthEpoch: uint64(auth), KnowledgeEpoch: uint64(knowledge), DeletionEpoch: uint64(deletion)}
	if v[8] != nil {
		s.TaskOwner, _ = v[8].(string)
	}
	if v[9] != nil {
		s.TaskHead, _ = v[9].(string)
	}
	return s, nil
}
func (r *RetrievalRepository) Seeds(ctx context.Context, tx domain.Tx, scope domain.Scope, query string, limit int) ([]domain.ClaimRevision, error) {
	if limit < 1 || limit > 50 {
		return nil, domain.ErrInvalidInput
	}
	params := map[string]any{"project": scope.ProjectID, "task": scope.TaskID, "limit": limit, "query": strings.ToLower(query), "exact": query}
	where := `(c.projectID=$project AND (coalesce(c.taskID,'')='' OR c.taskID=$task)) OR EXISTS { MATCH (p:Publication {claimRevision:c.id,sharingScope:'cross_project',valid:true}) }`
	if strings.HasPrefix(query, "@ids:") {
		var ids []string
		if json.Unmarshal([]byte(strings.TrimPrefix(query, "@ids:")), &ids) != nil || len(ids) > 50 {
			return nil, domain.ErrInvalidInput
		}
		params["ids"] = ids
		where = "(" + where + ") AND EXISTS { MATCH (c)-[:ABOUT]->(e:Entity) WHERE e.id IN $ids AND (e.projectID=$project OR EXISTS {MATCH (p:Publication {claimRevision:c.id,sharingScope:'cross_project',valid:true}) WHERE p.generalizedText=c.text AND c.kind='lesson'}) }"
	} else if strings.HasPrefix(query, "@artifact:") {
		params["artifact"] = strings.TrimPrefix(query, "@artifact:")
		where = "(" + where + ") AND EXISTS { MATCH (c)-[:EVIDENCE]->(:Artifact {id:$artifact}) }"
	} else {
		exact, e := r.query(ctx, tx, `MATCH (c:ClaimRevision) WHERE (`+where+`) AND (c.id=$exact OR c.claimID=$exact) RETURN c.data ORDER BY c.id LIMIT $limit`, params)
		if e != nil {
			return nil, e
		}
		if len(exact) > 0 {
			return decodeClaims(exact, nil)
		}
		where = "(" + where + ") AND ($query='' OR toLower(c.text) CONTAINS $query OR c.id=$exact OR c.claimID=$exact)"
	}
	// LIMIT executes before JSON decoding. Returned records, including those later
	// rejected by eligibility, count against the service's examined-record bound.
	rows, e := r.query(ctx, tx, `MATCH (c:ClaimRevision) WHERE `+where+` RETURN c.data ORDER BY c.id LIMIT $limit`, params)
	return decodeClaims(rows, e)
}

// CitationSeeds selects only citation records that can possibly authorize this
// fetch, before applying the fixed record bound. Hitting the bound is treated
// as potentially incomplete. Eligible remains the authoritative security gate.
func (r *RetrievalRepository) CitationSeeds(ctx context.Context, tx domain.Tx, p domain.Principal, scope domain.Scope, mode domain.FetchMode, ref domain.EvidenceRef, limit int) ([]domain.ClaimRevision, bool, error) {
	if limit < 1 || limit > domain.MaxGraphVisited || domain.ValidateIDs(scope.ProjectID, ref.ArtifactID, ref.Digest, ref.Locator) != nil {
		return nil, false, domain.ErrInvalidInput
	}
	where := ""
	switch mode {
	case domain.FetchPublication:
		if scope.TaskID != "" {
			return nil, false, domain.ErrInvalidInput
		}
		where = `coalesce(c.taskID,'')='' AND c.status IN ['verified','disputed'] AND coalesce(c.valid,false) AND EXISTS { MATCH (:Publication {claimRevision:c.id,valid:true}) }`
	case domain.FetchWorking:
		if domain.ValidateIDs(scope.TaskID, p.ClientID) != nil {
			return nil, false, domain.ErrInvalidInput
		}
		where = `c.taskID=$task AND c.clientID=$client AND c.status='candidate'`
	default:
		return nil, false, domain.ErrInvalidInput
	}
	rows, err := r.query(ctx, tx, `MATCH (a:Artifact {id:$artifact})<-[ref:EVIDENCE]-(c:ClaimRevision) WHERE c.projectID=$project AND ref.digest=$digest AND ref.locator=$locator AND `+where+` WITH DISTINCT c ORDER BY c.id LIMIT $limit RETURN c.data`, map[string]any{"artifact": ref.ArtifactID, "project": scope.ProjectID, "task": scope.TaskID, "client": p.ClientID, "digest": ref.Digest, "locator": ref.Locator, "limit": limit})
	if err != nil {
		return nil, false, err
	}
	boundReached := len(rows) == limit
	claims, err := decodeClaims(rows, nil)
	return claims, boundReached, err
}
func (r *RetrievalRepository) Neighbors(ctx context.Context, tx domain.Tx, ids, kinds []string, limit int) ([]domain.ClaimRevision, error) {
	if limit < 1 || limit > 50 || len(ids) > 50 {
		return nil, domain.ErrInvalidInput
	}
	for _, k := range kinds {
		if !slices.Contains([]string{"SUPPORTS", "CONTRADICTS", "SUPERSEDES", "RELATED"}, k) {
			return nil, domain.ErrInvalidInput
		}
	}
	rows, e := r.query(ctx, tx, `UNWIND $ids AS id MATCH (c:ClaimRevision {id:id})-[r:SUPPORTS|CONTRADICTS|SUPERSEDES|RELATED]-(n:ClaimRevision) WHERE type(r) IN $kinds RETURN n.data LIMIT $limit`, map[string]any{"ids": ids, "kinds": kinds, "limit": limit})
	return decodeClaims(rows, e)
}
func decodeClaims(rows []*neo4j.Record, e error) ([]domain.ClaimRevision, error) {
	if e != nil {
		return nil, e
	}
	claims := make([]domain.ClaimRevision, 0, len(rows))
	for _, row := range rows {
		var c domain.ClaimRevision
		if decodeCapture(row, 0, &c) != nil {
			return nil, domain.ErrUnavailable
		}
		claims = append(claims, c)
	}
	return claims, nil
}

type eligibilityRecord struct {
	claim             domain.ClaimRevision
	status            domain.ClaimStatus
	valid, deleted    bool
	client            string
	support           []string
	published, global bool
	generalized       string
}

func (r *RetrievalRepository) records(ctx context.Context, tx domain.Tx, ids []string) (map[string]eligibilityRecord, error) {
	rows, e := r.query(ctx, tx, `UNWIND $ids AS id MATCH (c:ClaimRevision {id:id}) CALL (c) { OPTIONAL MATCH (p:Publication {claimRevision:c.id,valid:true}) RETURN p LIMIT 2 } RETURN c.data,c.status,coalesce(c.valid,false),EXISTS { MATCH (:Tombstone {id:c.id}) } OR EXISTS { MATCH (:Tombstone {id:c.claimID}) },c.clientID,CASE WHEN size(p.supportRevisions)<=1000 THEN p.supportRevisions ELSE null END,p.sharingScope,p.generalizedText`, map[string]any{"ids": ids})
	if e != nil {
		return nil, e
	}
	out := map[string]eligibilityRecord{}
	for _, row := range rows {
		var c domain.ClaimRevision
		if decodeCapture(row, 0, &c) != nil {
			return nil, domain.ErrUnavailable
		}
		v := row.Values
		status, ok := v[1].(string)
		if !ok {
			return nil, domain.ErrUnavailable
		}
		record := eligibilityRecord{claim: c, status: domain.ClaimStatus(status)}
		record.valid, _ = v[2].(bool)
		record.deleted, _ = v[3].(bool)
		record.client, _ = v[4].(string)
		if v[5] != nil {
			var ok bool
			record.support, ok = stringList(v[5])
			if !ok {
				return nil, domain.ErrUnavailable
			}
			record.published = true
		}
		record.global = v[6] == "cross_project"
		record.generalized, _ = v[7].(string)
		if _, exists := out[c.Revision]; exists {
			return nil, domain.ErrUnavailable
		}
		out[c.Revision] = record
	}
	return out, nil
}
func (r *RetrievalRepository) sources(ctx context.Context, tx domain.Tx, claims []domain.ClaimRevision) (bool, error) {
	for _, c := range claims {
		if (len(c.Evidence) == 0 && c.Status != domain.ClaimCandidate) || len(c.Evidence) > 50 {
			return false, nil
		}
		for _, ref := range c.Evidence {
			rows, e := r.query(ctx, tx, `MATCH (a:Artifact {id:$id}) OPTIONAL MATCH (p:CapturePolicy {projectID:a.projectID}) RETURN a.data,a.state,coalesce(a.available,false),p.data,EXISTS { MATCH (:Tombstone {id:a.id}) }`, map[string]any{"id": ref.ArtifactID})
			if e != nil {
				return false, e
			}
			if len(rows) != 1 {
				return false, nil
			}
			var a domain.Artifact
			var p domain.Policy
			if decodeCapture(rows[0], 0, &a) != nil || decodeCapture(rows[0], 3, &p) != nil {
				return false, nil
			}
			v := rows[0].Values
			if v[1] != string(domain.ArtifactReady) || v[2] != true || v[4] == true || a.Digest != ref.Digest || a.SourceLocator != ref.Locator || a.Scope != c.Scope || p.Revision == "" || p.Revision != a.PolicyRevision || p.Paused {
				return false, nil
			}
			for _, excluded := range p.ExcludedPaths {
				prefix := strings.TrimSuffix(excluded, "/")
				if prefix == "" || a.SourcePath == "" || a.SourcePath == prefix || strings.HasPrefix(a.SourcePath, prefix+"/") {
					return false, nil
				}
			}
		}
	}
	return true, nil
}
func (r *RetrievalRepository) Eligible(ctx context.Context, tx domain.Tx, p domain.Principal, requested domain.Scope, ids []string) (domain.EligibilityResult, error) {
	if len(ids) > 50 || domain.ValidateIDs(requested.ProjectID) != nil {
		return domain.EligibilityResult{}, domain.ErrInvalidInput
	}
	snap, e := r.Snapshot(ctx, tx, p, domain.Scope{})
	if e != nil {
		return domain.EligibilityResult{}, e
	}
	result := domain.EligibilityResult{Snapshot: snap}
	records, e := r.records(ctx, tx, ids)
	if e != nil {
		return result, e
	}
	for _, id := range ids {
		record, ok := records[id]
		if !ok || record.deleted {
			continue
		}
		c := record.claim
		c.Status = record.status
		authorized := slices.Contains(snap.Principal.ProjectIDs, c.Scope.ProjectID)
		sameProject := c.Scope.ProjectID == requested.ProjectID
		if c.Status == domain.ClaimCandidate {
			if !authorized || !sameProject || record.client != p.ClientID || c.Scope.TaskID == "" {
				continue
			}
			ownerRows, e := r.query(ctx, tx, `MATCH (t:TaskCheckpointOwner {projectID:$project,taskID:$task,clientID:$client}) RETURN t.clientID`, map[string]any{"project": c.Scope.ProjectID, "task": c.Scope.TaskID, "client": p.ClientID})
			if e != nil {
				return result, e
			}
			if len(ownerRows) != 1 {
				continue
			}
		} else if (c.Status != domain.ClaimVerified && c.Status != domain.ClaimDisputed) || !record.valid || !record.published || c.Scope.TaskID != "" || (sameProject && !authorized) || (!sameProject && (!record.global || record.generalized == "" || c.Kind != "lesson" || c.Text != record.generalized)) {
			continue
		}

		all, valid, e := r.supportClosure(ctx, tx, record)
		if e != nil {
			return result, e
		}
		if !valid {
			continue
		}
		usable, e := r.sources(ctx, tx, all)
		if e != nil {
			return result, e
		}
		if !usable {
			if sameProject && authorized {
				result.Unavailable = true
			}
			continue
		}
		if !sameProject {
			c.Text = record.generalized
			c.ClaimID = "lesson:" + c.Revision
			c.Scope = domain.Scope{}
			c.Evidence = nil
			c.DependsOn = nil
			c.EntityIDs = nil
		} else { // task sources cannot enter project-wide publications even via imported data
			safe := true
			for _, ref := range c.Evidence {
				if ref.Origin.ClientID == "" {
					safe = false
				}
			}
			if !safe {
				continue
			}
		}
		result.Claims = append(result.Claims, c)
	}
	return result, nil
}

// supportClosure checks indexed authoritative validity separately from graph
// expansion. At most 1000 distinct supporting revisions are hydrated per target.
func (r *RetrievalRepository) supportClosure(ctx context.Context, tx domain.Tx, record eligibilityRecord) ([]domain.ClaimRevision, bool, error) {
	c := record.claim
	c.Status = record.status
	ids := slices.Clone(record.support)
	if record.status == domain.ClaimCandidate {
		ids = slices.Clone(c.DependsOn)
		direct, e := r.records(ctx, tx, ids)
		if e != nil {
			return nil, false, e
		}
		for _, dep := range direct {
			if !dep.published {
				return nil, false, nil
			}
			for _, ancestor := range dep.support {
				if !slices.Contains(ids, ancestor) {
					ids = append(ids, ancestor)
				}
				if len(ids) > domain.MaxSupportReferences {
					return nil, false, nil
				}
			}
		}
	}
	if len(ids) > domain.MaxSupportReferences || slices.Contains(ids, c.Revision) || len(slices.Compact(slices.Sorted(slices.Values(ids)))) != len(ids) {
		return nil, false, nil
	}
	supports, e := r.records(ctx, tx, ids)
	if e != nil {
		return nil, false, e
	}
	if len(supports) != len(ids) {
		return nil, false, nil
	}
	for _, id := range c.DependsOn {
		if !slices.Contains(ids, id) {
			return nil, false, nil
		}
	}
	all := []domain.ClaimRevision{c}
	visiting := map[string]bool{}
	visited := map[string]bool{}
	var visit func(string) bool
	visit = func(id string) bool {
		if visiting[id] {
			return false
		}
		if visited[id] {
			return true
		}
		dep, ok := supports[id]
		if !ok || dep.deleted || !dep.valid || dep.status != domain.ClaimVerified || len(dep.claim.DependsOn) > domain.MaxSupportReferences {
			return false
		}
		visiting[id] = true
		for _, ancestor := range dep.claim.DependsOn {
			if !visit(ancestor) {
				return false
			}
		}
		delete(visiting, id)
		visited[id] = true
		return true
	}
	for id, dep := range supports {
		if !visit(id) {
			return nil, false, nil
		}
		dep.claim.Status = dep.status
		all = append(all, dep.claim)
	}
	return all, true, nil
}
