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
// registrations already exist, in which case it returns ErrBudget. The
// count and the create run in one statement, inside the caller's transaction.
func (r *OAuthRepository) AddRegistration(ctx context.Context, tx domain.Tx, reg domain.OAuthRegistration, maxPending int) error {
	rows, err := r.query(ctx, tx, `OPTIONAL MATCH (p:OAuthRegistration) WHERE p.approved = false AND p.expiresAt > $created
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
