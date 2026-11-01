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
func (f *identityFixture) Owner(_ context.Context, _ Tx) (OwnerIdentity, error) { return f.owner, nil }
func (f *identityFixture) PutOwner(_ context.Context, _ Tx, o OwnerIdentity) error {
	if f.owner.ID != "" {
		return domain.ErrConflict
	}
	f.owner = o
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
