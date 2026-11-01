package gateway

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway/memoryapi"
)

const memoryAPIResponseLimit = 256 << 10

var bearerPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// MemoryAPI is the remote memory server reached with exactly one per-client
// bearer credential. It never holds graph or object-store credentials, never
// follows redirects (the bearer stays pinned to one origin), reads bounded
// bodies, and normalizes generated DTOs into domain types. Remote problem
// messages are never relayed; only the closed problem code is read.
type MemoryAPI struct {
	client *memoryapi.HTTPClient
}

// NewMemoryAPI accepts an HTTPS origin, or HTTP on a numeric loopback address.
func NewMemoryAPI(origin, bearer string, timeout time.Duration) (*MemoryAPI, error) {
	u, err := url.Parse(origin)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Host == "" {
		return nil, domain.ErrInvalidInput
	}
	if u.Scheme != "https" && (u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "::1")) {
		return nil, domain.ErrInvalidInput
	}
	if !bearerPattern.MatchString(bearer) || timeout <= 0 {
		return nil, domain.ErrInvalidInput
	}
	// A redirect is returned unfollowed and fails closed: the bearer is never
	// presented to any other origin.
	httpClient := &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	client, err := memoryapi.NewClient(u.Scheme+"://"+u.Host, memoryapi.WithHTTPClient(httpClient), memoryapi.WithRequestEditorFn(func(_ context.Context, r *http.Request) error {
		r.Header.Set("Authorization", "Bearer "+bearer)
		r.Header.Set("X-Request-Id", rand.Text())
		return nil
	}))
	if err != nil {
		return nil, domain.ErrInvalidInput
	}
	return &MemoryAPI{client: client}, nil
}

// decode reads a bounded body and maps HTTP failures to closed domain errors.
func decode(response *http.Response, err error, dest any, successStatus, successLimit int) error {
	if err != nil || response == nil || response.Body == nil {
		return domain.ErrUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, memoryAPIResponseLimit+1))
	if err != nil || len(body) > memoryAPIResponseLimit {
		return domain.ErrUnavailable
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		if response.StatusCode != successStatus || (successLimit > 0 && len(body) > successLimit) || !validMemoryResponse(body, dest) || json.Unmarshal(body, dest) != nil {
			return domain.ErrUnavailable
		}
		return nil
	}
	var problem struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(body, &problem)
	switch {
	case response.StatusCode == 401:
		return domain.ErrUnauthorized
	case response.StatusCode == 403:
		return domain.ErrForbidden
	case response.StatusCode == 409 && problem.Code == "conflict":
		return domain.ErrConflict
	case response.StatusCode == 409:
		return domain.ErrStale
	case response.StatusCode == 410:
		return domain.ErrDeleted
	case response.StatusCode == 413 && problem.Code == "budget":
		return domain.ErrBudget
	case response.StatusCode == 400 || response.StatusCode == 413:
		return domain.ErrInvalidInput
	case response.StatusCode == 503 && problem.Code == "incomplete":
		return domain.ErrIncomplete
	}
	return domain.ErrUnavailable
}
func optional[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}
func scopeDTO(s domain.Scope) memoryapi.Scope {
	return memoryapi.Scope{ProjectId: s.ProjectID, TaskId: optional(s.TaskID), SessionId: optional(s.SessionID)}
}
func scopeOf(s memoryapi.Scope) domain.Scope {
	return domain.Scope{ProjectID: s.ProjectId, TaskID: value(s.TaskId), SessionID: value(s.SessionId)}
}
func value[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
func applicabilityDTO(a domain.Applicability) memoryapi.Applicability {
	out := memoryapi.Applicability{RepositoryRevision: optional(a.RepositoryRevision), DirtyDigest: optional(a.DirtyDigest), Environment: optional(a.Environment), ValidFrom: a.ValidFrom, ValidUntil: a.ValidUntil}
	if a.Versions != nil {
		out.Versions = &a.Versions
	}
	return out
}
func applicabilityOf(a memoryapi.Applicability) domain.Applicability {
	return domain.Applicability{RepositoryRevision: value(a.RepositoryRevision), DirtyDigest: value(a.DirtyDigest), Environment: value(a.Environment), Versions: value(a.Versions), ValidFrom: a.ValidFrom, ValidUntil: a.ValidUntil}
}
func refsOf(refs []memoryapi.EvidenceRef) []domain.EvidenceRef {
	out := make([]domain.EvidenceRef, 0, len(refs))
	for _, r := range refs {
		out = append(out, domain.EvidenceRef{ArtifactID: r.ArtifactId, Digest: r.Digest, Locator: r.Locator, SourceRevision: r.SourceRevision, Origin: domain.Origin{ClientID: r.Origin.ClientId, Trust: domain.OriginTrust(r.Origin.Trust), AttestationID: value(r.Origin.AttestationId)}, Applicability: applicabilityOf(r.Applicability)})
	}
	return out
}
func checkpointOf(c memoryapi.Checkpoint) domain.Checkpoint {
	return domain.Checkpoint{ID: c.Id, Revision: c.Revision, Scope: scopeOf(c.Scope), Objective: c.Objective, Completed: c.Completed, Pending: c.Pending, Questions: c.Questions, EvidenceRevisionIDs: c.EvidenceRevisionIds, UpdatedAt: c.UpdatedAt, EvidenceReferencesOmitted: value(c.EvidenceReferencesOmitted)}
}
func resultOf(r memoryapi.RetrievalResult) (domain.RetrievalResult, error) {
	if r.SchemaVersion != "3" || !validRetrievalResult(r) {
		return domain.RetrievalResult{}, domain.ErrUnavailable
	}
	out := domain.RetrievalResult{SchemaVersion: "3", Chunks: make([]domain.EvidenceChunk, 0, len(r.Chunks)), Truncated: r.Truncated, Continuation: value(r.Continuation), Visited: r.Visited, NoResults: r.NoResults, Action: value(r.Action), TaskStateOmitted: value(r.TaskStateOmitted)}
	if r.TruncationReason != nil {
		out.TruncationReason = string(*r.TruncationReason)
	}
	for _, c := range r.Chunks {
		chunk := domain.EvidenceChunk{RecordID: c.RecordId, Revision: c.Revision, Text: c.Text, ClaimStatus: domain.ClaimStatus(c.ClaimStatus), OriginTrust: domain.OriginTrust(c.OriginTrust), Evidence: refsOf(c.Evidence), Warnings: c.Warnings}
		if c.Applicability != nil {
			a := applicabilityOf(*c.Applicability)
			chunk.Applicability = &a
		}
		if c.VerificationOutcome != nil {
			v := domain.VerificationOutcome(*c.VerificationOutcome)
			chunk.VerificationOutcome = &v
		}
		if c.Passage != nil {
			chunk.Passage = &domain.Passage{StartByte: c.Passage.StartByte, EndByte: c.Passage.EndByte, SourceBytes: c.Passage.SourceBytes}
		}
		out.Chunks = append(out.Chunks, chunk)
	}
	if r.TaskCheckpoint != nil {
		out.TaskCheckpoint = &domain.TaskState{Label: string(r.TaskCheckpoint.Label), Checkpoint: checkpointOf(r.TaskCheckpoint.Checkpoint)}
	}
	return out, nil
}

// Retrieve uses the orientation route for orientation mode and the search
// route (16 KiB limit) for evidence and working modes.
func (g *MemoryAPI) Retrieve(ctx context.Context, r domain.RetrievalRequest) (domain.RetrievalResult, error) {
	mode := memoryapi.RetrievalMode(r.Mode)
	body := memoryapi.SearchInput{Scope: scopeDTO(r.Scope), Query: optional(r.Query), BudgetBytes: optional(r.BudgetBytes), MaxDepth: &r.MaxDepth, MaxVisited: optional(r.MaxVisited), Continuation: optional(r.Continuation), Mode: &mode}
	if len(r.EntityIDs) > 0 {
		body.EntityIds = &r.EntityIDs
	}
	if a := applicabilityDTO(r.Applicability); a != (memoryapi.Applicability{}) {
		body.Applicability = &a
	}
	var out memoryapi.RetrievalResult
	var response *http.Response
	var err error
	if r.Mode == domain.RetrievalOrientation {
		response, err = g.client.OrientMemory(ctx, memoryapi.OrientInput(body))
	} else {
		response, err = g.client.SearchMemory(ctx, body)
	}
	limit := domain.EvidenceBudget
	if r.Mode == domain.RetrievalOrientation {
		limit = domain.OrientationBudget
	}
	if r.BudgetBytes > 0 {
		limit = min(limit, r.BudgetBytes)
	}
	if err = decode(response, err, &out, http.StatusOK, limit); err != nil {
		return domain.RetrievalResult{}, err
	}
	if !validRetrievalForRequest(out, r) {
		return domain.RetrievalResult{}, domain.ErrUnavailable
	}
	return resultOf(out)
}
func (g *MemoryAPI) Fetch(ctx context.Context, r domain.FetchRequest) (domain.RetrievalResult, error) {
	mode := memoryapi.FetchMode(r.Mode)
	params := memoryapi.FetchEvidenceParams{Locator: &r.Locator, BudgetBytes: optional(r.BudgetBytes), Mode: &mode, StartByte: optional(r.StartByte), EndByte: r.EndByte, Continuation: optional(r.Continuation)}
	if r.Mode == domain.FetchWorking {
		params.ProjectId, params.TaskId, params.SessionId = optional(r.Scope.ProjectID), optional(r.Scope.TaskID), optional(r.Scope.SessionID)
	}
	var out memoryapi.RetrievalResult
	response, err := g.client.FetchEvidence(ctx, r.ArtifactID, &params)
	limit := domain.EvidenceBudget
	if r.BudgetBytes > 0 {
		limit = min(limit, r.BudgetBytes)
	}
	if err = decode(response, err, &out, http.StatusOK, limit); err != nil {
		return domain.RetrievalResult{}, err
	}
	if !validFetchResult(out, r) {
		return domain.RetrievalResult{}, domain.ErrUnavailable
	}
	return resultOf(out)
}
func (g *MemoryAPI) Submit(ctx context.Context, c domain.CandidateInput) (domain.ClaimRevision, error) {
	refs := make([]memoryapi.SubmittedEvidenceRef, 0, len(c.Evidence))
	for _, e := range c.Evidence {
		refs = append(refs, memoryapi.SubmittedEvidenceRef{ArtifactId: e.ArtifactID, Digest: e.Digest, Locator: e.Locator, SourceRevision: e.SourceRevision, Applicability: applicabilityDTO(e.Applicability)})
	}
	body := memoryapi.CandidateInput{ClaimId: c.ClaimID, ExpectedRevision: optional(c.ExpectedRevision), Text: c.Text, Kind: c.Kind, Scope: scopeDTO(c.Scope), Evidence: refs, DependsOn: c.DependsOn, Applicability: applicabilityDTO(c.Applicability)}
	if body.DependsOn == nil {
		body.DependsOn = []string{}
	}
	if len(c.EntityIDs) > 0 {
		body.EntityIds = &c.EntityIDs
	}
	var out memoryapi.CandidateResult
	response, err := g.client.SubmitCandidate(ctx, body)
	if err = writeOutcome(response, err, decode(response, err, &out, http.StatusCreated, 0)); err != nil {
		return domain.ClaimRevision{}, err
	}
	claim := out.Claim
	result := domain.ClaimRevision{ClaimID: claim.ClaimId, Revision: claim.Revision, Text: claim.Text, Kind: claim.Kind, Scope: scopeOf(claim.Scope), Evidence: refsOf(claim.Evidence), DependsOn: claim.DependsOn, Applicability: applicabilityOf(claim.Applicability), Status: domain.ClaimStatus(claim.ClaimStatus), EntityIDs: value(claim.EntityIds)}
	if out.SchemaVersion != "3" || claim.ClaimStatus != memoryapi.ClaimStatusCandidate || claim.VerificationOutcome != nil || claim.ClaimId != c.ClaimID || scopeOf(claim.Scope) != c.Scope || len(claim.Revision) > domain.MaxRevisionBytes {
		return domain.ClaimRevision{}, domain.ErrUnknownOutcome
	}
	return result, nil
}
func (g *MemoryAPI) PutCheckpoint(ctx context.Context, expected string, c domain.Checkpoint) (domain.Checkpoint, error) {
	body := memoryapi.CheckpointInput{ExpectedRevision: optional(expected), Scope: scopeDTO(c.Scope), Objective: c.Objective, Completed: nonEmpty(c.Completed), Pending: nonEmpty(c.Pending), Questions: nonEmpty(c.Questions), EvidenceRevisionIds: nonEmpty(c.EvidenceRevisionIDs)}
	var out memoryapi.CheckpointResult
	response, err := g.client.PutCheckpoint(ctx, c.ID, body)
	if err = writeOutcome(response, err, decode(response, err, &out, http.StatusOK, 0)); err != nil {
		return domain.Checkpoint{}, err
	}
	if out.SchemaVersion != "3" || out.Checkpoint.Id != c.ID || scopeOf(out.Checkpoint.Scope) != c.Scope || len(out.Checkpoint.Revision) > domain.MaxRevisionBytes {
		return domain.Checkpoint{}, domain.ErrUnknownOutcome
	}
	return checkpointOf(out.Checkpoint), nil
}

// writeOutcome reclassifies a write failure that may have committed. A failed
// dial (no request bytes sent) and any 4xx rejection are known non-commits, as
// is a closed 5xx code; transport loss after dialing, other 5xx and invalid
// success receipts are not.
func writeOutcome(response *http.Response, transport, decoded error) error {
	if decoded == nil {
		return nil
	}
	if transport != nil || response == nil {
		var op *net.OpError
		if errors.As(transport, &op) && op.Op == "dial" {
			return domain.ErrUnavailable
		}
		return domain.ErrUnknownOutcome
	}
	if response.StatusCode >= 400 && response.StatusCode < 500 || !errors.Is(decoded, domain.ErrUnavailable) {
		return decoded
	}
	return domain.ErrUnknownOutcome
}
func nonEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
func (g *MemoryAPI) Status(ctx context.Context) (domain.Status, error) {
	var out memoryapi.StatusResult
	response, err := g.client.GetStatus(ctx)
	if err = decode(response, err, &out, http.StatusOK, 0); err != nil {
		return domain.Status{}, err
	}
	if out.SchemaVersion != "3" {
		return domain.Status{}, domain.ErrUnavailable
	}
	status := domain.Status{ConnectionState: string(out.ConnectionState), AuthorizedProjectIDs: out.AuthorizedProjectIds, AuthorizedCapabilities: out.AuthorizedCapabilities, CapabilityGaps: out.CapabilityGaps}
	if out.MaxRevisionBytes != nil {
		// Any positive bound is a valid status; writes enforce the client limit.
		if *out.MaxRevisionBytes < 1 {
			return domain.Status{}, domain.ErrUnavailable
		}
		status.MaxRevisionBytes = *out.MaxRevisionBytes
	}
	return status, nil
}
