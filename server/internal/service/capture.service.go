package service

import (
	"bytes"
	"context"
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

const MaxArtifactBytes int64 = 8 << 20

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
	Checked(context.Context, Tx, string, int64) error
	Availability(context.Context, Tx, string, bool) error
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
	if domain.ValidateIDs(m.Scope.ProjectID, m.PolicyRevision, m.SourceLocator) != nil || !validDigest(m.Digest) || m.Bytes < 0 || m.Bytes > MaxArtifactBytes || len(m.SourceLocator) > 4096 || len(m.SourcePath) > 4096 || body == nil {
		return domain.Artifact{}, domain.ErrInvalidInput
	}
	if err := Authorize(p, "artifacts:write", []string{m.Scope.ProjectID}); err != nil {
		return domain.Artifact{}, err
	}
	id := captureID(p.ClientID, m.Scope, m.Digest, m.Bytes, m.PolicyRevision, m.SourceLocator, m.SourcePath)
	a := domain.Artifact{ID: id, ObjectKey: "evidence/" + id, Digest: m.Digest, PolicyRevision: m.PolicyRevision, Scope: m.Scope, Bytes: m.Bytes, State: domain.ArtifactStaged, SourceLocator: m.SourceLocator, SourcePath: m.SourcePath}
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		if err := s.policy(ctx, tx, a); err != nil {
			return err
		}
		existing, _, err := s.repo.Artifact(ctx, tx, id)
		if err == nil {
			a = existing
			return nil
		}
		if !errors.Is(err, domain.ErrIncomplete) {
			return err
		}
		if err = s.repo.SaveArtifact(ctx, tx, a, p.ClientID); err != nil {
			return err
		}
		return s.enqueue(ctx, tx, a, "capture.reconcile")
	})
	if err != nil {
		return domain.Artifact{}, err
	}
	// Bounded by one chunk, including one sentinel byte to reject oversized input.
	data, err := io.ReadAll(io.LimitReader(body, MaxArtifactBytes+1))
	if err != nil {
		return domain.Artifact{}, domain.ErrUnavailable
	}
	if int64(len(data)) != m.Bytes || TokenDigest(string(data)) != m.Digest {
		return domain.Artifact{}, domain.ErrInvalidInput
	}
	if a.State == domain.ArtifactDeleted {
		return domain.Artifact{}, domain.ErrDeleted
	}
	// Content-addressed immutable key: retries can only write the validated bytes.
	if err = s.objects.Put(ctx, a.ObjectKey, bytes.NewReader(data), a.Digest); err != nil {
		return domain.Artifact{}, err
	}
	// Policy may have changed while object I/O was in progress. Never acknowledge
	// the object as usable without rechecking authoritative state.
	err = s.tx.Within(ctx, func(ctx context.Context, tx Tx) error { return s.policy(ctx, tx, a) })
	if err != nil {
		return domain.Artifact{}, err
	}
	return a, nil
}
func (s *CaptureService) enqueue(ctx context.Context, tx Tx, a domain.Artifact, kind string) error {
	// The input digest binds immutable identity, client/scope and policy, not raw payload.
	j := domain.Job{ID: captureID(kind, a.ID), Kind: kind, InputDigest: captureID(a.ID, a.Digest), PolicyRevision: a.PolicyRevision, Scope: a.Scope, InputRevisionIDs: []string{a.ID}, DueAt: time.Now().UTC()}
	if domain.ValidateIDs(j.ID, j.Kind, j.InputDigest, j.PolicyRevision, j.InputRevisionIDs[0]) != nil {
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
		return s.policy(ctx, tx, a)
	})
	return a, err
}
func (s *CaptureService) checkObject(ctx context.Context, a domain.Artifact) error {
	r, err := s.objects.Open(ctx, a.ObjectKey)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(r, MaxArtifactBytes+1))
	if err != nil {
		return domain.ErrUnavailable
	}
	if n != a.Bytes || hex.EncodeToString(h.Sum(nil)) != a.Digest {
		return domain.ErrIncomplete
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
	if err = s.policy(ctx, tx, current); err != nil {
		return domain.Receipt{}, err
	}
	duplicate := current.State == domain.ArtifactReady
	if !duplicate {
		current.State = domain.ArtifactReady
		current.ObjectAvailable = true
		if err = s.repo.SaveArtifact(ctx, tx, current, client); err != nil {
			return domain.Receipt{}, err
		}
	}
	if err = s.repo.Availability(ctx, tx, current.ID, true); err != nil {
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
	if err = s.checkObject(ctx, a); err != nil {
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
		if err = s.checkObject(ctx, a); err != nil {
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
	if err = s.checkObject(ctx, a); err != nil {
		healthErr := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error { return s.repo.Availability(ctx, tx, a.ID, false) })
		if healthErr != nil {
			return domain.EvidenceRef{}, healthErr
		}
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

// Reconcile performs object I/O outside JobsService.Commit. Its callback only
// publishes graph state with the live lease fence. Extraction stays queued.
func (s *CaptureService) Reconcile(ctx context.Context, job domain.Job, lease domain.Lease, jobs interface {
	Commit(context.Context, domain.Lease, string, func(context.Context, Tx) error) error
}) error {
	if job.Kind != "capture.reconcile" || len(job.InputRevisionIDs) != 1 {
		return domain.ErrInvalidInput
	}
	var a domain.Artifact
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		a, _, err = s.repo.Artifact(ctx, tx, job.InputRevisionIDs[0])
		if err != nil {
			return err
		}
		if job.PolicyRevision != a.PolicyRevision || job.InputDigest != captureID(a.ID, a.Digest) || job.Scope != a.Scope {
			return domain.ErrConflict
		}
		return s.policy(ctx, tx, a)
	})
	if err != nil {
		return err
	}
	if a.State == domain.ArtifactDeleted {
		return nil
	}
	if err = s.checkObject(ctx, a); err != nil {
		return err
	}
	return jobs.Commit(ctx, lease, job.InputDigest, func(ctx context.Context, tx Tx) error { _, err := s.publish(ctx, tx, a); return err })
}

// Sweep audits bounded batches ordered by last check, so missing ready objects
// cannot remain silently usable and interrupted uploads can finish without the
// caller. Missing objects stay explicit incomplete manifests; no extraction is
// acknowledged by this operation. It is safe alongside leased reconciliation.
func (s *CaptureService) Sweep(ctx context.Context, limit int) error {
	if limit < 1 || limit > 100 {
		return domain.ErrInvalidInput
	}
	var artifacts []domain.Artifact
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		artifacts, err = s.repo.Pending(ctx, tx, limit)
		return err
	})
	if err != nil {
		return err
	}
	var failures error
	for _, a := range artifacts {
		checkErr := s.checkObject(ctx, a)
		err = s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
			if err := s.repo.Availability(ctx, tx, a.ID, checkErr == nil); err != nil {
				return err
			}
			if err := s.repo.Checked(ctx, tx, a.ID, time.Now().UTC().UnixNano()); err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			return err
		}
		if checkErr != nil {
			failures = errors.Join(failures, checkErr)
			continue
		}
		if a.State == domain.ArtifactStaged {
			err = s.tx.Within(ctx, func(ctx context.Context, tx Tx) error { _, err := s.publish(ctx, tx, a); return err })
			if err != nil {
				failures = errors.Join(failures, err)
			}
		}
	}
	return failures
}
