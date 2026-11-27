package service

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type identityFixture struct {
	mu      sync.Mutex
	clients map[string]ClientRecord
	owner   OwnerIdentity
	epoch   uint64
	retired map[string]bool
}

func (f *identityFixture) Within(ctx context.Context, fn func(context.Context, Tx) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fn(ctx, nil)
}
func (f *identityFixture) FindToken(_ context.Context, _ Tx, digest string) (ClientRecord, error) {
	for _, c := range f.clients {
		if c.TokenDigest == digest {
			c.Principal.AuthEpoch = f.epoch
			return c, nil
		}
	}
	return ClientRecord{}, domain.ErrUnauthorized
}
func (f *identityFixture) PutClient(_ context.Context, _ Tx, c ClientRecord) error {
	if _, ok := f.clients[c.Principal.ClientID]; ok {
		return domain.ErrConflict
	}
	f.clients[c.Principal.ClientID] = c
	return nil
}
func (f *identityFixture) RevokeClient(_ context.Context, _ Tx, id string) error {
	c, ok := f.clients[id]
	if !ok {
		return domain.ErrForbidden
	}
	now := time.Now().UTC()
	c.RevokedAt = &now
	f.clients[id] = c
	f.epoch++
	return nil
}
func (f *identityFixture) RotateClient(_ context.Context, _ Tx, id, digest string, expiresAt *time.Time) error {
	c, ok := f.clients[id]
	if !ok || c.RevokedAt != nil {
		return domain.ErrForbidden
	}
	c.TokenDigest = digest
	c.ExpiresAt = expiresAt
	f.clients[id] = c
	f.epoch++
	return nil
}
func (f *identityFixture) Owner(_ context.Context, _ Tx) (OwnerIdentity, error) { return f.owner, nil }
func (f *identityFixture) PutOwner(_ context.Context, _ Tx, o OwnerIdentity) error {
	if f.owner.ID != "" {
		return domain.ErrConflict
	}
	f.owner = o
	return nil
}
func (f *identityFixture) ReplaceOwnerKey(_ context.Context, _ Tx, c domain.OwnerKeyChange) error {
	if f.owner.ID != c.OwnerID || f.owner.PublicKey != c.RetiredKey || f.retired[c.PublicKey] {
		return domain.ErrConflict
	}
	if f.retired == nil {
		f.retired = map[string]bool{}
	}
	f.retired[c.RetiredKey] = true
	f.owner.PublicKey = c.PublicKey
	f.epoch++
	return nil
}
func newIdentityFixture() (*IdentityService, *identityFixture) {
	f := &identityFixture{clients: map[string]ClientRecord{}}
	return NewIdentityService(f, f, gateway.NostrIdentity{}), f
}
func setupFixture(t *testing.T, s *IdentityService) ClientCredential {
	t.Helper()
	c, err := s.Setup(t.Context(), OwnerIdentity{ID: "owner-public", PublicKey: ownerNostrPublicKey}, EnrollInput{ClientID: "client", Role: domain.RoleAgent, ProjectIDs: []string{"demo"}, Capabilities: []string{"status:read", "retrieval:read"}})
	require.NoError(t, err)
	return c
}
func TestAgentCannotEnrollVerifier(t *testing.T) {
	s, _ := newIdentityFixture()
	c := setupFixture(t, s)
	p, err := s.Authenticate(t.Context(), c.Bearer)
	require.NoError(t, err)
	_, err = s.Enroll(t.Context(), p, EnrollInput{Role: domain.RoleVerifier, ProjectIDs: []string{"demo"}, Capabilities: []string{"verification:write"}})
	assert.ErrorIs(t, err, domain.ErrForbidden)
}
func TestRevokedTokenFailsNextRequest(t *testing.T) {
	s, _ := newIdentityFixture()
	c := setupFixture(t, s)
	owner := domain.Principal{OwnerID: "owner-public", Role: domain.RoleOwner}
	require.NoError(t, s.Revoke(t.Context(), owner, c.ClientID))
	_, err := s.Authenticate(t.Context(), c.Bearer)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
}
func TestClientCannotRequestOtherProject(t *testing.T) {
	s, _ := newIdentityFixture()
	c := setupFixture(t, s)
	p, err := s.Authenticate(t.Context(), c.Bearer)
	require.NoError(t, err)
	err = Authorize(p, "retrieval:read", []string{"private-project"})
	assert.ErrorIs(t, err, domain.ErrForbidden)
}
func TestSetupOnceAndRoleCapabilities(t *testing.T) {
	s, f := newIdentityFixture()
	c := setupFixture(t, s)
	assert.NotContains(t, f.clients[c.ClientID].TokenDigest, c.Bearer)
	_, err := s.Setup(t.Context(), f.owner, EnrollInput{Role: domain.RoleAgent})
	assert.ErrorIs(t, err, domain.ErrConflict)
	_, err = s.Enroll(t.Context(), domain.Principal{OwnerID: f.owner.ID, Role: domain.RoleOwner}, EnrollInput{ClientID: "forged", Role: domain.RoleAgent, ProjectIDs: []string{"demo"}, Capabilities: []string{"clients:manage"}})
	assert.ErrorIs(t, err, domain.ErrInvalidInput)
}

func TestEnrollmentRejectsAuthorityAndEmptyScope(t *testing.T) {
	s, f := newIdentityFixture()
	setupFixture(t, s)
	owner := domain.Principal{OwnerID: f.owner.ID, Role: domain.RoleOwner}
	for _, tc := range []struct {
		name           string
		role           domain.Role
		projects, caps []string
	}{
		{"owner bearer", domain.RoleOwner, []string{"demo"}, []string{"clients:manage"}},
		{"agent administration", domain.RoleAgent, []string{"demo"}, []string{"clients:manage"}},
		{"collector verifier", domain.RoleCollector, []string{"demo"}, []string{"verification:write"}},
		{"collector export", domain.RoleCollector, []string{"demo"}, []string{"memory:export"}},
		{"verifier export", domain.RoleVerifier, []string{"demo"}, []string{"memory:export"}},
		{"blank project", domain.RoleAgent, []string{" "}, []string{"status:read"}},
		{"missing project", domain.RoleAgent, nil, []string{"status:read"}},
		{"missing capabilities", domain.RoleAgent, []string{"demo"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Enroll(t.Context(), owner, EnrollInput{Role: tc.role, ProjectIDs: tc.projects, Capabilities: tc.caps})
			assert.ErrorIs(t, err, domain.ErrInvalidInput)
		})
	}
	c, err := s.Enroll(t.Context(), owner, EnrollInput{Role: domain.RoleVerifier, ProjectIDs: []string{"demo"}, Capabilities: []string{"verification:write"}})
	require.NoError(t, err)
	p, err := s.Authenticate(t.Context(), c.Bearer)
	require.NoError(t, err)
	assert.Equal(t, domain.RoleVerifier, p.Role)
	assert.ErrorIs(t, Authorize(p, "verification:write", nil), domain.ErrForbidden)
	assert.ErrorIs(t, Authorize(p, "verification:write", []string{""}), domain.ErrForbidden)
	assert.ErrorIs(t, Authorize(p, "clients:manage", []string{"demo"}), domain.ErrForbidden)
	require.NoError(t, Authorize(p, "verification:write", []string{"demo"}))
	assert.NotContains(t, c.String(), c.Bearer)
	assert.NotContains(t, c.GoString(), c.Bearer)
}

func TestAgentMayBeGrantedExplicitExport(t *testing.T) {
	s, f := newIdentityFixture()
	setupFixture(t, s)
	owner := domain.Principal{OwnerID: f.owner.ID, Role: domain.RoleOwner}

	c, err := s.Enroll(t.Context(), owner, EnrollInput{Role: domain.RoleAgent, ProjectIDs: []string{"demo"}, Capabilities: []string{"retrieval:read", "memory:export"}})

	require.NoError(t, err)
	assert.Contains(t, c.Principal.Capabilities, "memory:export")
}
func TestAuthenticationAndOwnerFailuresAreClosed(t *testing.T) {
	s, f := newIdentityFixture()
	setupFixture(t, s)
	for _, token := range []string{"", "fixture-secret", strings.Repeat("x", 64)} {
		_, err := s.Authenticate(t.Context(), token)
		assert.ErrorIs(t, err, domain.ErrUnauthorized)
	}
	err := s.Revoke(t.Context(), domain.Principal{OwnerID: "forged", Role: domain.RoleOwner}, "client")
	assert.ErrorIs(t, err, domain.ErrForbidden)
	err = s.Revoke(t.Context(), domain.Principal{OwnerID: f.owner.ID, Role: domain.RoleOwner}, " ")
	assert.ErrorIs(t, err, domain.ErrInvalidInput)
}
func TestInvalidOwnerSetupDoesNotInitialize(t *testing.T) {
	s, f := newIdentityFixture()
	_, err := s.Setup(t.Context(), OwnerIdentity{ID: "owner", PublicKey: "not-a-public-key"}, EnrollInput{})
	assert.ErrorIs(t, err, domain.ErrInvalidInput)
	assert.Empty(t, f.owner.ID)
}

type failedCommit struct{ inner Transactor }

func (f failedCommit) Within(ctx context.Context, fn func(context.Context, Tx) error) error {
	if err := f.inner.Within(ctx, fn); err != nil {
		return err
	}
	return domain.ErrUnavailable
}
func TestFailedCommitReturnsNoCredential(t *testing.T) {
	_, f := newIdentityFixture()
	s := NewIdentityService(failedCommit{inner: f}, f, gateway.NostrIdentity{})
	credential, err := s.Setup(t.Context(), OwnerIdentity{ID: "owner-public", PublicKey: ownerNostrPublicKey}, EnrollInput{Role: domain.RoleAgent, ProjectIDs: []string{"demo"}, Capabilities: []string{"status:read"}})
	assert.ErrorIs(t, err, domain.ErrUnavailable)
	assert.Empty(t, credential.Bearer)
	assert.Empty(t, credential.ClientID)
}

// The secp256k1 generator's public x coordinate is a public test vector only.
const ownerNostrPublicKey = "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"

func TestSetupRejectsNonNostrOwnerIdentity(t *testing.T) {
	for _, tc := range []struct{ name, key string }{
		{"former base64 zero fixture", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="},
		{"base64 Ed25519 public key", "11qYAYdk9JYiv7wBF1jLQ3hHfX5xj5s3o1J7b53CzjM="},
		{"algorithm-tagged key", "ed25519:" + ownerNostrPublicKey},
		{"SEC compressed point", "02" + ownerNostrPublicKey},
		{"zero x coordinate", strings.Repeat("0", 64)},
		{"off curve x coordinate", strings.Repeat("0", 63) + "5"},
		{"coordinate outside field", strings.Repeat("f", 64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newIdentityFixture()
			credential, err := s.Setup(t.Context(), OwnerIdentity{ID: "owner-public", PublicKey: tc.key}, EnrollInput{Role: domain.RoleAgent, ProjectIDs: []string{"demo"}, Capabilities: []string{"status:read"}})
			assert.ErrorIs(t, err, domain.ErrInvalidInput)
			assert.Empty(t, credential.ClientID)
			assert.Empty(t, f.owner.ID)
			assert.Empty(t, f.clients)
		})
	}
}
func TestSetupStoresCanonicalNostrOwnerIdentity(t *testing.T) {
	s, f := newIdentityFixture()
	credential, err := s.Setup(t.Context(), OwnerIdentity{ID: "owner-public", PublicKey: strings.ToUpper(ownerNostrPublicKey)}, EnrollInput{Role: domain.RoleAgent, ProjectIDs: []string{"demo"}, Capabilities: []string{"status:read"}})
	require.NoError(t, err)
	assert.Equal(t, ownerNostrPublicKey, f.owner.PublicKey)
	assert.Equal(t, "owner-public", f.owner.ID)
	assert.NotEqual(t, f.owner.ID, f.owner.PublicKey)
	assert.NotEmpty(t, credential.ClientID)
}

func TestEnrollmentReturnsEpochAfterRevocation(t *testing.T) {
	s, f := newIdentityFixture()
	old := setupFixture(t, s)
	owner := domain.Principal{OwnerID: f.owner.ID, Role: domain.RoleOwner}
	require.NoError(t, s.Revoke(t.Context(), owner, old.ClientID))
	credential, err := s.Enroll(t.Context(), owner, EnrollInput{ClientID: "fresh", Role: domain.RoleAgent, ProjectIDs: []string{"demo"}, Capabilities: []string{"retrieval:read"}})
	require.NoError(t, err)
	assert.Equal(t, uint64(1), credential.Principal.AuthEpoch)
}

func (f *identityFixture) ListClients(_ context.Context, _ Tx, owner string, limit int, after string) ([]domain.ClientMetadata, error) {
	var out []domain.ClientMetadata
	for _, c := range f.clients {
		if c.Principal.OwnerID == owner && c.Principal.ClientID > after {
			out = append(out, domain.ClientMetadata{Principal: c.Principal, RevokedAt: c.RevokedAt})
		}
	}
	slices.SortFunc(out, func(a, b domain.ClientMetadata) int {
		return strings.Compare(a.Principal.ClientID, b.Principal.ClientID)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func TestHostAdminEnrollRevokeListUsesPersistedOwner(t *testing.T) {
	s, f := newIdentityFixture()
	_, err := s.EnrollHost(t.Context(), EnrollInput{Role: domain.RoleAgent, ProjectIDs: []string{"demo"}, Capabilities: []string{"status:read"}})
	require.ErrorIs(t, err, domain.ErrForbidden)
	setupFixture(t, s)
	c, err := s.EnrollHost(t.Context(), EnrollInput{ClientID: "recovered", Role: domain.RoleAgent, ProjectIDs: []string{"demo"}, Capabilities: []string{"status:read"}})
	require.NoError(t, err)
	assert.Equal(t, f.owner.ID, c.Principal.OwnerID)
	require.NoError(t, s.RevokeHost(t.Context(), c.ClientID))
	page, err := s.ListHost(t.Context(), 1, "")
	require.NoError(t, err)
	require.Len(t, page.Clients, 1)
	assert.Equal(t, "client", page.Clients[0].Principal.ClientID)
	assert.Equal(t, "client", page.Next)
	page, err = s.ListHost(t.Context(), 1, page.Next)
	require.NoError(t, err)
	require.Len(t, page.Clients, 1)
	assert.NotNil(t, page.Clients[0].RevokedAt)
	assert.Empty(t, page.Next)
}
func TestHostAdminListNeverReturnsCredentials(t *testing.T) {
	s, f := newIdentityFixture()
	c := setupFixture(t, s)
	page, err := s.ListHost(t.Context(), 50, "")
	require.NoError(t, err)
	data, err := json.Marshal(page)
	require.NoError(t, err)
	assert.NotContains(t, string(data), c.Bearer)
	assert.NotContains(t, string(data), f.clients[c.ClientID].TokenDigest)
	assert.NotContains(t, string(data), "TokenDigest")
	for _, limit := range []int{0, 101} {
		_, err = s.ListHost(t.Context(), limit, "")
		assert.ErrorIs(t, err, domain.ErrInvalidInput)
	}
}
func TestHostOwnerCannotAuthenticateAsBearer(t *testing.T) {
	s, f := newIdentityFixture()
	c := setupFixture(t, s)
	record := f.clients[c.ClientID]
	record.Principal.Role = domain.RoleOwner
	f.clients[c.ClientID] = record
	_, err := s.Authenticate(t.Context(), c.Bearer)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
	_, err = s.EnrollHost(t.Context(), EnrollInput{Role: domain.RoleOwner, ProjectIDs: []string{"demo"}, Capabilities: []string{"status:read"}})
	assert.ErrorIs(t, err, domain.ErrInvalidInput)
	_, err = s.EnrollHost(t.Context(), EnrollInput{ClientID: "host-owner:fake", Role: domain.RoleAgent, ProjectIDs: []string{"demo"}, Capabilities: []string{"status:read"}})
	assert.ErrorIs(t, err, domain.ErrInvalidInput)
}

func TestExpiredCredentialStopsAuthenticating(t *testing.T) {
	s, f := newIdentityFixture()
	clock := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	_, err := s.Setup(t.Context(), OwnerIdentity{ID: "owner-public", PublicKey: ownerNostrPublicKey}, EnrollInput{ClientID: "client", Role: domain.RoleAgent, ProjectIDs: []string{"demo"}, Capabilities: []string{"status:read"}, TTL: 2 * time.Hour})
	require.NoError(t, err)
	short, err := s.EnrollHost(t.Context(), EnrollInput{ClientID: "short", Role: domain.RoleAgent, ProjectIDs: []string{"demo"}, Capabilities: []string{"status:read"}, TTL: time.Hour})
	require.NoError(t, err)
	require.NotNil(t, f.clients["short"].ExpiresAt)
	assert.Equal(t, clock.Add(time.Hour), *f.clients["short"].ExpiresAt)
	require.NotNil(t, short.ExpiresAt, "the credential reports its own expiry")
	assert.Equal(t, clock.Add(time.Hour), *short.ExpiresAt)

	clock = clock.Add(time.Hour - time.Nanosecond)
	_, err = s.Authenticate(t.Context(), short.Bearer)
	require.NoError(t, err)
	clock = clock.Add(time.Nanosecond)
	_, err = s.Authenticate(t.Context(), short.Bearer)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
}

func TestCredentialTTLIsBounded(t *testing.T) {
	s, _ := newIdentityFixture()
	setupFixture(t, s)
	for _, ttl := range []time.Duration{-time.Hour, time.Minute, MaxCredentialTTL + time.Hour} {
		_, err := s.EnrollHost(t.Context(), EnrollInput{Role: domain.RoleAgent, ProjectIDs: []string{"demo"}, Capabilities: []string{"status:read"}, TTL: ttl})
		assert.ErrorIs(t, err, domain.ErrInvalidInput, ttl)
		_, err = s.RotateHost(t.Context(), "client", ttl)
		assert.ErrorIs(t, err, domain.ErrInvalidInput, ttl)
	}
	_, err := s.EnrollHost(t.Context(), EnrollInput{Role: domain.RoleAgent, ProjectIDs: []string{"demo"}, Capabilities: []string{"status:read"}, TTL: MaxCredentialTTL})
	assert.NoError(t, err)
}

func TestRotateReplacesBearerAndKeepsGrants(t *testing.T) {
	s, f := newIdentityFixture()
	old := setupFixture(t, s)
	before, err := s.Authenticate(t.Context(), old.Bearer)
	require.NoError(t, err)

	rotated, err := s.RotateHost(t.Context(), "client", 90*24*time.Hour)
	require.NoError(t, err)
	assert.NotEqual(t, old.Bearer, rotated.Bearer)
	assert.Len(t, rotated.Bearer, 64)
	_, err = s.Authenticate(t.Context(), old.Bearer)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
	after, err := s.Authenticate(t.Context(), rotated.Bearer)
	require.NoError(t, err)
	assert.Equal(t, before.ClientID, after.ClientID)
	assert.Equal(t, before.ProjectIDs, after.ProjectIDs)
	assert.Equal(t, before.Capabilities, after.Capabilities)
	assert.Greater(t, after.AuthEpoch, before.AuthEpoch)
	require.NotNil(t, f.clients["client"].ExpiresAt)
	require.NotNil(t, rotated.ExpiresAt)
	assert.Equal(t, *f.clients["client"].ExpiresAt, *rotated.ExpiresAt)

	require.NoError(t, s.RevokeHost(t.Context(), "client"))
	_, err = s.RotateHost(t.Context(), "client", 0)
	assert.ErrorIs(t, err, domain.ErrForbidden)
	_, err = s.RotateHost(t.Context(), "missing", 0)
	assert.ErrorIs(t, err, domain.ErrForbidden)
	_, err = s.RotateHost(t.Context(), "host-owner:x", 0)
	assert.ErrorIs(t, err, domain.ErrInvalidInput)
}

func TestRotateRequiresPersistedOwner(t *testing.T) {
	s, _ := newIdentityFixture()
	_, err := s.RotateHost(t.Context(), "client", 0)
	assert.ErrorIs(t, err, domain.ErrForbidden)
}

// Two more valid BIP-340 x-only keys: the x coordinates of 2G and 3G.
const (
	replacementOwnerKey = "c6047f9441ed7d6d3045406e95c07cd85c778e4b8cef3ca7abac09b95c709ee5"
	laterOwnerKey       = "f9308a019258c31049344f85f89d5229b531c845836f99b08601f113bce036f9"
)

func TestReplaceOwnerKeyRetiresTheOldKeyForGood(t *testing.T) {
	s, f := newIdentityFixture()
	client := setupFixture(t, s)

	change, err := s.ReplaceOwnerKeyHost(t.Context(), strings.ToUpper(replacementOwnerKey))
	require.NoError(t, err)
	assert.Equal(t, domain.OwnerKeyChange{OwnerID: "owner-public", RetiredKey: ownerNostrPublicKey, PublicKey: replacementOwnerKey, ChangedAt: change.ChangedAt}, change)
	assert.False(t, change.ChangedAt.IsZero())
	assert.Equal(t, replacementOwnerKey, f.owner.PublicKey, "the key is stored in canonical lowercase form")

	// Clients keep working: only the owner's signing key changed.
	_, err = s.Authenticate(t.Context(), client.Bearer)
	require.NoError(t, err)

	_, err = s.ReplaceOwnerKeyHost(t.Context(), replacementOwnerKey)
	assert.ErrorIs(t, err, domain.ErrConflict, "replacing a key with itself is refused")
	_, err = s.ReplaceOwnerKeyHost(t.Context(), ownerNostrPublicKey)
	assert.ErrorIs(t, err, domain.ErrConflict, "a retired key can never come back")
	_, err = s.ReplaceOwnerKeyHost(t.Context(), laterOwnerKey)
	require.NoError(t, err)
	_, err = s.ReplaceOwnerKeyHost(t.Context(), replacementOwnerKey)
	assert.ErrorIs(t, err, domain.ErrConflict)
}

func TestReplaceOwnerKeyNeedsAnOwnerAndAValidKey(t *testing.T) {
	s, _ := newIdentityFixture()
	_, err := s.ReplaceOwnerKeyHost(t.Context(), replacementOwnerKey)
	assert.ErrorIs(t, err, domain.ErrForbidden, "no owner is enrolled yet")
	setupFixture(t, s)
	for _, key := range []string{"", "not-hex", strings.Repeat("0", 64), replacementOwnerKey[:62]} {
		_, err = s.ReplaceOwnerKeyHost(t.Context(), key)
		assert.ErrorIs(t, err, domain.ErrInvalidInput, key)
	}
}
