// Package repository implements graph persistence without owning transactions.
package repository

import (
	"context"
	"strings"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
)

type IdentityRepository struct{ store *graph.Store }

func NewIdentityRepository(store *graph.Store) *IdentityRepository {
	return &IdentityRepository{store: store}
}
func (r *IdentityRepository) query(ctx context.Context, tx domain.Tx, statement string, params map[string]any) ([]*neo4j.Record, error) {
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
func (r *IdentityRepository) FindToken(ctx context.Context, tx domain.Tx, digest string) (domain.ClientRecord, error) {
	rows, err := r.query(ctx, tx, `MATCH (o:OwnerState {id:'owner'}), (c:Client {tokenDigest:$digest}) RETURN c.id,c.ownerID,c.role,c.projectIDs,c.capabilities,o.authEpoch,c.revokedAt,c.expiresAt,coalesce(c.resource,'')`, map[string]any{"digest": digest})
	if err != nil {
		return domain.ClientRecord{}, err
	}
	if len(rows) != 1 {
		return domain.ClientRecord{}, domain.ErrUnauthorized
	}
	v := rows[0].Values
	id, ok1 := v[0].(string)
	owner, ok2 := v[1].(string)
	role, ok3 := v[2].(string)
	projects, ok4 := stringList(v[3])
	caps, ok5 := stringList(v[4])
	epoch, ok6 := v[5].(int64)
	resource, ok7 := v[8].(string)
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || !ok6 || !ok7 || epoch < 0 {
		return domain.ClientRecord{}, domain.ErrUnavailable
	}
	record := domain.ClientRecord{Principal: domain.Principal{ClientID: id, OwnerID: owner, Role: domain.Role(role), ProjectIDs: projects, Capabilities: caps, AuthEpoch: uint64(epoch), Resource: resource}, TokenDigest: digest}
	var ok bool
	if record.RevokedAt, ok = optionalInstant(v[6]); !ok {
		return domain.ClientRecord{}, domain.ErrUnavailable
	}
	if record.ExpiresAt, ok = optionalInstant(v[7]); !ok {
		return domain.ClientRecord{}, domain.ErrUnavailable
	}
	return record, nil
}

// optionalInstant decodes a nullable UTC nanosecond timestamp property.
func optionalInstant(value any) (*time.Time, bool) {
	if value == nil {
		return nil, true
	}
	stamp, ok := value.(int64)
	if !ok {
		return nil, false
	}
	at := time.Unix(0, stamp).UTC()
	return &at, true
}
func optionalNanos(at *time.Time) any {
	if at == nil {
		return nil
	}
	return at.UTC().UnixNano()
}
func stringList(value any) ([]string, bool) {
	values, ok := value.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		s, ok := v.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}
func (r *IdentityRepository) PutClient(ctx context.Context, tx domain.Tx, c domain.ClientRecord) error {
	if strings.HasPrefix(c.Principal.ClientID, "host-owner:") {
		return domain.ErrInvalidInput
	}
	rows, err := r.query(ctx, tx, "MATCH (c:Client) WHERE c.id=$id OR c.tokenDigest=$digest RETURN c.id LIMIT 1", map[string]any{"id": c.Principal.ClientID, "digest": c.TokenDigest})
	if err != nil {
		return err
	}
	if len(rows) > 0 {
		return domain.ErrConflict
	}
	_, err = r.query(ctx, tx, `CREATE (c:Client {id:$id,ownerID:$owner,role:$role,projectIDs:$projects,capabilities:$caps,tokenDigest:$digest}) SET c.expiresAt=$expires, c.resource=$resource`, map[string]any{"resource": c.Principal.Resource, "id": c.Principal.ClientID, "owner": c.Principal.OwnerID, "role": string(c.Principal.Role), "projects": c.Principal.ProjectIDs, "caps": c.Principal.Capabilities, "digest": c.TokenDigest, "expires": optionalNanos(c.ExpiresAt)})
	return err
}
func (r *IdentityRepository) RevokeClient(ctx context.Context, tx domain.Tx, id string) error {
	rows, err := r.query(ctx, tx, `MATCH (o:OwnerState {id:'owner'}),(c:Client {id:$id}) SET c.revokedAt=coalesce(c.revokedAt,$now),o.authEpoch=o.authEpoch+1 RETURN c.id`, map[string]any{"id": id, "now": time.Now().UTC().UnixNano()})
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrForbidden
	}
	return nil
}
func (r *IdentityRepository) RotateClient(ctx context.Context, tx domain.Tx, id, digest string, expiresAt *time.Time) error {
	if strings.HasPrefix(id, "host-owner:") {
		return domain.ErrInvalidInput
	}
	taken, err := r.query(ctx, tx, "MATCH (c:Client {tokenDigest:$digest}) RETURN c.id LIMIT 1", map[string]any{"digest": digest})
	if err != nil {
		return err
	}
	if len(taken) > 0 {
		return domain.ErrConflict
	}
	rows, err := r.query(ctx, tx, `MATCH (o:OwnerState {id:'owner'}),(c:Client {id:$id}) WHERE c.revokedAt IS NULL SET c.tokenDigest=$digest,c.expiresAt=$expires,c.rotatedAt=$now,o.authEpoch=o.authEpoch+1 RETURN c.id`, map[string]any{"id": id, "digest": digest, "expires": optionalNanos(expiresAt), "now": time.Now().UTC().UnixNano()})
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrForbidden
	}
	return nil
}
func (r *IdentityRepository) Owner(ctx context.Context, tx domain.Tx) (domain.OwnerIdentity, error) {
	rows, err := r.query(ctx, tx, `MATCH (o:OwnerState {id:'owner'}) RETURN o.ownerID,o.ownerPublicKey,EXISTS {MATCH (c:Client) WHERE c.id STARTS WITH 'host-owner:'}`, nil)
	if err != nil {
		return domain.OwnerIdentity{}, err
	}
	if len(rows) != 1 {
		return domain.OwnerIdentity{}, domain.ErrUnavailable
	}
	if rows[0].Values[2] == true {
		return domain.OwnerIdentity{}, domain.ErrForbidden
	}
	if rows[0].Values[0] == nil {
		return domain.OwnerIdentity{}, nil
	}
	id, ok1 := rows[0].Values[0].(string)
	key, ok2 := rows[0].Values[1].(string)
	if !ok1 || !ok2 {
		return domain.OwnerIdentity{}, domain.ErrUnavailable
	}
	return domain.OwnerIdentity{ID: id, PublicKey: key}, nil
}
func (r *IdentityRepository) PutOwner(ctx context.Context, tx domain.Tx, o domain.OwnerIdentity) error {
	rows, err := r.query(ctx, tx, `MATCH (o:OwnerState {id:'owner'}) WHERE o.ownerID IS NULL SET o.ownerID=$id,o.ownerPublicKey=$key RETURN o.id`, map[string]any{"id": o.ID, "key": o.PublicKey})
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrConflict
	}
	return nil
}

// ReplaceOwnerKey swaps the owner's public key if the current key is still
// change.RetiredKey and the new key was never retired, and records the old key
// as retired in the same transaction.
func (r *IdentityRepository) ReplaceOwnerKey(ctx context.Context, tx domain.Tx, change domain.OwnerKeyChange) error {
	rows, err := r.query(ctx, tx, `MATCH (o:OwnerState {id:'owner'})
WHERE o.ownerID=$owner AND o.ownerPublicKey=$old AND NOT EXISTS {MATCH (:OwnerKeyRetired {key:$new})}
SET o.ownerPublicKey=$new, o.ownerKeyChangedAt=$at, o.authEpoch=o.authEpoch+1
CREATE (:OwnerKeyRetired {key:$old, ownerID:$owner, retiredAt:$at})
RETURN o.id`, map[string]any{"owner": change.OwnerID, "old": change.RetiredKey, "new": change.PublicKey, "at": change.ChangedAt.UTC().UnixNano()})
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrConflict
	}
	return nil
}

func (r *IdentityRepository) ListClients(ctx context.Context, tx domain.Tx, owner string, limit int, after string) ([]domain.ClientMetadata, error) {
	if limit < 1 || limit > 101 {
		return nil, domain.ErrInvalidInput
	}
	rows, err := r.query(ctx, tx, `MATCH (c:Client {ownerID:$owner}),(o:OwnerState {id:'owner'}) WHERE c.id>$after RETURN c.id,c.role,c.projectIDs,c.capabilities,c.revokedAt,o.authEpoch,c.expiresAt ORDER BY c.id LIMIT $limit`, map[string]any{"owner": owner, "after": after, "limit": limit})
	if err != nil {
		return nil, err
	}
	out := make([]domain.ClientMetadata, 0, len(rows))
	for _, row := range rows {
		v := row.Values
		id, a := v[0].(string)
		role, b := v[1].(string)
		projects, c := stringList(v[2])
		caps, d := stringList(v[3])
		epoch, e := v[5].(int64)
		if !a || !b || !c || !d || !e || epoch < 0 {
			return nil, domain.ErrUnavailable
		}
		m := domain.ClientMetadata{Principal: domain.Principal{ClientID: id, OwnerID: owner, Role: domain.Role(role), ProjectIDs: projects, Capabilities: caps, AuthEpoch: uint64(epoch)}}
		var ok bool
		if m.RevokedAt, ok = optionalInstant(v[4]); !ok {
			return nil, domain.ErrUnavailable
		}
		if m.ExpiresAt, ok = optionalInstant(v[6]); !ok {
			return nil, domain.ErrUnavailable
		}
		out = append(out, m)
	}
	return out, nil
}
