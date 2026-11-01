package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"io"
	"math"
	"path"
	"strings"
	"time"
)

const MaxArtifactBytes = domain.MaxArtifactBytes

type ArtifactInput struct {
	Scope                                     domain.Scope
	Digest                                    string
	Bytes                                     int64
	PolicyRevision, SourceLocator, SourcePath string
}
type ObjectStore interface {
	Put(context.Context, string, io.Reader, string) error
	Open(context.Context, string) (io.ReadCloser, error)
	Delete(context.Context, string) error
}
type EvidenceRepository interface {
	Policy(context.Context, Tx, string) (domain.Policy, error)
	Artifact(context.Context, Tx, string) (domain.Artifact, string, error)
	SaveArtifact(context.Context, Tx, domain.Artifact, string) error
	Event(context.Context, Tx, string) (domain.Event, domain.Origin, error)
	SaveEvent(context.Context, Tx, string, domain.Event, domain.Origin) error
	Pending(context.Context, Tx, int) ([]domain.Artifact, error)
	Checked(context.Context, Tx, string, string, int64) error
	Availability(context.Context, Tx, string, string, bool) error
	UploadLease(context.Context, Tx, string, string, string, time.Time) error
}
type CaptureJobs interface {
	Enqueue(context.Context, Tx, domain.Job) error
}
type CaptureService struct {
	tx      Transactor
	repo    EvidenceRepository
	jobs    CaptureJobs
	objects ObjectStore
}

func NewCaptureService(tx Transactor, r EvidenceRepository, j CaptureJobs, o ObjectStore) *CaptureService {
	return &CaptureService{tx, r, j, o}
}

// SourcePath is a slash-separated, relative source identifier, never a server
// filesystem path. Exclusions are exact paths or directory prefixes (trailing
// slash optional), with no glob interpretation. C1 owns field redaction before
// network/spool; revision agreement does not attest that filtering occurred.
func validateSource(policy domain.Policy, source string) error {
	if source != "" && (path.IsAbs(source) || path.Clean(source) != source || source == "." || source == ".." || strings.HasPrefix(source, "../") || strings.Contains(source, "\\")) {
		return domain.ErrInvalidInput
	}
	if len(policy.ExcludedPaths) > 0 && source == "" {
		return domain.ErrForbidden
	}
	for _, excluded := range policy.ExcludedPaths {
		e := strings.TrimSuffix(excluded, "/")
		if e == "" || source == e || strings.HasPrefix(source, e+"/") {
			return domain.ErrForbidden
		}
	}
	return nil
}
func validDigest(d string) bool {
	b, err := hex.DecodeString(d)
	return err == nil && len(b) == sha256.Size && d == strings.ToLower(d)
}
func captureID(parts ...any) string {
	b, _ := json.Marshal(parts)
	d := sha256.Sum256(b)
	return hex.EncodeToString(d[:])
}
func (s *CaptureService) policy(ctx context.Context, tx Tx, a domain.Artifact) error {
	p, err := s.repo.Policy(ctx, tx, a.Scope.ProjectID)
	if err != nil {
		return err
	}
	if p.Revision == "" || p.Paused {
		return domain.ErrForbidden
	}
	if p.Revision != a.PolicyRevision {
		return domain.ErrStale
	}
	if p.SpoolLimitBytes < a.Bytes {
		return domain.ErrBudget
	}
	return validateSource(p, a.SourcePath)
}
func (s *CaptureService) Stage(ctx context.Context, p domain.Principal, m ArtifactInput, body io.Reader) (domain.Artifact, error) {
	if err := Authorize(p, "artifacts:write", []string{m.Scope.ProjectID}); err != nil {
		return domain.Artifact{}, err
	}
	return s.stage(ctx, p.ClientID, m, body, nil)
}

// stage is the shared F4 pipeline. Host authority is rechecked in each mutation
// transaction; the actor is an uploader namespace, never a fabricated Client.
func (s *CaptureService) stage(ctx context.Context, actor string, m ArtifactInput, body io.Reader, check func(context.Context, Tx) error) (domain.Artifact, error) {
	if domain.ValidateIDs(m.Scope.ProjectID, m.PolicyRevision, m.SourceLocator) != nil || !validDigest(m.Digest) || m.Bytes < 0 || m.Bytes > MaxArtifactBytes || len(m.SourceLocator) > 4096 || len(m.SourcePath) > 4096 || body == nil {
		return domain.Artifact{}, domain.ErrInvalidInput
	}

	// Validate one capped chunk before creating persistence or cleanup obligations.
	data, err := io.ReadAll(io.LimitReader(body, MaxArtifactBytes+1))
	if err != nil {
		return domain.Artifact{}, domain.ErrUnavailable
	}
	if int64(len(data)) != m.Bytes || TokenDigest(string(data)) != m.Digest {
		return domain.Artifact{}, domain.ErrInvalidInput
	}
	id := captureID(actor, m.Scope, m.Digest, m.Bytes, m.PolicyRevision, m.SourceLocator, m.SourcePath)
	a := domain.Artifact{ID: id, Digest: m.Digest, PolicyRevision: m.PolicyRevision, Scope: m.Scope, Bytes: m.Bytes, State: domain.ArtifactStaged, SourceLocator: m.SourceLocator, SourcePath: m.SourcePath}
	leaseID := rand.Text()
	err = s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		if check != nil {
			if err := check(ctx, tx); err != nil {
				return err
			}
		}
		if err := s.policy(ctx, tx, a); err != nil {
			return err
		}
		current, _, err := s.repo.Artifact(ctx, tx, id)
		if err != nil && !errors.Is(err, domain.ErrIncomplete) {
			return err
		}
		if err == nil {
			a = current
			switch a.State {
			case domain.ArtifactDeleted:
				return domain.ErrDeleted
			case domain.ArtifactAbandoning:
				return domain.ErrIncomplete
			case domain.ArtifactReady:
				return nil
			}
			if a.UploadLeaseUntil.After(time.Now().UTC()) {
				return domain.ErrConflict
			}
		}
		if a.Generation == "" || a.State == domain.ArtifactAbandoned {
			a.Generation = rand.Text()
			a.ObjectKey = "evidence/" + id + "/" + a.Generation
		}
		a.State = domain.ArtifactStaged
		a.ObjectAvailable = false
		deadline := time.Now().UTC().Add(CaptureAbandonAfter)
		a.ExpiresAt = &deadline
		a.UploadLeaseID = leaseID
		a.UploadLeaseUntil = time.Now().UTC().Add(CaptureUploadLease)
		if err = s.repo.SaveArtifact(ctx, tx, a, actor); err != nil {
			return err
		}
		return s.enqueue(ctx, tx, a, "capture.reconcile")
	})
	if err != nil {
		return domain.Artifact{}, err
	}
	putCtx, cancel := context.WithTimeout(ctx, CapturePutTimeout)
	putErr := s.objects.Put(putCtx, a.ObjectKey, bytes.NewReader(data), a.Digest)
	cancel()
	// Parent cancellation is authoritative; a failed settlement leaves only the
	// bounded persisted lease, not an unbounded detached cleanup goroutine.
	err = s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		if check != nil {
			if err := check(ctx, tx); err != nil {
				return err
			}
		}
		current, _, err := s.repo.Artifact(ctx, tx, a.ID)
		if err != nil {
			return err
		}
		if current.ObjectKey != a.ObjectKey {
			return domain.ErrStale
		}
		if current.UploadLeaseID == leaseID {
			if err = s.repo.UploadLease(ctx, tx, a.ID, a.ObjectKey, "", time.Time{}); err != nil {
				return err
			}
		}
		if current.State == domain.ArtifactDeleted {
			return domain.ErrDeleted
		}
		if current.State == domain.ArtifactAbandoning || current.State == domain.ArtifactAbandoned {
			return domain.ErrIncomplete
		}
		return s.policy(ctx, tx, current)
	})
	if err != nil {
		return domain.Artifact{}, err
	}
	if putErr != nil {
		return domain.Artifact{}, putErr
	}
	a.UploadLeaseID = ""
	a.UploadLeaseUntil = time.Time{}
	return a, nil
}
func (s *CaptureService) enqueue(ctx context.Context, tx Tx, a domain.Artifact, kind string) error {
	digest := captureID(a.ID, a.Digest)
	id := captureID(kind, a.ID)
	revisions := []string{a.ID}
	if kind == "capture.reconcile" {
		digest = captureID(a.ID, a.Digest, a.Generation)
		id = captureID(kind, a.ID, a.Generation)
		revisions = append(revisions, a.Generation)
	}
	j := domain.Job{ID: id, Kind: kind, InputDigest: digest, PolicyRevision: a.PolicyRevision, Scope: a.Scope, InputRevisionIDs: revisions, DueAt: time.Now().UTC()}
	if domain.ValidateIDs(j.ID, j.Kind, j.InputDigest, j.PolicyRevision) != nil {
		return domain.ErrInvalidInput
	}
	return s.jobs.Enqueue(ctx, tx, j)
}
func (s *CaptureService) load(ctx context.Context, p domain.Principal, id, cap string) (domain.Artifact, error) {
	if domain.ValidateIDs(id) != nil {
		return domain.Artifact{}, domain.ErrInvalidInput
	}
	if err := Authorize(p, cap, p.ProjectIDs); err != nil {
		return domain.Artifact{}, err
	}
	var a domain.Artifact
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		var owner string
		var err error
		a, owner, err = s.repo.Artifact(ctx, tx, id)
		if err != nil {
			return domain.ErrForbidden
		}
		if err = Authorize(p, cap, []string{a.Scope.ProjectID}); err != nil {
			return err
		}
		if cap == "artifacts:write" && owner != p.ClientID {
			return domain.ErrForbidden
		}
		if a.State == domain.ArtifactDeleted {
			return domain.ErrDeleted
		}
		if a.State == domain.ArtifactAbandoning || a.State == domain.ArtifactAbandoned {
			return domain.ErrIncomplete
		}
		return s.policy(ctx, tx, a)
	})
	return a, err
}
func (s *CaptureService) checkObject(ctx context.Context, a domain.Artifact) error {
	r, err := s.objects.Open(ctx, a.ObjectKey)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(r, MaxArtifactBytes+1))
	closeErr := r.Close()
	if err != nil {
		return domain.ErrUnavailable
	}
	if n != a.Bytes || hex.EncodeToString(h.Sum(nil)) != a.Digest {
		return domain.ErrIncomplete
	}
	if closeErr != nil {
		return domain.ErrUnavailable
	}
	return nil
}
func (s *CaptureService) publish(ctx context.Context, tx Tx, a domain.Artifact) (domain.Receipt, error) {
	current, client, err := s.repo.Artifact(ctx, tx, a.ID)
	if err != nil {
		return domain.Receipt{}, err
	}
	if current.State == domain.ArtifactDeleted {
		return domain.Receipt{}, domain.ErrDeleted
	}
	if current.ObjectKey != a.ObjectKey || current.Generation != a.Generation {
		return domain.Receipt{}, domain.ErrStale
	}
	if current.State != domain.ArtifactStaged && current.State != domain.ArtifactReady {
		return domain.Receipt{}, domain.ErrIncomplete
	}
	if current.State == domain.ArtifactStaged && current.ExpiresAt != nil && !current.ExpiresAt.After(time.Now().UTC()) {
		return domain.Receipt{}, domain.ErrIncomplete
	}
	if err = s.policy(ctx, tx, current); err != nil {
		return domain.Receipt{}, err
	}
	duplicate := current.State == domain.ArtifactReady
	if !duplicate {
		current.State = domain.ArtifactReady
		current.ExpiresAt = nil
		current.UploadLeaseID = ""
		current.UploadLeaseUntil = time.Time{}
		current.ObjectAvailable = true
		if err = s.repo.SaveArtifact(ctx, tx, current, client); err != nil {
			return domain.Receipt{}, err
		}
	}
	if err = s.repo.Availability(ctx, tx, current.ID, current.ObjectKey, true); err != nil {
		return domain.Receipt{}, err
	}
	if err = s.enqueue(ctx, tx, current, "evidence.extract"); err != nil {
		return domain.Receipt{}, err
	}
	return domain.Receipt{ID: a.ID, Duplicate: duplicate, State: "ready"}, nil
}
func (s *CaptureService) Complete(ctx context.Context, p domain.Principal, id, digest string) (domain.Receipt, error) {
	if !validDigest(digest) {
		return domain.Receipt{}, domain.ErrInvalidInput
	}
	a, err := s.load(ctx, p, id, "artifacts:write")
	if err != nil {
		return domain.Receipt{}, err
	}
	if a.Digest != digest {
		return domain.Receipt{}, domain.ErrConflict
	}
	if err = s.verifyObject(ctx, a); err != nil {
		return domain.Receipt{}, err
	}
	var receipt domain.Receipt
	err = s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		receipt, err = s.publish(ctx, tx, a)
		return err
	})
	return receipt, err
}
func validEvent(e domain.Event) bool {
	if domain.ValidateIDs(e.ID, e.IdempotencyKey, e.Scope.ProjectID, e.PayloadArtifactID) != nil || !validDigest(e.Digest) || e.ObservedAt.IsZero() || e.Sequence > math.MaxInt64 {
		return false
	}
	switch e.Kind {
	case domain.EventMessage, domain.EventToolCall, domain.EventToolResult, domain.EventCheckpoint, domain.EventArtifact:
	default:
		return false
	}
	switch e.Completeness {
	case domain.CompletenessComplete, domain.CompletenessPartial, domain.CompletenessWithheld:
		return true
	}
	return false
}
func (s *CaptureService) Append(ctx context.Context, p domain.Principal, events []domain.Event) ([]domain.Receipt, error) {
	if len(events) < 1 || len(events) > 100 {
		return nil, domain.ErrInvalidInput
	}
	for _, e := range events {
		if !validEvent(e) {
			return nil, domain.ErrInvalidInput
		}
		if err := Authorize(p, "events:append", []string{e.Scope.ProjectID}); err != nil {
			return nil, err
		}
	}
	for _, e := range events {
		a, err := s.load(ctx, p, e.PayloadArtifactID, "events:append")
		if err != nil {
			return nil, err
		}
		if a.Scope != e.Scope {
			return nil, domain.ErrForbidden
		}
		if a.State != domain.ArtifactReady || !a.ObjectAvailable {
			return nil, domain.ErrIncomplete
		}
		if a.Digest != e.Digest {
			return nil, domain.ErrConflict
		}
		if err = s.verifyObject(ctx, a); err != nil {
			return nil, err
		}
	}
	receipts := make([]domain.Receipt, 0, len(events))
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		for _, e := range events {
			key := captureID(p.ClientID, e.Scope, e.IdempotencyKey)
			existing, _, err := s.repo.Event(ctx, tx, key)
			if err == nil {
				a, _, loadErr := s.repo.Artifact(ctx, tx, existing.PayloadArtifactID)
				if loadErr != nil {
					return domain.ErrForbidden
				}
				if loadErr = s.policy(ctx, tx, a); loadErr != nil {
					return loadErr
				}
				if a.State != domain.ArtifactReady || !a.ObjectAvailable {
					return domain.ErrIncomplete
				}
				if existing.Digest != e.Digest || captureID(existing.Scope, existing.Kind, existing.PayloadArtifactID, existing.Sequence, existing.Completeness) != captureID(e.Scope, e.Kind, e.PayloadArtifactID, e.Sequence, e.Completeness) {
					return domain.ErrConflict
				}
				receipts = append(receipts, domain.Receipt{ID: existing.ID, Duplicate: true, State: "submitted"})
				continue
			}
			if !errors.Is(err, domain.ErrIncomplete) {
				return err
			}
			a, client, err := s.repo.Artifact(ctx, tx, e.PayloadArtifactID)
			if err != nil || client != p.ClientID || a.Scope != e.Scope {
				return domain.ErrForbidden
			}
			if a.State != domain.ArtifactReady || !a.ObjectAvailable {
				return domain.ErrIncomplete
			}
			if e.Digest != a.Digest {
				return domain.ErrConflict
			}
			if err = s.policy(ctx, tx, a); err != nil {
				return err
			}
			// No submitted role, exit code or collector role authenticates observation.
			origin := domain.Origin{ClientID: p.ClientID, Trust: domain.TrustSubmitted}
			e.ID = captureID(p.ClientID, e.Scope, e.ID)
			e.ObservedAt = e.ObservedAt.UTC()
			if err = s.repo.SaveEvent(ctx, tx, key, e, origin); err != nil {
				return err
			}
			receipts = append(receipts, domain.Receipt{ID: e.ID, State: "submitted"})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return receipts, nil
}
func (s *CaptureService) Reference(ctx context.Context, p domain.Principal, ref domain.EvidenceRef, scope domain.Scope) (domain.EvidenceRef, error) {
	a, err := s.load(ctx, p, ref.ArtifactID, "evidence:read")
	if err != nil {
		return domain.EvidenceRef{}, err
	}
	if a.Scope != scope {
		return domain.EvidenceRef{}, domain.ErrForbidden
	}
	if a.State != domain.ArtifactReady || !a.ObjectAvailable {
		return domain.EvidenceRef{}, domain.ErrIncomplete
	}
	if a.Digest != ref.Digest || ref.Locator != a.SourceLocator {
		return domain.EvidenceRef{}, domain.ErrConflict
	}
	if err = s.verifyObject(ctx, a); err != nil {
		return domain.EvidenceRef{}, err
	}
	// Caller trust never crosses the boundary. Capture proves integrity only.
	ref.Origin = domain.Origin{Trust: domain.TrustSubmitted}
	err = s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		current, client, err := s.repo.Artifact(ctx, tx, a.ID)
		if err != nil {
			return err
		}
		if current.State != domain.ArtifactReady || !current.ObjectAvailable {
			return domain.ErrIncomplete
		}
		ref.Origin.ClientID = client
		return s.policy(ctx, tx, current)
	})
	if err != nil {
		return domain.EvidenceRef{}, err
	}
	return ref, nil
}
