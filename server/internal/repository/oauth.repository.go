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

const oauthRequestFields = `r.id, r.approvalCode, r.clientID, r.clientName, r.redirectURI, r.state, r.codeChallenge, r.requestedProjects, r.requestedCapabilities, r.status, r.grantedProjects, r.grantedCapabilities, r.grantTTL, r.cookieDigest, r.codeDigest, r.codeExpiresAt, r.released, r.createdAt, r.expiresAt, coalesce(r.codeUsed, false), coalesce(r.grantID, '')`

// AddRequest stores a pending grant request unless maxPending pending requests
// are unexpired (ErrBudget) or an unexpired request already holds its approval
// code (ErrConflict). The requests limit node is written first, so concurrent
// requests check and create one at a time.
func (r *OAuthRepository) AddRequest(ctx context.Context, tx domain.Tx, req domain.OAuthRequest, maxPending int) error {
	rows, err := r.query(ctx, tx, `MERGE (l:OAuthLimit {id:'requests'}) SET l.at = $created
WITH l
OPTIONAL MATCH (p:OAuthRequest) WHERE p.status = 'pending' AND p.expiresAt > $created
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
	rows, err := r.query(ctx, tx, `MATCH (r:OAuthRequest {id:$id}) SET r.lockedAt = timestamp()
WITH r WHERE r.status = 'pending' AND r.expiresAt > $now
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
	// Writing the node first takes its lock, so the released check below sees
	// any concurrent release that committed first.
	rows, err := r.query(ctx, tx, `MATCH (r:OAuthRequest {id:$id}) SET r.lockedAt = timestamp()
WITH r WHERE r.released = false
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
	if len(v) != 21 {
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
	var ok [14]bool
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
	req.CodeUsed, ok[12] = v[19].(bool)
	req.GrantID, ok[13] = v[20].(string)
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

// ClaimCode finds the released, approved request holding an authorization
// code digest and marks the code used, recording the grant it will produce.
// The request node is written first, so of two concurrent exchanges one sees
// the other's claim. A code already used returns the request with
// ErrConflict, so its grant can be revoked; an unknown code is ErrForbidden.
func (r *OAuthRepository) ClaimCode(ctx context.Context, tx domain.Tx, codeDigest, grantID string) (domain.OAuthRequest, error) {
	if codeDigest == "" {
		return domain.OAuthRequest{}, domain.ErrForbidden
	}
	rows, err := r.query(ctx, tx, `MATCH (r:OAuthRequest {codeDigest:$digest}) WHERE r.status = 'approved' AND r.released = true
SET r.lockedAt = timestamp()
WITH r RETURN `+oauthRequestFields, map[string]any{"digest": codeDigest})
	if err != nil {
		return domain.OAuthRequest{}, err
	}
	if len(rows) != 1 {
		return domain.OAuthRequest{}, domain.ErrForbidden
	}
	req, err := decodeOAuthRequest(rows[0])
	if err != nil {
		return domain.OAuthRequest{}, err
	}
	if req.CodeUsed {
		return req, domain.ErrConflict
	}
	if _, err := r.query(ctx, tx, `MATCH (r:OAuthRequest {id:$id}) SET r.codeUsed = true, r.grantID = $grant`, map[string]any{"id": req.ID, "grant": grantID}); err != nil {
		return domain.OAuthRequest{}, err
	}
	req.CodeUsed, req.GrantID = true, grantID
	return req, nil
}

// AddGrant stores an approved grant and marks its registration approved, so
// the registration is no longer pruned.
func (r *OAuthRepository) AddGrant(ctx context.Context, tx domain.Tx, g domain.OAuthGrant) error {
	rows, err := r.query(ctx, tx, `MATCH (reg:OAuthRegistration {id:$registration})
SET reg.approved = true
CREATE (g:OAuthGrant {id:$id, clientID:$client, registrationID:$registration, redirectURI:$redirect, refreshDigest:$refresh, createdAt:$created, expiresAt:$expires})
RETURN g.id`, map[string]any{"id": g.ID, "client": g.ClientID, "registration": g.RegistrationID, "redirect": g.RedirectURI, "refresh": g.RefreshDigest, "created": g.CreatedAt.UTC().UnixNano(), "expires": g.ExpiresAt.UTC().UnixNano()})
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrForbidden
	}
	return nil
}

const oauthGrantFields = `g.id, g.clientID, g.registrationID, g.redirectURI, g.refreshDigest, g.createdAt, g.expiresAt`

// FindGrantByRefresh returns the grant whose current refresh token has this
// digest, locking it. If the digest belongs to a refresh token already used,
// it returns that grant with ErrConflict. An unknown digest is ErrForbidden.
func (r *OAuthRepository) FindGrantByRefresh(ctx context.Context, tx domain.Tx, digest string) (domain.OAuthGrant, error) {
	if digest == "" {
		return domain.OAuthGrant{}, domain.ErrForbidden
	}
	rows, err := r.query(ctx, tx, `MATCH (g:OAuthGrant {refreshDigest:$digest}) SET g.lockedAt = timestamp() WITH g RETURN `+oauthGrantFields, map[string]any{"digest": digest})
	if err != nil {
		return domain.OAuthGrant{}, err
	}
	if len(rows) == 1 {
		return decodeOAuthGrant(rows[0])
	}
	rows, err = r.query(ctx, tx, `MATCH (u:OAuthUsedRefresh {digest:$digest}) MATCH (g:OAuthGrant {id:u.grantID}) RETURN `+oauthGrantFields, map[string]any{"digest": digest})
	if err != nil {
		return domain.OAuthGrant{}, err
	}
	if len(rows) != 1 {
		return domain.OAuthGrant{}, domain.ErrForbidden
	}
	g, err := decodeOAuthGrant(rows[0])
	if err != nil {
		return domain.OAuthGrant{}, err
	}
	return g, domain.ErrConflict
}

// FindGrantByClient returns the grant a client record belongs to, or
// ErrForbidden.
func (r *OAuthRepository) FindGrantByClient(ctx context.Context, tx domain.Tx, clientID string) (domain.OAuthGrant, error) {
	rows, err := r.query(ctx, tx, `MATCH (g:OAuthGrant {clientID:$client}) RETURN `+oauthGrantFields, map[string]any{"client": clientID})
	if err != nil {
		return domain.OAuthGrant{}, err
	}
	if len(rows) != 1 {
		return domain.OAuthGrant{}, domain.ErrForbidden
	}
	return decodeOAuthGrant(rows[0])
}

// RotateRefresh replaces a grant's current refresh digest, keeping the old one
// as used so presenting it again is detected. A grant whose current digest is
// no longer old is ErrConflict.
func (r *OAuthRepository) RotateRefresh(ctx context.Context, tx domain.Tx, grantID, old, next string) error {
	rows, err := r.query(ctx, tx, `MATCH (g:OAuthGrant {id:$id}) SET g.lockedAt = timestamp()
WITH g WHERE g.refreshDigest = $old
SET g.refreshDigest = $next
CREATE (:OAuthUsedRefresh {digest:$old, grantID:g.id})
RETURN g.id`, map[string]any{"id": grantID, "old": old, "next": next})
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrConflict
	}
	return nil
}

// DeleteGrant removes a grant and the used refresh digests it kept.
func (r *OAuthRepository) DeleteGrant(ctx context.Context, tx domain.Tx, grantID string) error {
	_, err := r.query(ctx, tx, `OPTIONAL MATCH (u:OAuthUsedRefresh {grantID:$id}) DETACH DELETE u
WITH count(*) AS used
OPTIONAL MATCH (g:OAuthGrant {id:$id}) DETACH DELETE g`, map[string]any{"id": grantID})
	return err
}

// ExpiredGrants lists up to limit grants that expired at or before now.
func (r *OAuthRepository) ExpiredGrants(ctx context.Context, tx domain.Tx, now time.Time, limit int) ([]domain.OAuthGrant, error) {
	rows, err := r.query(ctx, tx, `MATCH (g:OAuthGrant) WHERE g.expiresAt <= $now RETURN `+oauthGrantFields+` LIMIT $limit`, map[string]any{"now": now.UTC().UnixNano(), "limit": int64(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]domain.OAuthGrant, 0, len(rows))
	for _, row := range rows {
		g, err := decodeOAuthGrant(row)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, nil
}

func decodeOAuthGrant(row *neo4j.Record) (domain.OAuthGrant, error) {
	v := row.Values
	if len(v) != 7 {
		return domain.OAuthGrant{}, domain.ErrUnavailable
	}
	var g domain.OAuthGrant
	var ok [7]bool
	g.ID, ok[0] = v[0].(string)
	g.ClientID, ok[1] = v[1].(string)
	g.RegistrationID, ok[2] = v[2].(string)
	g.RedirectURI, ok[3] = v[3].(string)
	g.RefreshDigest, ok[4] = v[4].(string)
	created, ok5 := v[5].(int64)
	expires, ok6 := v[6].(int64)
	ok[5], ok[6] = ok5, ok6
	for _, good := range ok {
		if !good {
			return domain.OAuthGrant{}, domain.ErrUnavailable
		}
	}
	g.CreatedAt = time.Unix(0, created).UTC()
	g.ExpiresAt = time.Unix(0, expires).UTC()
	return g, nil
}
