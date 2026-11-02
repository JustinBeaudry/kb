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
	CitationSeeds(context.Context, Tx, domain.Principal, domain.Scope, domain.FetchMode, domain.EvidenceRef, int) ([]domain.ClaimRevision, bool, error)
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

func (s *RetrievalService) sign(c retrievalCursor, passage ...passageCursor) string {
	var data []byte
	if len(passage) == 1 {
		data, _ = json.Marshal(passage[0])
	} else {
		data, _ = json.Marshal(c)
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write(data)
	return base64.RawURLEncoding.EncodeToString(data) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (s *RetrievalService) cursor(raw string) (retrievalCursor, error) {
	var c retrievalCursor
	data, e := s.verified(raw)
	if e != nil || json.Unmarshal(data, &c) != nil || c.Offset < 0 || c.Offset > 50 {
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
		if r.Continuation == "" && r.Scope.TaskID != "" && (r.Mode == domain.RetrievalWorking || r.Mode == domain.RetrievalOrientation) && s.tasks != nil {
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
				result.Action = "Use the continuation with identical parameters; otherwise narrow the query or use a larger supported budget."
				result.Continuation = s.sign(retrievalCursor{Set: setDigest, Query: query, Auth: snap.AuthEpoch, Knowledge: snap.KnowledgeEpoch, Deletion: snap.DeletionEpoch, Head: snap.TaskHead, Offset: i})
				// Task state that fits alone is never lost to its own cursor.
				if wire, _ := domain.RetrievalWire(result); len(wire)+1 > r.BudgetBytes {
					result.Continuation = ""
					result.Action = "Narrow the query or use a larger supported budget to see claims beyond task state."
				}
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

// Fetch is the ID-only publication fetch: project-wide evidence supporting a
// currently eligible publication. Task evidence is never available here.
func (s *RetrievalService) Fetch(ctx context.Context, p domain.Principal, id, locator string, budget int) (domain.RetrievalResult, error) {
	return s.FetchEvidence(ctx, p, domain.FetchRequest{ArtifactID: id, Locator: locator, Mode: domain.FetchPublication, BudgetBytes: budget})
}

type passageCursor struct {
	Kind                      string `json:"k"`
	Query                     string `json:"q"`
	Generation, Digest        string
	Auth, Knowledge, Deletion uint64
	Head                      string `json:"h,omitempty"`
	Offset                    int64  `json:"o"`
	Start                     int64  `json:"b"`
}

// verified returns the payload of an HMAC-authenticated cursor.
func (s *RetrievalService) verified(raw string) ([]byte, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 2 || len(raw) > 2048 {
		return nil, domain.ErrStale
	}
	data, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil {
		return nil, domain.ErrStale
	}
	sig, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil {
		return nil, domain.ErrStale
	}
	m := hmac.New(sha256.New, s.key)
	m.Write(data)
	if !hmac.Equal(sig, m.Sum(nil)) {
		return nil, domain.ErrStale
	}
	return data, nil
}
func normalizedFetch(r domain.FetchRequest) (domain.FetchRequest, error) {
	if r.Mode == "" {
		r.Mode = domain.FetchPublication
	}
	invalid := domain.ValidateIDs(r.ArtifactID, r.Locator) != nil || r.BudgetBytes < 1 || r.BudgetBytes > domain.EvidenceBudget || r.StartByte < 0 || r.StartByte > MaxArtifactBytes || (r.EndByte != nil && (*r.EndByte < r.StartByte || *r.EndByte > MaxArtifactBytes)) || (r.Continuation != "" && r.StartByte != 0)
	switch r.Mode {
	case domain.FetchPublication:
		invalid = invalid || r.Scope != domain.Scope{}
	case domain.FetchWorking:
		invalid = invalid || domain.ValidateIDs(r.Scope.ProjectID, r.Scope.TaskID) != nil
	default:
		invalid = true
	}
	if invalid {
		return r, domain.ErrInvalidInput
	}
	return r, nil
}

type fetchAuthority struct {
	artifact        domain.Artifact
	client          string
	ref             domain.EvidenceRef
	citationBinding string
	snap            RetrievalSnapshot
}

// authorizeFetch performs every graph check for one fetch inside a service
// transaction. It never performs object I/O. ID-only publication fetch allows
// only project-scoped sources of eligible publications; working mode allows
// only task sources cited by an eligible candidate this client submitted in a
// task it currently owns. Artifact ownership alone grants nothing.
func (s *RetrievalService) authorizeFetch(ctx context.Context, tx Tx, p domain.Principal, r domain.FetchRequest) (fetchAuthority, error) {
	a, client, e := s.evidence.Artifact(ctx, tx, r.ArtifactID)
	if e != nil {
		return fetchAuthority{}, domain.ErrForbidden
	}
	scope := a.Scope
	if r.Mode == domain.FetchWorking {
		if a.Scope.TaskID == "" || a.Scope.ProjectID != r.Scope.ProjectID || a.Scope.TaskID != r.Scope.TaskID || (r.Scope.SessionID != "" && a.Scope.SessionID != r.Scope.SessionID) {
			return fetchAuthority{}, domain.ErrForbidden
		}
		scope = domain.Scope{ProjectID: r.Scope.ProjectID, TaskID: r.Scope.TaskID}
	} else if a.Scope.TaskID != "" || a.Scope.SessionID != "" {
		return fetchAuthority{}, domain.ErrForbidden
	}
	project := []string{a.Scope.ProjectID}
	if Authorize(p, "retrieval:read", project) != nil || Authorize(p, "evidence:read", project) != nil {
		return fetchAuthority{}, domain.ErrForbidden
	}
	snap, e := s.repo.Snapshot(ctx, tx, p, scope)
	if e != nil {
		return fetchAuthority{}, e
	}
	if Authorize(snap.Principal, "retrieval:read", project) != nil || Authorize(snap.Principal, "evidence:read", project) != nil {
		return fetchAuthority{}, domain.ErrForbidden
	}
	if r.Mode == domain.FetchWorking && snap.TaskOwner != p.ClientID {
		return fetchAuthority{}, domain.ErrForbidden
	}
	if a.State != domain.ArtifactReady || !a.ObjectAvailable || a.Generation == "" {
		return fetchAuthority{}, domain.ErrUnavailable
	}
	if r.Locator != a.SourceLocator {
		return fetchAuthority{}, domain.ErrForbidden
	}
	policy, e := s.evidence.Policy(ctx, tx, a.Scope.ProjectID)
	if e != nil {
		return fetchAuthority{}, e
	}
	if policy.Paused || policy.Revision == "" || policy.Revision != a.PolicyRevision || validateSource(policy, a.SourcePath) != nil {
		return fetchAuthority{}, domain.ErrForbidden
	}
	candidates, boundReached, e := s.repo.CitationSeeds(ctx, tx, p, scope, r.Mode, domain.EvidenceRef{ArtifactID: r.ArtifactID, Digest: a.Digest, Locator: r.Locator}, domain.MaxGraphVisited)
	if e != nil {
		return fetchAuthority{}, e
	}
	if len(candidates) > domain.MaxGraphVisited {
		return fetchAuthority{}, domain.ErrUnavailable
	}
	ids := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if (r.Mode == domain.FetchPublication && c.Scope.TaskID == "") || (r.Mode == domain.FetchWorking && c.Scope.TaskID == scope.TaskID) {
			ids = append(ids, c.Revision)
		}
	}
	eligible, e := s.repo.Eligible(ctx, tx, p, scope, ids)
	if e != nil {
		return fetchAuthority{}, e
	}
	// Keep the eligible reference, not just its opaque source revision. Multiple
	// claims or references can share a revision but carry different conditions.
	// Order by source revision, claim identity and reference content so graph
	// result ordering cannot silently switch the chosen authority between reads.
	var selected domain.EvidenceRef
	selectedClaim, selectedRefKey, binding := "", "", ""
	for _, c := range eligible.Claims {
		if !applicable(c.Applicability, domain.Applicability{}) {
			continue
		}
		allowed := r.Mode == domain.FetchPublication && c.Scope == a.Scope && (c.Status == domain.ClaimVerified || c.Status == domain.ClaimDisputed) ||
			r.Mode == domain.FetchWorking && c.Status == domain.ClaimCandidate && c.SubmittedBy == p.ClientID && c.Scope == a.Scope
		if !allowed {
			continue
		}
		for _, ref := range c.Evidence {
			if ref.ArtifactID != r.ArtifactID || ref.Digest != a.Digest || ref.Locator != r.Locator || domain.ValidateIDs(ref.SourceRevision) != nil || !applicable(ref.Applicability, domain.Applicability{}) {
				continue
			}
			refKey := captureID(ref)
			if binding != "" && (ref.SourceRevision > selected.SourceRevision || ref.SourceRevision == selected.SourceRevision && (c.Revision > selectedClaim || c.Revision == selectedClaim && refKey >= selectedRefKey)) {
				continue
			}
			selected, selectedClaim, selectedRefKey = ref, c.Revision, refKey
			// Hash now, rather than retaining mutable map/pointer aliases across I/O.
			binding = captureID(c.ClaimID, c.Revision, c.Applicability, ref)
		}
	}
	if binding == "" {
		if boundReached {
			return fetchAuthority{}, domain.ErrIncomplete
		}
		return fetchAuthority{}, domain.ErrForbidden
	}
	return fetchAuthority{artifact: a, client: client, ref: selected, citationBinding: binding, snap: snap}, nil
}

// FetchEvidence returns a bounded passage of one stored artifact. The immutable
// artifact digest and base locator are preserved; the passage reports the exact
// selected UTF-8 byte span. Authority, generation and epochs are checked before
// and after object I/O; continuations are HMAC-bound to all request parameters,
// the artifact generation and the epochs current when they were issued.
func (s *RetrievalService) FetchEvidence(ctx context.Context, p domain.Principal, input domain.FetchRequest) (domain.RetrievalResult, error) {
	r, e := normalizedFetch(input)
	if e != nil {
		return domain.RetrievalResult{}, e
	}
	if !slices.Contains(p.Capabilities, "evidence:read") {
		return domain.RetrievalResult{}, domain.ErrForbidden
	}
	if s.evidence == nil || s.objects == nil {
		return domain.RetrievalResult{}, domain.ErrUnavailable
	}
	requestedEnd := int64(-1)
	if r.EndByte != nil {
		requestedEnd = *r.EndByte
	}
	var cursor passageCursor
	start := r.StartByte
	if r.Continuation != "" {
		data, e := s.verified(r.Continuation)
		if e != nil || json.Unmarshal(data, &cursor) != nil || cursor.Kind != "passage" || cursor.Start < 0 || cursor.Offset <= cursor.Start || cursor.Offset > MaxArtifactBytes {
			return domain.RetrievalResult{}, domain.ErrStale
		}
		start = cursor.Start
	}
	query := captureID("passage", p.ClientID, r.ArtifactID, r.Locator, r.Mode, r.Scope, r.BudgetBytes, start, requestedEnd)
	if r.Continuation != "" {
		if r.EndByte != nil && cursor.Offset > *r.EndByte {
			return domain.RetrievalResult{}, domain.ErrStale
		}
		r.StartByte = cursor.Offset
	}
	empty := passageResult(domain.Artifact{ID: r.ArtifactID, SourceLocator: r.Locator, Digest: strings.Repeat("0", 64), Generation: "g"}, "", domain.EvidenceRef{SourceRevision: "g"}, r.Mode, domain.Passage{}, "", "")
	if wire, _ := domain.RetrievalWire(empty); len(wire)+1 > r.BudgetBytes {
		return domain.RetrievalResult{}, domain.ErrBudget
	}
	var before fetchAuthority
	if e = s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		v, e := s.authorizeFetch(ctx, tx, p, r)
		if e != nil {
			return e
		}
		if r.Continuation != "" && (cursor.Query != captureID(query, v.citationBinding) || cursor.Generation != v.artifact.Generation || cursor.Digest != v.artifact.Digest || cursor.Auth != v.snap.AuthEpoch || cursor.Knowledge != v.snap.KnowledgeEpoch || cursor.Deletion != v.snap.DeletionEpoch || cursor.Head != v.snap.TaskHead) {
			return domain.ErrStale
		}
		if r.StartByte > v.artifact.Bytes || (r.EndByte != nil && *r.EndByte > v.artifact.Bytes) {
			return domain.ErrInvalidInput
		}
		before = v
		return nil
	}); e != nil {
		return domain.RetrievalResult{}, e
	}
	artifact := before.artifact
	failSource := func() (domain.RetrievalResult, error) {
		err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
			return s.evidence.Availability(ctx, tx, artifact.ID, artifact.ObjectKey, false)
		})
		if err != nil {
			return domain.RetrievalResult{}, err
		}
		return domain.RetrievalResult{}, domain.ErrUnavailable
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
	// Integrity covers the whole immutable object even when one passage is returned.
	if int64(len(data)) != artifact.Bytes || TokenDigest(string(data)) != artifact.Digest {
		return failSource()
	}
	if closeErr != nil {
		return domain.RetrievalResult{}, domain.ErrUnavailable
	}
	if e = s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		after, e := s.authorizeFetch(ctx, tx, p, r)
		if e != nil {
			return e
		}
		if !sameEpoch(before.snap, after.snap) || after.citationBinding != before.citationBinding || after.artifact.ObjectKey != artifact.ObjectKey || after.artifact.Generation != artifact.Generation || after.artifact.Digest != artifact.Digest {
			return domain.ErrStale
		}
		return nil
	}); e != nil {
		return domain.RetrievalResult{}, e
	}
	end := int64(len(data))
	if r.EndByte != nil {
		end = *r.EndByte
	}
	spanStart, end := runeSpan(data, r.StartByte, end)
	if !utf8.Valid(data[spanStart:end]) {
		return domain.RetrievalResult{}, domain.ErrIncomplete
	}
	next := func(offset int64) string {
		return s.sign(retrievalCursor{}, passageCursor{Kind: "passage", Query: captureID(query, before.citationBinding), Generation: artifact.Generation, Digest: artifact.Digest, Auth: before.snap.AuthEpoch, Knowledge: before.snap.KnowledgeEpoch, Deletion: before.snap.DeletionEpoch, Head: before.snap.TaskHead, Offset: offset, Start: start})
	}
	build := func(cut int64) domain.RetrievalResult {
		continuation := ""
		if cut < end {
			continuation = next(cut)
		}
		return passageResult(artifact, before.client, before.ref, r.Mode, domain.Passage{StartByte: spanStart, EndByte: cut, SourceBytes: int64(len(data))}, string(data[spanStart:cut]), continuation)
	}
	fits := func(v domain.RetrievalResult) bool {
		wire, e := domain.RetrievalWire(v)
		return e == nil && len(wire)+1 <= r.BudgetBytes
	}
	if whole := build(end); fits(whole) {
		return whole, nil
	}
	// Largest rune-aligned cut that fits with its own signed continuation.
	best := spanStart
	lo, hi := spanStart+1, end-1
	for lo <= hi {
		mid := lo + (hi-lo)/2
		cut := mid
		for cut > spanStart && !utf8.RuneStart(data[cut]) {
			cut--
		}
		if cut > spanStart && fits(build(cut)) {
			best = max(best, cut)
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	if best == spanStart {
		return domain.RetrievalResult{}, domain.ErrBudget
	}
	return build(best), nil
}

// runeSpan narrows [start,end) to whole UTF-8 runes: a start inside a rune moves
// forward, an end inside a rune moves back, and an incomplete trailing rune at
// the object edge (F4 chunks split at byte boundaries) is excluded.
func runeSpan(data []byte, start, end int64) (int64, int64) {
	for start < end && !utf8.RuneStart(data[start]) {
		start++
	}
	if end < int64(len(data)) {
		for end > start && !utf8.RuneStart(data[end]) {
			end--
		}
	}
	last := end
	for last > start && end-last < utf8.UTFMax && !utf8.RuneStart(data[last-1]) {
		last--
	}
	if last > start && !utf8.FullRune(data[last-1:end]) {
		end = last - 1
	}
	return start, end
}

// passageResult reports the selected citation's source revision and
// conditions; the origin is always the artifact's submitting client.
func passageResult(a domain.Artifact, client string, ref domain.EvidenceRef, mode domain.FetchMode, span domain.Passage, text, continuation string) domain.RetrievalResult {
	warnings := []string{"source_evidence_is_not_a_verified_claim"}
	if mode == domain.FetchWorking {
		warnings = append(warnings, "unverified_task_evidence")
	}
	if span.StartByte != 0 || span.EndByte != span.SourceBytes {
		warnings = append(warnings, "partial_source_passage")
	}
	result := domain.RetrievalResult{SchemaVersion: "3", Chunks: []domain.EvidenceChunk{{RecordID: a.ID, Revision: a.Digest, Text: text, ClaimStatus: domain.ClaimCandidate, OriginTrust: domain.TrustSubmitted, Passage: &span, Evidence: []domain.EvidenceRef{{ArtifactID: a.ID, Digest: a.Digest, Locator: a.SourceLocator, SourceRevision: ref.SourceRevision, Applicability: ref.Applicability, Origin: domain.Origin{ClientID: client, Trust: domain.TrustSubmitted}}}, Warnings: warnings}}}
	if continuation != "" {
		result.Truncated = true
		result.TruncationReason = "wire_budget"
		result.Continuation = continuation
		result.Action = "Use the continuation, omit start_byte, and keep other parameters unchanged; or omit the continuation and request start_byte at this passage's end_byte."
	}
	return result
}

// These are the verified target's public conditions, never hidden support/source
// metadata. A zero/absent constraint stays absent instead of an invented value.
func targetApplicability(a domain.Applicability) *domain.Applicability {
	if a.RepositoryRevision == "" && a.DirtyDigest == "" && a.Environment == "" && a.Versions == nil && a.ValidFrom == nil && a.ValidUntil == nil {
		return nil
	}
	return &a
}
