package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

type RetrievalSnapshot = domain.RetrievalSnapshot
type EligibilityResult = domain.EligibilityResult
type RetrievalRepository interface {
	Snapshot(context.Context, Tx, domain.Principal, domain.Scope) (RetrievalSnapshot, error)
	Seeds(context.Context, Tx, domain.Scope, string, int) ([]domain.ClaimRevision, error)
	Neighbors(context.Context, Tx, []string, []string, int) ([]domain.ClaimRevision, error)
	Eligible(context.Context, Tx, domain.Principal, domain.Scope, []string) (EligibilityResult, error)
}
type TaskStateReader interface {
	Latest(context.Context, Tx, domain.Scope) (domain.Checkpoint, string, error)
}
type RetrievalEvidence interface {
	Artifact(context.Context, Tx, string) (domain.Artifact, string, error)
	Policy(context.Context, Tx, string) (domain.Policy, error)
	Availability(context.Context, Tx, string, string, bool) error
}
type RetrievalService struct {
	tx       Transactor
	repo     RetrievalRepository
	tasks    TaskStateReader
	evidence RetrievalEvidence
	objects  ObjectStore
	key      []byte
}

func NewRetrievalService(tx Transactor, r RetrievalRepository, t TaskStateReader, e RetrievalEvidence, o ObjectStore, key []byte) (*RetrievalService, error) {
	if len(key) < 32 {
		return nil, domain.ErrInvalidInput
	}
	return &RetrievalService{tx, r, t, e, o, slices.Clone(key)}, nil
}

type retrievalCursor struct {
	Set                       string `json:"s"`
	Query                     string `json:"q"`
	Auth, Knowledge, Deletion uint64
	Head                      string `json:"h,omitempty"`
	Offset                    int    `json:"o"`
}

func (s *RetrievalService) sign(c retrievalCursor) string {
	data, _ := json.Marshal(c)
	mac := hmac.New(sha256.New, s.key)
	mac.Write(data)
	return base64.RawURLEncoding.EncodeToString(data) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (s *RetrievalService) cursor(raw string) (retrievalCursor, error) {
	var c retrievalCursor
	parts := strings.Split(raw, ".")
	if len(parts) != 2 || len(raw) > 2048 {
		return c, domain.ErrStale
	}
	data, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil {
		return c, domain.ErrStale
	}
	sig, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil {
		return c, domain.ErrStale
	}
	m := hmac.New(sha256.New, s.key)
	m.Write(data)
	if !hmac.Equal(sig, m.Sum(nil)) || json.Unmarshal(data, &c) != nil || c.Offset < 0 || c.Offset > 50 {
		return c, domain.ErrStale
	}
	return c, nil
}
func normalizedRequest(r domain.RetrievalRequest) (domain.RetrievalRequest, error) {
	if domain.ValidateIDs(r.Scope.ProjectID) != nil || len(r.Query) > 4096 || len(r.EntityIDs) > 50 {
		return r, domain.ErrInvalidInput
	}
	if r.Mode == "" {
		r.Mode = domain.RetrievalOrientation
	}
	maxBytes := domain.EvidenceBudget
	if r.Mode == domain.RetrievalOrientation {
		maxBytes = domain.OrientationBudget
	}
	if r.Mode != domain.RetrievalOrientation && r.Mode != domain.RetrievalEvidence && r.Mode != domain.RetrievalWorking {
		return r, domain.ErrInvalidInput
	}
	if r.Mode == domain.RetrievalWorking && r.Scope.TaskID == "" {
		return r, domain.ErrInvalidInput
	}
	if r.BudgetBytes == 0 {
		r.BudgetBytes = maxBytes
	}
	if r.MaxVisited == 0 {
		r.MaxVisited = 50
	}
	if r.BudgetBytes < 1 || r.BudgetBytes > maxBytes || r.MaxVisited < 1 || r.MaxVisited > 50 || r.MaxDepth < 0 || r.MaxDepth > 2 {
		return r, domain.ErrInvalidInput
	}
	for _, id := range r.EntityIDs {
		if domain.ValidateIDs(id) != nil {
			return r, domain.ErrInvalidInput
		}
	}
	return r, nil
}
func sameEpoch(a, b RetrievalSnapshot) bool {
	return a.AuthEpoch == b.AuthEpoch && a.KnowledgeEpoch == b.KnowledgeEpoch && a.DeletionEpoch == b.DeletionEpoch && a.TaskHead == b.TaskHead && a.TaskOwner == b.TaskOwner
}
func (s *RetrievalService) Retrieve(ctx context.Context, p domain.Principal, input domain.RetrievalRequest) (domain.RetrievalResult, error) {
	r, err := normalizedRequest(input)
	if err != nil {
		return domain.RetrievalResult{}, err
	}
	if err = Authorize(p, "retrieval:read", []string{r.Scope.ProjectID}); err != nil {
		return domain.RetrievalResult{}, err
	}
	clean := r
	clean.Continuation = ""
	query := captureID(p.ClientID, clean)
	var old retrievalCursor
	if r.Continuation != "" {
		old, err = s.cursor(r.Continuation)
		if err != nil {
			return domain.RetrievalResult{}, err
		}
		if old.Query != query {
			return domain.RetrievalResult{}, domain.ErrStale
		}
	}
	var result domain.RetrievalResult
	// All graph reads, eligibility and final epoch validation share OwnerState's
	// write lock. No object I/O occurs in this callback. A later revocation waits.
	err = s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		snap, e := s.repo.Snapshot(ctx, tx, p, r.Scope)
		if e != nil {
			return e
		}
		if e = Authorize(snap.Principal, "retrieval:read", []string{r.Scope.ProjectID}); e != nil {
			return e
		}
		if r.Scope.TaskID != "" && snap.TaskOwner != "" && snap.TaskOwner != p.ClientID {
			return domain.ErrForbidden
		}
		if r.Continuation != "" && (old.Auth != snap.AuthEpoch || old.Knowledge != snap.KnowledgeEpoch || old.Deletion != snap.DeletionEpoch || old.Head != snap.TaskHead) {
			return domain.ErrStale
		}
		result = domain.RetrievalResult{SchemaVersion: "3", Chunks: []domain.EvidenceChunk{}}
		if r.Scope.TaskID != "" && (r.Mode == domain.RetrievalWorking || r.Mode == domain.RetrievalOrientation) && s.tasks != nil {
			c, owner, e := s.tasks.Latest(ctx, tx, r.Scope)
			if e != nil {
				return e
			}
			if c.ID != "" {
				if owner != p.ClientID {
					return domain.ErrForbidden
				} // Checkpoint references are task data, never asserted claim support.
				if len(c.EvidenceRevisionIDs) > 0 {
					c.EvidenceRevisionIDs = nil
					c.EvidenceReferencesOmitted = true
				}
				result.TaskCheckpoint = &domain.TaskState{Label: "task_state", Checkpoint: c}
				wire, _ := domain.RetrievalWire(result)
				if len(wire)+1 > r.BudgetBytes {
					result.TaskCheckpoint = nil
					result.TaskStateOmitted = true
					result.TruncationReason = "task_state_budget"
					result.Action = "Task state exceeds this budget; use the checkpoint endpoint or a larger supported budget."
					result.Truncated = true
				}
			}
		}
		queryText := r.Query
		if len(r.EntityIDs) > 0 {
			encoded, _ := json.Marshal(r.EntityIDs)
			queryText = "@ids:" + string(encoded)
		}
		batch, e := s.repo.Seeds(ctx, tx, r.Scope, queryText, r.MaxVisited)
		if e != nil {
			return e
		}
		seen := map[string]bool{}
		var claims []domain.ClaimRevision
		unavailable := false
		for depth := 0; len(batch) > 0 && depth <= r.MaxDepth; depth++ {
			remaining := r.MaxVisited - result.Visited
			if len(batch) > remaining {
				return domain.ErrUnavailable
			}
			result.Visited += len(batch)
			ids := make([]string, 0, len(batch))
			for _, c := range batch {
				if !seen[c.Revision] && (c.Scope.TaskID == "" || (r.Mode == domain.RetrievalWorking && r.Scope.TaskID == c.Scope.TaskID && r.Scope.ProjectID == c.Scope.ProjectID)) {
					seen[c.Revision] = true
					ids = append(ids, c.Revision)
				}
			}
			eligible, e := s.repo.Eligible(ctx, tx, p, r.Scope, ids)
			if e != nil {
				return e
			}
			if eligible.Snapshot.AuthEpoch != snap.AuthEpoch || eligible.Snapshot.KnowledgeEpoch != snap.KnowledgeEpoch || eligible.Snapshot.DeletionEpoch != snap.DeletionEpoch {
				return domain.ErrStale
			}
			unavailable = unavailable || eligible.Unavailable
			next := make([]string, 0, len(eligible.Claims))
			for _, c := range eligible.Claims {
				if !retrievalScope(c, r) || !applicable(c.Applicability, r.Applicability) {
					continue
				}
				claims = append(claims, c)
				next = append(next, c.Revision)
			}
			if result.Visited >= r.MaxVisited {
				result.Truncated = true
				result.TruncationReason = "visited_limit"
				result.Action = "Narrow the original query or provide entity_ids."
				break
			}
			if depth == r.MaxDepth || len(next) == 0 {
				break
			}
			batch, e = s.repo.Neighbors(ctx, tx, next, []string{"SUPPORTS", "CONTRADICTS", "SUPERSEDES", "RELATED"}, r.MaxVisited-result.Visited)
			if e != nil {
				return e
			}
		}
		sort.SliceStable(claims, func(i, j int) bool {
			a, b := applicabilityRank(claims[i].Applicability, r.Applicability), applicabilityRank(claims[j].Applicability, r.Applicability)
			if a != b {
				return a > b
			}
			return claims[i].Revision < claims[j].Revision
		})
		setDigest := captureID(claims)
		if r.Continuation != "" && old.Set != setDigest {
			return domain.ErrStale
		}
		baseTruncated := result.Truncated
		baseReason, baseAction := result.TruncationReason, result.Action
		counts := map[string]int{}
		for _, c := range claims {
			counts[c.ClaimID]++
		}
		offset := old.Offset
		if offset > len(claims) {
			return domain.ErrStale
		}
		for i := offset; i < len(claims); i++ {
			c := claims[i]
			trust := domain.TrustSubmitted
			if len(c.Evidence) > 0 {
				trust = c.Evidence[0].Origin.Trust
			}
			warnings := []string{}
			if counts[c.ClaimID] > 1 || c.Status == domain.ClaimDisputed {
				warnings = append(warnings, "conflicting_versions")
			}
			if c.Status == domain.ClaimCandidate {
				warnings = append(warnings, "unverified_task_candidate")
			}
			if c.Scope.ProjectID == "" {
				warnings = append(warnings, "generalized_lesson_source_withheld")
			}
			chunk := domain.EvidenceChunk{RecordID: c.ClaimID, Revision: c.Revision, Text: c.Text, ClaimStatus: c.Status, OriginTrust: trust, Evidence: c.Evidence, Warnings: warnings, Applicability: targetApplicability(c.Applicability)}
			trial := result
			trial.Chunks = append(slices.Clone(result.Chunks), chunk)
			trial.NoResults = false
			trial.Truncated = baseTruncated || i+1 < len(claims)
			// Reserve the actual signed next cursor in every trial, including escaping.
			if i+1 < len(claims) {
				trial.Truncated = true
				trial.TruncationReason = "wire_budget"
				trial.Action = "Use the continuation with identical parameters; otherwise narrow the query or use a larger supported budget."
				trial.Continuation = s.sign(retrievalCursor{Set: setDigest, Query: query, Auth: snap.AuthEpoch, Knowledge: snap.KnowledgeEpoch, Deletion: snap.DeletionEpoch, Head: snap.TaskHead, Offset: i + 1})
			} else {
				trial.Continuation = ""
				trial.TruncationReason = baseReason
				trial.Action = baseAction
			}
			wire, _ := domain.RetrievalWire(trial)
			if len(wire)+1 > r.BudgetBytes {
				if len(result.Chunks) == 0 && result.TaskCheckpoint == nil {
					return domain.ErrBudget
				}
				result.Truncated = true
				result.TruncationReason = "wire_budget"
				result.Action = "Use the continuation when present; otherwise narrow the query or use a larger supported budget."
				break
			}
			result = trial
		}
		result.NoResults = len(result.Chunks) == 0 && result.TaskCheckpoint == nil && !result.TaskStateOmitted
		if result.NoResults && unavailable {
			return domain.ErrUnavailable
		}
		wire, _ := domain.RetrievalWire(result)
		if len(wire)+1 > r.BudgetBytes {
			return domain.ErrBudget
		}
		final, e := s.repo.Snapshot(ctx, tx, p, r.Scope)
		if e != nil {
			return e
		}
		if !sameEpoch(snap, final) {
			return domain.ErrStale
		}
		return nil
	})
	if err != nil {
		return domain.RetrievalResult{}, err
	}
	return result, nil
}
func retrievalScope(c domain.ClaimRevision, r domain.RetrievalRequest) bool {
	if c.Status == domain.ClaimCandidate {
		return r.Mode == domain.RetrievalWorking && r.Scope.TaskID != "" && c.Scope.ProjectID == r.Scope.ProjectID && c.Scope.TaskID == r.Scope.TaskID && (r.Scope.SessionID == "" || c.Scope.SessionID == r.Scope.SessionID)
	}
	if c.Status != domain.ClaimVerified && c.Status != domain.ClaimDisputed {
		return false
	}
	return c.Scope.TaskID == "" && (c.Scope.ProjectID == r.Scope.ProjectID || c.Scope.ProjectID == "")
}
func applicable(a, b domain.Applicability) bool {
	now := time.Now().UTC()
	if a.ValidFrom != nil && now.Before(*a.ValidFrom) || a.ValidUntil != nil && !now.Before(*a.ValidUntil) {
		return false
	}
	if a.RepositoryRevision != "" && b.RepositoryRevision != "" && a.RepositoryRevision != b.RepositoryRevision {
		return false
	}
	if a.DirtyDigest != "" && b.DirtyDigest != "" && a.DirtyDigest != b.DirtyDigest {
		return false
	}
	if a.Environment != "" && b.Environment != "" && a.Environment != b.Environment {
		return false
	}
	for k, v := range a.Versions {
		if b.Versions[k] != "" && b.Versions[k] != v {
			return false
		}
	}
	return true
}
func applicabilityRank(a, b domain.Applicability) int {
	n := 0
	if a.RepositoryRevision != "" && a.RepositoryRevision == b.RepositoryRevision {
		n++
	}
	if a.Environment != "" && a.Environment == b.Environment {
		n++
	}
	for k, v := range a.Versions {
		if b.Versions[k] == v {
			n++
		}
	}
	return n
}

// Fetch exposes only project-wide evidence supporting a currently eligible
// publication. Task evidence is available only as citations in scoped working
// retrieval; the ID-only endpoint cannot supply task ownership context.
func (s *RetrievalService) Fetch(ctx context.Context, p domain.Principal, id, locator string, budget int) (domain.RetrievalResult, error) {
	if domain.ValidateIDs(id, locator) != nil || budget < 1 || budget > domain.EvidenceBudget {
		return domain.RetrievalResult{}, domain.ErrInvalidInput
	}
	if !slices.Contains(p.Capabilities, "evidence:read") {
		return domain.RetrievalResult{}, domain.ErrForbidden
	}
	if s.evidence == nil || s.objects == nil {
		return domain.RetrievalResult{}, domain.ErrUnavailable
	}
	var artifact domain.Artifact
	var sourceClient string
	var before RetrievalSnapshot
	load := func(ctx context.Context, tx Tx) error {
		a, client, e := s.evidence.Artifact(ctx, tx, id)
		if e != nil {
			return domain.ErrForbidden
		}
		if a.Scope.TaskID != "" || a.Scope.SessionID != "" || (Authorize(p, "retrieval:read", []string{a.Scope.ProjectID}) != nil || Authorize(p, "evidence:read", []string{a.Scope.ProjectID}) != nil) {
			return domain.ErrForbidden
		}
		snap, e := s.repo.Snapshot(ctx, tx, p, a.Scope)
		if e != nil {
			return e
		}
		if Authorize(snap.Principal, "retrieval:read", []string{a.Scope.ProjectID}) != nil || Authorize(snap.Principal, "evidence:read", []string{a.Scope.ProjectID}) != nil {
			return domain.ErrForbidden
		}
		if a.State != domain.ArtifactReady || !a.ObjectAvailable {
			return domain.ErrUnavailable
		}
		if locator != a.SourceLocator {
			return domain.ErrForbidden
		}
		policy, e := s.evidence.Policy(ctx, tx, a.Scope.ProjectID)
		if e != nil {
			return e
		}
		if policy.Paused || policy.Revision == "" || policy.Revision != a.PolicyRevision || validateSource(policy, a.SourcePath) != nil {
			return domain.ErrForbidden
		}
		candidates, e := s.repo.Seeds(ctx, tx, a.Scope, "@artifact:"+id, 50)
		if e != nil {
			return e
		}
		ids := make([]string, 0, len(candidates))
		for _, c := range candidates {
			if c.Scope.TaskID == "" {
				ids = append(ids, c.Revision)
			}
		}
		eligible, e := s.repo.Eligible(ctx, tx, p, a.Scope, ids)
		if e != nil {
			return e
		}
		allowed := false
		for _, c := range eligible.Claims {
			if c.Status == domain.ClaimVerified || c.Status == domain.ClaimDisputed {
				for _, ref := range c.Evidence {
					if ref.ArtifactID == id && ref.Digest == a.Digest && ref.Locator == locator {
						allowed = true
					}
				}
			}
		}
		if !allowed {
			return domain.ErrForbidden
		}
		artifact = a
		sourceClient = client
		before = snap
		return nil
	}
	if e := s.tx.Within(ctx, load); e != nil {
		return domain.RetrievalResult{}, e
	}
	failSource := func() (domain.RetrievalResult, error) {
		err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
			return s.evidence.Availability(ctx, tx, artifact.ID, artifact.ObjectKey, false)
		})
		if err != nil {
			return domain.RetrievalResult{}, err
		}
		return domain.RetrievalResult{}, domain.ErrUnavailable
	}
	if artifact.Bytes > int64(budget) {
		return domain.RetrievalResult{}, domain.ErrBudget
	}
	reader, e := s.objects.Open(ctx, artifact.ObjectKey)
	if e != nil {
		if errors.Is(e, domain.ErrIncomplete) {
			return failSource()
		}
		return domain.RetrievalResult{}, domain.ErrUnavailable
	}
	data, e := io.ReadAll(io.LimitReader(reader, MaxArtifactBytes+1))
	closeErr := reader.Close()
	if e != nil {
		return domain.RetrievalResult{}, domain.ErrUnavailable
	}
	if int64(len(data)) != artifact.Bytes || TokenDigest(string(data)) != artifact.Digest {
		return failSource()
	}
	if closeErr != nil {
		return domain.RetrievalResult{}, domain.ErrUnavailable
	}
	if !utf8.Valid(data) {
		return domain.RetrievalResult{}, domain.ErrIncomplete
	}
	previous := artifact
	initial := before
	if e = s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		if e := load(ctx, tx); e != nil {
			return e
		}
		if !sameEpoch(initial, before) || artifact.ObjectKey != previous.ObjectKey {
			return domain.ErrStale
		}
		return nil
	}); e != nil {
		return domain.RetrievalResult{}, e
	}
	result := domain.RetrievalResult{SchemaVersion: "3", Chunks: []domain.EvidenceChunk{{RecordID: id, Revision: artifact.Digest, Text: string(data), ClaimStatus: domain.ClaimCandidate, OriginTrust: domain.TrustSubmitted, Evidence: []domain.EvidenceRef{{ArtifactID: id, Digest: artifact.Digest, Locator: locator, SourceRevision: artifact.Generation, Origin: domain.Origin{ClientID: sourceClient, Trust: domain.TrustSubmitted}}}, Warnings: []string{"source_evidence_is_not_a_verified_claim"}}}}
	wire, e := domain.RetrievalWire(result)
	if e != nil || len(wire)+1 > budget {
		return domain.RetrievalResult{}, domain.ErrBudget
	}
	return result, nil
}

// These are the verified target's public conditions, never hidden support/source
// metadata. A zero/absent constraint stays absent instead of an invented value.
func targetApplicability(a domain.Applicability) *domain.Applicability {
	if a.RepositoryRevision == "" && a.DirtyDigest == "" && a.Environment == "" && a.Versions == nil && a.ValidFrom == nil && a.ValidUntil == nil {
		return nil
	}
	return &a
}
