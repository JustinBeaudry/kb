package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

// ExportPageSize bounds one export chunk. It equals the eligibility bound, so
// every chunk is authorized by a single authoritative eligibility read.
const ExportPageSize = 50

const exportCapability = "memory:export"

type ExportHead = domain.ExportHead

// TransferRepository lists candidate export identities only. It never decides
// visibility; the authorizer does that for every chunk.
type TransferRepository interface {
	ExportPage(ctx context.Context, tx Tx, scope domain.Scope, after string, limit int) ([]ExportHead, error)
}

// TransferAuthorizer is the authoritative, tombstone-aware retrieval boundary.
type TransferAuthorizer interface {
	Snapshot(context.Context, Tx, domain.Principal, domain.Scope) (RetrievalSnapshot, error)
	Eligible(context.Context, Tx, domain.Principal, domain.Scope, []string) (EligibilityResult, error)
}

// ExportedClaim carries a stored claim unchanged. OriginTrust is the weakest
// evidence origin so a portable copy never reads as more trusted than its
// least trusted support.
type ExportedClaim struct {
	Claim       domain.ClaimRevision
	OriginTrust domain.OriginTrust
}

// ExportManifest is one authorized, resumable export chunk.
type ExportManifest struct {
	Scope        domain.Scope
	Claims       []ExportedClaim
	Omitted      int
	Completeness domain.Completeness
	Continuation string
}

type TransferService struct {
	tx   Transactor
	repo TransferRepository
	auth TransferAuthorizer
	key  []byte
}

func NewTransferService(tx Transactor, repo TransferRepository, auth TransferAuthorizer, key []byte) (*TransferService, error) {
	if len(key) < 32 {
		return nil, domain.ErrUnavailable
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte("kb-export-cursor-v1"))
	return &TransferService{tx: tx, repo: repo, auth: auth, key: m.Sum(nil)}, nil
}

type exportCursor struct {
	Version int          `json:"v"`
	Client  string       `json:"c"`
	Scope   domain.Scope `json:"s"`
	After   string       `json:"a"`
}

func (s *TransferService) sign(payload []byte) []byte {
	m := hmac.New(sha256.New, s.key)
	m.Write(payload)
	return m.Sum(nil)
}

func (s *TransferService) encodeCursor(c exportCursor) string {
	payload, _ := json.Marshal(c)
	enc := base64.RawURLEncoding
	return enc.EncodeToString(payload) + "." + enc.EncodeToString(s.sign(payload))
}

// decodeCursor authenticates a cursor and its binding to the same client and
// scope. Any mismatch is stale: the caller restarts the export.
func (s *TransferService) decodeCursor(raw string, p domain.Principal, scope domain.Scope) (string, error) {
	if raw == "" {
		return "", nil
	}
	parts := strings.Split(raw, ".")
	if len(raw) > 2048 || len(parts) != 2 {
		return "", domain.ErrStale
	}
	enc := base64.RawURLEncoding
	payload, err := enc.DecodeString(parts[0])
	if err != nil {
		return "", domain.ErrStale
	}
	sig, err := enc.DecodeString(parts[1])
	if err != nil || !hmac.Equal(sig, s.sign(payload)) {
		return "", domain.ErrStale
	}
	var c exportCursor
	if json.Unmarshal(payload, &c) != nil || c.Version != 1 || c.Client != p.ClientID || c.Scope != scope || c.After == "" {
		return "", domain.ErrStale
	}
	return c.After, nil
}

func trustRank(t domain.OriginTrust) int {
	switch t {
	case domain.TrustOwnerAuthenticated:
		return 3
	case domain.TrustGatewayObserved:
		return 2
	case domain.TrustCollectorAttested:
		return 1
	default:
		return 0
	}
}

func weakestTrust(refs []domain.EvidenceRef) domain.OriginTrust {
	if len(refs) == 0 {
		return domain.TrustSubmitted
	}
	weakest := refs[0].Origin.Trust
	for _, r := range refs[1:] {
		if trustRank(r.Origin.Trust) < trustRank(weakest) {
			weakest = r.Origin.Trust
		}
	}
	if trustRank(weakest) == 0 {
		return domain.TrustSubmitted
	}
	return weakest
}

func inScope(requested, claim domain.Scope) bool {
	return claim.ProjectID == requested.ProjectID &&
		(requested.TaskID == "" || claim.TaskID == requested.TaskID) &&
		(requested.SessionID == "" || claim.SessionID == requested.SessionID)
}

// Export returns one authorized chunk of the requested scope. Every chunk
// re-reads the principal's current grants and epochs and re-checks every
// record through the tombstone-aware eligibility boundary, so revocation or
// deletion between chunks is observed. Stored status and provenance are
// reported unchanged; nothing is promoted.
func (s *TransferService) Export(ctx context.Context, p domain.Principal, scope domain.Scope, continuation string) (ExportManifest, error) {
	if domain.ValidateIDs(scope.ProjectID) != nil || (scope.TaskID != "" && domain.ValidateIDs(scope.TaskID) != nil) || (scope.SessionID != "" && domain.ValidateIDs(scope.SessionID) != nil) {
		return ExportManifest{}, domain.ErrInvalidInput
	}
	if err := authorize(ctx, p, exportCapability, []string{scope.ProjectID}); err != nil {
		return ExportManifest{}, err
	}
	after, err := s.decodeCursor(continuation, p, scope)
	if err != nil {
		return ExportManifest{}, err
	}
	out := ExportManifest{Scope: scope, Completeness: domain.CompletenessComplete}
	err = s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		out = ExportManifest{Scope: scope, Completeness: domain.CompletenessComplete}
		snapshot, err := s.auth.Snapshot(ctx, tx, p, scope)
		if err != nil {
			return err
		}
		if err = authorize(ctx, snapshot.Principal, exportCapability, []string{scope.ProjectID}); err != nil {
			return err
		}
		heads, err := s.repo.ExportPage(ctx, tx, scope, after, ExportPageSize+1)
		if err != nil {
			return err
		}
		more := len(heads) > ExportPageSize
		if more {
			heads = heads[:ExportPageSize]
		}
		if len(heads) == 0 {
			return nil
		}
		ids := make([]string, len(heads))
		for i, h := range heads {
			ids[i] = h.Revision
		}
		eligible, err := s.auth.Eligible(ctx, tx, p, scope, ids)
		if err != nil {
			return err
		}
		visible := make(map[string]domain.ClaimRevision, len(eligible.Claims))
		for _, c := range eligible.Claims {
			visible[c.Revision] = c
		}
		for _, h := range heads {
			c, ok := visible[h.Revision]
			if !ok || c.ClaimID != h.ClaimID || !inScope(scope, c.Scope) {
				out.Omitted++
				continue
			}
			out.Claims = append(out.Claims, ExportedClaim{Claim: c, OriginTrust: weakestTrust(c.Evidence)})
		}
		if out.Omitted > 0 || eligible.Unavailable {
			out.Completeness = domain.CompletenessPartial
		}
		if more {
			out.Continuation = s.encodeCursor(exportCursor{Version: 1, Client: p.ClientID, Scope: scope, After: heads[len(heads)-1].ClaimID})
		}
		return nil
	})
	if err != nil {
		return ExportManifest{}, err
	}
	return out, nil
}
