package repository

import (
	"context"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
)

// OAuthRepository stores OAuth client registrations as OAuthRegistration
// nodes.
type OAuthRepository struct{ store *graph.Store }

func NewOAuthRepository(store *graph.Store) *OAuthRepository {
	return &OAuthRepository{store: store}
}

func (r *OAuthRepository) query(ctx context.Context, tx domain.Tx, statement string, params map[string]any) ([]*neo4j.Record, error) {
	native, err := r.store.Unwrap(tx)
	if err != nil {
		return nil, err
	}
	result, err := native.Run(ctx, statement, params)
	if err != nil {
		return nil, domain.ErrUnavailable
	}
	rows, err := result.Collect(ctx)
	if err != nil {
		return nil, domain.ErrUnavailable
	}
	return rows, nil
}

// AddRegistration stores reg unless maxPending unexpired, unapproved
// registrations already exist, in which case it returns ErrBudget. Writing the
// registrations limit node first holds its lock until the transaction ends,
// so concurrent registrations count and create one at a time.
func (r *OAuthRepository) AddRegistration(ctx context.Context, tx domain.Tx, reg domain.OAuthRegistration, maxPending int) error {
	rows, err := r.query(ctx, tx, `MERGE (l:OAuthLimit {id:'registrations'}) SET l.at = $created
WITH l
OPTIONAL MATCH (p:OAuthRegistration) WHERE p.approved = false AND p.expiresAt > $created
WITH count(p) AS pending WHERE pending < $max
CREATE (reg:OAuthRegistration {id:$id, redirectURIs:$uris, clientName:$name, createdAt:$created, expiresAt:$expires, approved:false})
RETURN reg.id`, map[string]any{"id": reg.ID, "uris": reg.RedirectURIs, "name": reg.ClientName, "created": reg.CreatedAt.UTC().UnixNano(), "expires": reg.ExpiresAt.UTC().UnixNano(), "max": int64(maxPending)})
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrBudget
	}
	return nil
}

// FindRegistration returns a registration that is approved or has not yet
// expired. An unknown or expired one is ErrUnauthorized, as an unknown client.
func (r *OAuthRepository) FindRegistration(ctx context.Context, tx domain.Tx, id string, now time.Time) (domain.OAuthRegistration, error) {
	rows, err := r.query(ctx, tx, `MATCH (reg:OAuthRegistration {id:$id}) WHERE reg.approved = true OR reg.expiresAt > $now
RETURN reg.redirectURIs, reg.clientName, reg.createdAt, reg.expiresAt`, map[string]any{"id": id, "now": now.UTC().UnixNano()})
	if err != nil {
		return domain.OAuthRegistration{}, err
	}
	if len(rows) != 1 {
		return domain.OAuthRegistration{}, domain.ErrUnauthorized
	}
	v := rows[0].Values
	uris, ok1 := stringList(v[0])
	name, ok2 := v[1].(string)
	created, ok3 := v[2].(int64)
	expires, ok4 := v[3].(int64)
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return domain.OAuthRegistration{}, domain.ErrUnavailable
	}
	return domain.OAuthRegistration{ID: id, RedirectURIs: uris, ClientName: name, CreatedAt: time.Unix(0, created).UTC(), ExpiresAt: time.Unix(0, expires).UTC()}, nil
}

// PruneRegistrations deletes up to limit unapproved registrations that
// expired at or before now, and returns how many it deleted.
func (r *OAuthRepository) PruneRegistrations(ctx context.Context, tx domain.Tx, now time.Time, limit int) (int, error) {
	if limit < 1 {
		return 0, domain.ErrInvalidInput
	}
	rows, err := r.query(ctx, tx, `MATCH (reg:OAuthRegistration) WHERE reg.approved = false AND reg.expiresAt <= $now
WITH reg LIMIT $limit DETACH DELETE reg RETURN count(*)`, map[string]any{"now": now.UTC().UnixNano(), "limit": int64(limit)})
	if err != nil {
		return 0, err
	}
	if len(rows) != 1 {
		return 0, domain.ErrUnavailable
	}
	n, ok := rows[0].Values[0].(int64)
	if !ok {
		return 0, domain.ErrUnavailable
	}
	return int(n), nil
}

const oauthRequestFields = `r.id, r.approvalCode, r.clientID, r.clientName, r.redirectURI, r.state, r.codeChallenge, r.requestedProjects, r.requestedCapabilities, r.status, r.grantedProjects, r.grantedCapabilities, r.grantTTL, r.cookieDigest, r.codeDigest, r.codeExpiresAt, r.released, r.createdAt, r.expiresAt`

// AddRequest stores a pending grant request unless maxPending pending requests
// are unexpired (ErrBudget) or an unexpired request already holds its approval
// code (ErrConflict).
func (r *OAuthRepository) AddRequest(ctx context.Context, tx domain.Tx, req domain.OAuthRequest, maxPending int) error {
	rows, err := r.query(ctx, tx, `OPTIONAL MATCH (p:OAuthRequest) WHERE p.status = 'pending' AND p.expiresAt > $created
WITH count(p) AS pending
OPTIONAL MATCH (c:OAuthRequest {approvalCode:$code}) WHERE c.expiresAt > $created
WITH pending, count(c) AS clash
RETURN pending, clash`, map[string]any{"created": req.CreatedAt.UTC().UnixNano(), "code": req.ApprovalCode})
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrUnavailable
	}
	pending, ok1 := rows[0].Values[0].(int64)
	clash, ok2 := rows[0].Values[1].(int64)
	switch {
	case !ok1 || !ok2:
		return domain.ErrUnavailable
	case pending >= int64(maxPending):
		return domain.ErrBudget
	case clash > 0:
		return domain.ErrConflict
	}
	_, err = r.query(ctx, tx, `CREATE (r:OAuthRequest {id:$id, approvalCode:$code, clientID:$client, clientName:$name, redirectURI:$redirect, state:$state, codeChallenge:$challenge, requestedProjects:$projects, requestedCapabilities:$capabilities, status:'pending', grantedProjects:[], grantedCapabilities:[], grantTTL:0, cookieDigest:$cookie, codeDigest:'', codeExpiresAt:0, released:false, createdAt:$created, expiresAt:$expires})`, map[string]any{
		"id": req.ID, "code": req.ApprovalCode, "client": req.ClientID, "name": req.ClientName, "redirect": req.RedirectURI, "state": req.State, "challenge": req.CodeChallenge,
		"projects": nonNil(req.Requested.Projects), "capabilities": nonNil(req.Requested.Capabilities), "cookie": req.CookieDigest,
		"created": req.CreatedAt.UTC().UnixNano(), "expires": req.ExpiresAt.UTC().UnixNano(),
	})
	return err
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// FindRequest returns a grant request by ID, or ErrForbidden.
func (r *OAuthRepository) FindRequest(ctx context.Context, tx domain.Tx, id string) (domain.OAuthRequest, error) {
	rows, err := r.query(ctx, tx, `MATCH (r:OAuthRequest {id:$id}) RETURN `+oauthRequestFields, map[string]any{"id": id})
	if err != nil {
		return domain.OAuthRequest{}, err
	}
	if len(rows) != 1 {
		return domain.OAuthRequest{}, domain.ErrForbidden
	}
	return decodeOAuthRequest(rows[0])
}

// FindPendingRequest returns the pending, unexpired request holding an
// approval code, or ErrForbidden.
func (r *OAuthRepository) FindPendingRequest(ctx context.Context, tx domain.Tx, code string, now time.Time) (domain.OAuthRequest, error) {
	rows, err := r.query(ctx, tx, `MATCH (r:OAuthRequest {approvalCode:$code}) WHERE r.status = 'pending' AND r.expiresAt > $now RETURN `+oauthRequestFields, map[string]any{"code": code, "now": now.UTC().UnixNano()})
	if err != nil {
		return domain.OAuthRequest{}, err
	}
	if len(rows) != 1 {
		return domain.OAuthRequest{}, domain.ErrForbidden
	}
	return decodeOAuthRequest(rows[0])
}

// ListPendingRequests returns pending, unexpired requests, oldest first.
func (r *OAuthRepository) ListPendingRequests(ctx context.Context, tx domain.Tx, now time.Time, limit int) ([]domain.OAuthRequest, error) {
	rows, err := r.query(ctx, tx, `MATCH (r:OAuthRequest) WHERE r.status = 'pending' AND r.expiresAt > $now RETURN `+oauthRequestFields+` ORDER BY r.createdAt, r.id LIMIT $limit`, map[string]any{"now": now.UTC().UnixNano(), "limit": int64(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]domain.OAuthRequest, 0, len(rows))
	for _, row := range rows {
		req, err := decodeOAuthRequest(row)
		if err != nil {
			return nil, err
		}
		out = append(out, req)
	}
	return out, nil
}

// DecideRequest records the owner's decision on a pending, unexpired request.
// A request decided already, or expired, is ErrConflict.
func (r *OAuthRepository) DecideRequest(ctx context.Context, tx domain.Tx, id string, status domain.OAuthRequestStatus, granted domain.OAuthScope, ttl time.Duration, now time.Time) error {
	rows, err := r.query(ctx, tx, `MATCH (r:OAuthRequest {id:$id}) WHERE r.status = 'pending' AND r.expiresAt > $now
SET r.status = $status, r.grantedProjects = $projects, r.grantedCapabilities = $capabilities, r.grantTTL = $ttl
RETURN r.id`, map[string]any{"id": id, "now": now.UTC().UnixNano(), "status": string(status), "projects": nonNil(granted.Projects), "capabilities": nonNil(granted.Capabilities), "ttl": int64(ttl)})
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrConflict
	}
	return nil
}

// ReleaseRequest marks a request's outcome as handed to its browser, once, and
// stores the authorization code digest when there is one. A request released
// already is ErrConflict.
func (r *OAuthRepository) ReleaseRequest(ctx context.Context, tx domain.Tx, id, codeDigest string, codeExpires time.Time) error {
	expires := int64(0)
	if !codeExpires.IsZero() {
		expires = codeExpires.UTC().UnixNano()
	}
	rows, err := r.query(ctx, tx, `MATCH (r:OAuthRequest {id:$id}) WHERE r.released = false
SET r.released = true, r.codeDigest = $digest, r.codeExpiresAt = $expires
RETURN r.id`, map[string]any{"id": id, "digest": codeDigest, "expires": expires})
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrConflict
	}
	return nil
}

// PruneRequests deletes up to limit requests that expired at or before
// before, and returns how many it deleted.
func (r *OAuthRepository) PruneRequests(ctx context.Context, tx domain.Tx, before time.Time, limit int) (int, error) {
	if limit < 1 {
		return 0, domain.ErrInvalidInput
	}
	rows, err := r.query(ctx, tx, `MATCH (r:OAuthRequest) WHERE r.expiresAt <= $before
WITH r LIMIT $limit DETACH DELETE r RETURN count(*)`, map[string]any{"before": before.UTC().UnixNano(), "limit": int64(limit)})
	if err != nil {
		return 0, err
	}
	if len(rows) != 1 {
		return 0, domain.ErrUnavailable
	}
	n, ok := rows[0].Values[0].(int64)
	if !ok {
		return 0, domain.ErrUnavailable
	}
	return int(n), nil
}

func decodeOAuthRequest(row *neo4j.Record) (domain.OAuthRequest, error) {
	v := row.Values
	if len(v) != 19 {
		return domain.OAuthRequest{}, domain.ErrUnavailable
	}
	var req domain.OAuthRequest
	strs := []*string{&req.ID, &req.ApprovalCode, &req.ClientID, &req.ClientName, &req.RedirectURI, &req.State, &req.CodeChallenge}
	for i, dst := range strs {
		s, ok := v[i].(string)
		if !ok {
			return domain.OAuthRequest{}, domain.ErrUnavailable
		}
		*dst = s
	}
	var ok [12]bool
	req.Requested.Projects, ok[0] = stringList(v[7])
	req.Requested.Capabilities, ok[1] = stringList(v[8])
	var status string
	status, ok[2] = v[9].(string)
	req.Status = domain.OAuthRequestStatus(status)
	req.Granted.Projects, ok[3] = stringList(v[10])
	req.Granted.Capabilities, ok[4] = stringList(v[11])
	var ttl, codeExpires, created, expires int64
	ttl, ok[5] = v[12].(int64)
	req.CookieDigest, ok[6] = v[13].(string)
	req.CodeDigest, ok[7] = v[14].(string)
	codeExpires, ok[8] = v[15].(int64)
	req.Released, ok[9] = v[16].(bool)
	created, ok[10] = v[17].(int64)
	expires, ok[11] = v[18].(int64)
	for _, good := range ok {
		if !good {
			return domain.OAuthRequest{}, domain.ErrUnavailable
		}
	}
	req.GrantTTL = time.Duration(ttl)
	if codeExpires != 0 {
		req.CodeExpiresAt = time.Unix(0, codeExpires).UTC()
	}
	req.CreatedAt = time.Unix(0, created).UTC()
	req.ExpiresAt = time.Unix(0, expires).UTC()
	return req, nil
}
