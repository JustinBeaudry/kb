package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

type ClientRecord = domain.ClientRecord
type OwnerIdentity = domain.OwnerIdentity
type EnrollInput struct {
	ClientID                 string
	Role                     domain.Role
	ProjectIDs, Capabilities []string
	// TTL bounds the bearer's lifetime; zero issues a bearer without expiry.
	TTL time.Duration
}

// Credential lifetimes are bounded so a forgotten bearer cannot live forever
// once an expiry is chosen, and cannot be set so short it expires mid-setup.
const (
	MinCredentialTTL = time.Hour
	MaxCredentialTTL = 366 * 24 * time.Hour
)

func validTTL(ttl time.Duration) bool {
	return ttl == 0 || (ttl >= MinCredentialTTL && ttl <= MaxCredentialTTL)
}

type ClientCredential struct {
	ClientID  string
	Bearer    string
	Principal domain.Principal
	// ExpiresAt is when Bearer stops authenticating; nil means no expiry.
	ExpiresAt *time.Time
}

func (ClientCredential) String() string   { return "ClientCredential([redacted])" }
func (ClientCredential) GoString() string { return "ClientCredential([redacted])" }

type ClientPage struct {
	Clients []domain.ClientMetadata
	Next    string `json:",omitempty"`
}

const ownerActorPrefix = "host-owner:"

type IdentityRepository interface {
	ListClients(context.Context, Tx, string, int, string) ([]domain.ClientMetadata, error)
	FindToken(context.Context, Tx, string) (ClientRecord, error)
	PutClient(context.Context, Tx, ClientRecord) error
	RevokeClient(context.Context, Tx, string) error
	// RotateClient replaces an active client's bearer digest and expiry in
	// place, keeping its identity and grants. The old bearer stops working.
	RotateClient(ctx context.Context, tx Tx, id, digest string, expiresAt *time.Time) error
	Owner(context.Context, Tx) (OwnerIdentity, error)
	PutOwner(context.Context, Tx, OwnerIdentity) error
	// ReplaceOwnerKey swaps the owner key only while the current key is
	// change.RetiredKey and the new key was never retired; otherwise it
	// returns ErrConflict.
	ReplaceOwnerKey(context.Context, Tx, domain.OwnerKeyChange) error
}

// OwnerPublicKeyValidator isolates provider encoding/curve validation from
// service orchestration. Implementations return canonical public identity only.
type OwnerPublicKeyValidator interface{ NormalizePublicKey(string) (string, error) }
type IdentityService struct {
	tx         Transactor
	repo       IdentityRepository
	publicKeys OwnerPublicKeyValidator
	now        func() time.Time
}

func NewIdentityService(tx Transactor, repo IdentityRepository, publicKeys OwnerPublicKeyValidator) *IdentityService {
	return &IdentityService{tx: tx, repo: repo, publicKeys: publicKeys, now: time.Now}
}
func TokenDigest(bearer string) string {
	digest := sha256.Sum256([]byte(bearer))
	return hex.EncodeToString(digest[:])
}
func (s *IdentityService) Authenticate(ctx context.Context, bearer string) (domain.Principal, error) {
	if len(bearer) != 64 || strings.TrimSpace(bearer) != bearer {
		domain.ObserveAuthFailure(ctx, "", domain.AccessAuthUnknown)
		return domain.Principal{}, domain.ErrUnauthorized
	}
	var p domain.Principal
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		record, err := s.repo.FindToken(ctx, tx, TokenDigest(bearer))
		if errors.Is(err, domain.ErrUnauthorized) {
			domain.ObserveAuthFailure(ctx, "", domain.AccessAuthUnknown)
		}
		if err != nil {
			return err
		}
		if record.Principal.Role == domain.RoleOwner {
			domain.ObserveAuthFailure(ctx, "", domain.AccessAuthUnknown)
			return domain.ErrUnauthorized
		}
		if record.RevokedAt != nil {
			domain.ObserveAuthFailure(ctx, record.Principal.ClientID, domain.AccessAuthRevoked)
			return domain.ErrUnauthorized
		}
		if record.ExpiresAt != nil && !s.now().Before(*record.ExpiresAt) {
			domain.ObserveAuthFailure(ctx, record.Principal.ClientID, domain.AccessAuthExpired)
			return domain.ErrUnauthorized
		}
		p = record.Principal
		return nil
	})
	if err != nil {
		return domain.Principal{}, err
	}
	return p, nil
}

// Setup runs only on the controlled service host; it cannot be reached over HTTP.
// Public identity enrollment and first scoped client creation commit atomically.
func (s *IdentityService) Setup(ctx context.Context, owner OwnerIdentity, input EnrollInput) (ClientCredential, error) {
	var credential ClientCredential
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		existing, err := s.repo.Owner(ctx, tx)
		if err != nil {
			return err
		}
		if existing.ID != "" {
			return domain.ErrConflict
		}
		canonicalKey, err := s.publicKeys.NormalizePublicKey(owner.PublicKey)
		if err != nil || domain.ValidateIDs(owner.ID) != nil {
			return domain.ErrInvalidInput
		}
		owner.PublicKey = canonicalKey
		if err := validateEnrollment(input); err != nil {
			return err
		}
		if err := s.repo.PutOwner(ctx, tx, owner); err != nil {
			return err
		}
		credential, err = s.enroll(ctx, tx, owner.ID, input)
		return err
	})
	if err != nil {
		return ClientCredential{}, err
	}
	return credential, nil
}
func (s *IdentityService) Enroll(ctx context.Context, owner domain.Principal, input EnrollInput) (ClientCredential, error) {
	var credential ClientCredential
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		if err := s.requireOwner(ctx, tx, owner); err != nil {
			return err
		}
		if err := validateEnrollment(input); err != nil {
			return err
		}
		var err error
		credential, err = s.enroll(ctx, tx, owner.OwnerID, input)
		return err
	})
	if err != nil {
		return ClientCredential{}, err
	}
	return credential, nil
}
func (s *IdentityService) requireOwner(ctx context.Context, tx Tx, p domain.Principal) error {
	if p.Role != domain.RoleOwner || p.OwnerID == "" {
		return domain.ErrForbidden
	}
	owner, err := s.repo.Owner(ctx, tx)
	if err != nil {
		return err
	}
	if owner.ID != p.OwnerID {
		return domain.ErrForbidden
	}
	return nil
}
func (s *IdentityService) enroll(ctx context.Context, tx Tx, ownerID string, input EnrollInput) (ClientCredential, error) {
	id := input.ClientID
	if id == "" {
		id = rand.Text()
	}
	bearer, err := newBearer()
	if err != nil {
		return ClientCredential{}, err
	}
	p := domain.Principal{ClientID: id, OwnerID: ownerID, Role: input.Role, ProjectIDs: slices.Clone(input.ProjectIDs), Capabilities: slices.Clone(input.Capabilities)}
	if err := s.repo.PutClient(ctx, tx, ClientRecord{Principal: p, TokenDigest: TokenDigest(bearer), ExpiresAt: s.expiry(input.TTL)}); err != nil {
		return ClientCredential{}, err
	}
	record, err := s.repo.FindToken(ctx, tx, TokenDigest(bearer))
	if err != nil {
		return ClientCredential{}, err
	}
	return ClientCredential{ClientID: id, Bearer: bearer, Principal: record.Principal, ExpiresAt: record.ExpiresAt}, nil
}
func newBearer() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", domain.ErrUnavailable
	}
	return hex.EncodeToString(raw[:]), nil
}
func (s *IdentityService) expiry(ttl time.Duration) *time.Time {
	if ttl == 0 {
		return nil
	}
	at := s.now().UTC().Add(ttl)
	return &at
}
func (s *IdentityService) Revoke(ctx context.Context, owner domain.Principal, id string) error {
	return s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		if err := s.requireOwner(ctx, tx, owner); err != nil {
			return err
		}
		if domain.ValidateIDs(id) != nil {
			return domain.ErrInvalidInput
		}
		return s.repo.RevokeClient(ctx, tx, id)
	})
}
func validateEnrollment(input EnrollInput) error {
	// AccessOutsideGrant stands for an unknown client in the access audit
	// trail, so no client may take it as its ID.
	if strings.HasPrefix(input.ClientID, ownerActorPrefix) || input.ClientID == domain.AccessOutsideGrant || (input.ClientID != "" && domain.ValidateIDs(input.ClientID) != nil) {
		return domain.ErrInvalidInput
	}
	if !validTTL(input.TTL) {
		return domain.ErrInvalidInput
	}
	if len(input.ProjectIDs) == 0 || len(input.ProjectIDs) > 50 || len(input.Capabilities) == 0 || len(input.Capabilities) > 20 {
		return domain.ErrInvalidInput
	}
	var allowed []string
	switch input.Role {
	case domain.RoleAgent:
		allowed = []string{"policy:read", "status:read", "retrieval:read", "evidence:read", "candidate:write", "checkpoint:write", "memory:export"}
	case domain.RoleCollector:
		allowed = []string{"policy:read", "status:read", "events:append", "artifacts:write"}
	case domain.RoleVerifier:
		allowed = []string{"policy:read", "status:read", "retrieval:read", "evidence:read", "verification:write"}
	default:
		return domain.ErrInvalidInput
	}
	for _, id := range input.ProjectIDs {
		if domain.ValidateIDs(id) != nil {
			return domain.ErrInvalidInput
		}
	}
	for _, capability := range input.Capabilities {
		if !slices.Contains(allowed, capability) {
			return domain.ErrInvalidInput
		}
	}
	return nil
}

// Authorize requires every requested project to be granted, never a union or a
// broadened default. Empty scope is allowed only for metadata/status operations.
func Authorize(p domain.Principal, capability string, projects []string) error {
	if p.ClientID == "" || !slices.Contains(p.Capabilities, capability) {
		return domain.ErrForbidden
	}
	for _, id := range projects {
		if id == "" || !slices.Contains(p.ProjectIDs, id) {
			return domain.ErrForbidden
		}
	}
	if len(projects) == 0 && capability != "status:read" && capability != "clients:manage" {
		return domain.ErrForbidden
	}
	return nil
}

// hostOwner resolves authority from protected service-host execution, never a
// caller-provided identity or an enrolled client credential.
func hostOwner(ctx context.Context, tx Tx, repo interface {
	Owner(context.Context, Tx) (OwnerIdentity, error)
}) (domain.Principal, error) {
	owner, err := repo.Owner(ctx, tx)
	if err != nil {
		return domain.Principal{}, err
	}
	if owner.ID == "" || owner.PublicKey == "" {
		return domain.Principal{}, domain.ErrForbidden
	}
	return domain.Principal{Role: domain.RoleOwner, OwnerID: owner.ID}, nil
}
func (s *IdentityService) EnrollHost(ctx context.Context, input EnrollInput) (ClientCredential, error) {
	var c ClientCredential
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		owner, err := hostOwner(ctx, tx, s.repo)
		if err != nil {
			return err
		}
		if err = validateEnrollment(input); err != nil {
			return err
		}
		c, err = s.enroll(ctx, tx, owner.OwnerID, input)
		return err
	})
	if err != nil {
		return ClientCredential{}, err
	}
	return c, nil
}
func (s *IdentityService) RevokeHost(ctx context.Context, id string) error {
	if domain.ValidateIDs(id) != nil {
		return domain.ErrInvalidInput
	}
	return s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := hostOwner(ctx, tx, s.repo); err != nil {
			return err
		}
		return s.repo.RevokeClient(ctx, tx, id)
	})
}

// RotateHost issues a fresh bearer for an active client on the controlled
// service host. Grants are unchanged; the previous bearer stops working in the
// same transaction, and the auth epoch advances so state bound to it is stale.
func (s *IdentityService) RotateHost(ctx context.Context, id string, ttl time.Duration) (ClientCredential, error) {
	if domain.ValidateIDs(id) != nil || strings.HasPrefix(id, ownerActorPrefix) || !validTTL(ttl) {
		return ClientCredential{}, domain.ErrInvalidInput
	}
	var c ClientCredential
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := hostOwner(ctx, tx, s.repo); err != nil {
			return err
		}
		bearer, err := newBearer()
		if err != nil {
			return err
		}
		if err := s.repo.RotateClient(ctx, tx, id, TokenDigest(bearer), s.expiry(ttl)); err != nil {
			return err
		}
		record, err := s.repo.FindToken(ctx, tx, TokenDigest(bearer))
		if err != nil {
			return err
		}
		c = ClientCredential{ClientID: id, Bearer: bearer, Principal: record.Principal, ExpiresAt: record.ExpiresAt}
		return nil
	})
	if err != nil {
		return ClientCredential{}, err
	}
	return c, nil
}
// ReplaceOwnerKeyHost replaces the owner's public signing key on the service
// host, for a lost or stolen key. Signatures by the old key stop verifying at
// once, so pending decision challenges signed with it fail. The old key is
// kept as retired and can never be the owner key again. Clients and their
// bearers are unchanged.
func (s *IdentityService) ReplaceOwnerKeyHost(ctx context.Context, publicKey string) (domain.OwnerKeyChange, error) {
	canonical, err := s.publicKeys.NormalizePublicKey(publicKey)
	if err != nil {
		return domain.OwnerKeyChange{}, domain.ErrInvalidInput
	}
	var change domain.OwnerKeyChange
	err = s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		owner, err := s.repo.Owner(ctx, tx)
		if err != nil {
			return err
		}
		if owner.ID == "" || owner.PublicKey == "" {
			return domain.ErrForbidden
		}
		if owner.PublicKey == canonical {
			return domain.ErrConflict
		}
		change = domain.OwnerKeyChange{OwnerID: owner.ID, RetiredKey: owner.PublicKey, PublicKey: canonical, ChangedAt: s.now().UTC()}
		return s.repo.ReplaceOwnerKey(ctx, tx, change)
	})
	if err != nil {
		return domain.OwnerKeyChange{}, err
	}
	return change, nil
}
func (s *IdentityService) ListHost(ctx context.Context, limit int, after string) (ClientPage, error) {
	if limit < 1 || limit > 100 || (after != "" && domain.ValidateIDs(after) != nil) {
		return ClientPage{}, domain.ErrInvalidInput
	}
	var page ClientPage
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		owner, err := hostOwner(ctx, tx, s.repo)
		if err != nil {
			return err
		}
		page.Clients, err = s.repo.ListClients(ctx, tx, owner.OwnerID, limit+1, after)
		if err != nil {
			return err
		}
		if len(page.Clients) > limit {
			page.Clients = page.Clients[:limit]
			page.Next = page.Clients[limit-1].Principal.ClientID
		}
		return nil
	})
	if err != nil {
		return ClientPage{}, err
	}
	return page, nil
}
