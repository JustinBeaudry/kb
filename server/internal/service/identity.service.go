package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

type ClientRecord = domain.ClientRecord
type OwnerIdentity = domain.OwnerIdentity
type EnrollInput struct {
	ClientID                 string
	Role                     domain.Role
	ProjectIDs, Capabilities []string
}
type ClientCredential struct {
	ClientID  string
	Bearer    string
	Principal domain.Principal
}

func (ClientCredential) String() string   { return "ClientCredential([redacted])" }
func (ClientCredential) GoString() string { return "ClientCredential([redacted])" }

type IdentityRepository interface {
	FindToken(context.Context, Tx, string) (ClientRecord, error)
	PutClient(context.Context, Tx, ClientRecord) error
	RevokeClient(context.Context, Tx, string) error
	Owner(context.Context, Tx) (OwnerIdentity, error)
	PutOwner(context.Context, Tx, OwnerIdentity) error
}
type IdentityService struct {
	tx   Transactor
	repo IdentityRepository
}

func NewIdentityService(tx Transactor, repo IdentityRepository) *IdentityService {
	return &IdentityService{tx: tx, repo: repo}
}
func TokenDigest(bearer string) string {
	digest := sha256.Sum256([]byte(bearer))
	return hex.EncodeToString(digest[:])
}
func (s *IdentityService) Authenticate(ctx context.Context, bearer string) (domain.Principal, error) {
	if len(bearer) != 64 || strings.TrimSpace(bearer) != bearer {
		return domain.Principal{}, domain.ErrUnauthorized
	}
	var p domain.Principal
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		record, err := s.repo.FindToken(ctx, tx, TokenDigest(bearer))
		if err != nil {
			return err
		}
		if record.RevokedAt != nil || record.Principal.Role == domain.RoleOwner {
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
		key, err := base64.StdEncoding.DecodeString(owner.PublicKey)
		if err != nil || len(key) != 32 || domain.ValidateIDs(owner.ID) != nil {
			return domain.ErrInvalidInput
		}
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
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return ClientCredential{}, domain.ErrUnavailable
	}
	bearer := hex.EncodeToString(raw[:])
	p := domain.Principal{ClientID: id, OwnerID: ownerID, Role: input.Role, ProjectIDs: slices.Clone(input.ProjectIDs), Capabilities: slices.Clone(input.Capabilities)}
	if err := s.repo.PutClient(ctx, tx, ClientRecord{Principal: p, TokenDigest: TokenDigest(bearer)}); err != nil {
		return ClientCredential{}, err
	}
	return ClientCredential{ClientID: id, Bearer: bearer, Principal: p}, nil
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
	if input.ClientID != "" && domain.ValidateIDs(input.ClientID) != nil {
		return domain.ErrInvalidInput
	}
	if len(input.ProjectIDs) == 0 || len(input.ProjectIDs) > 50 || len(input.Capabilities) == 0 || len(input.Capabilities) > 20 {
		return domain.ErrInvalidInput
	}
	var allowed []string
	switch input.Role {
	case domain.RoleAgent:
		allowed = []string{"status:read", "retrieval:read", "evidence:read", "candidate:write", "checkpoint:write"}
	case domain.RoleCollector:
		allowed = []string{"status:read", "events:append", "artifacts:write"}
	case domain.RoleVerifier:
		allowed = []string{"status:read", "retrieval:read", "evidence:read", "verification:write"}
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
