package service

import (
	"context"
	"sync"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

// MemoryAPI is the remote memory API bound to exactly one per-client
// credential. Implementations hold no graph or object-store credentials and no
// method accepts a Principal: the authoritative server derives identity from the
// credential. Generated HTTP DTOs stay inside the implementing gateway.
type MemoryAPI interface {
	Retrieve(context.Context, domain.RetrievalRequest) (domain.RetrievalResult, error)
	Fetch(context.Context, domain.FetchRequest) (domain.RetrievalResult, error)
	Submit(context.Context, domain.CandidateInput) (domain.ClaimRevision, error)
	PutCheckpoint(context.Context, string, domain.Checkpoint) (domain.Checkpoint, error)
	Status(context.Context) (domain.Status, error)
}

// RemoteMemoryService serves stdio adapters (kb-mcp) from the authoritative
// server only. It validates requests with the server's own rules before any
// authenticated call, accepts only schema-3 results, never presents a fresh
// submission as verified, and reports every failure instead of substituting
// local or cached results.
type RemoteMemoryService struct {
	api MemoryAPI

	mu    sync.Mutex
	bound int // revision bound confirmed by server status; zero until confirmed
}

func NewRemoteMemoryService(api MemoryAPI) *RemoteMemoryService {
	return &RemoteMemoryService{api: api}
}

func (s *RemoteMemoryService) Retrieve(ctx context.Context, input domain.RetrievalRequest) (domain.RetrievalResult, error) {
	r, err := normalizedRequest(input)
	if err != nil {
		return domain.RetrievalResult{}, err
	}
	result, err := s.api.Retrieve(ctx, r)
	if err != nil {
		return domain.RetrievalResult{}, err
	}
	if result.SchemaVersion != "3" {
		return domain.RetrievalResult{}, domain.ErrUnavailable
	}
	return result, nil
}
func (s *RemoteMemoryService) Fetch(ctx context.Context, input domain.FetchRequest) (domain.RetrievalResult, error) {
	r, err := normalizedFetch(input)
	if err != nil {
		return domain.RetrievalResult{}, err
	}
	result, err := s.api.Fetch(ctx, r)
	if err != nil {
		return domain.RetrievalResult{}, err
	}
	if result.SchemaVersion != "3" {
		return domain.RetrievalResult{}, domain.ErrUnavailable
	}
	return result, nil
}
func (s *RemoteMemoryService) Submit(ctx context.Context, c domain.CandidateInput) (domain.ClaimRevision, error) {
	if domain.ValidateIDs(c.ClaimID, c.Text, c.Kind, c.Scope.ProjectID, c.Scope.TaskID) != nil {
		return domain.ClaimRevision{}, domain.ErrInvalidInput
	}
	claim, err := s.api.Submit(ctx, c)
	if err != nil {
		return domain.ClaimRevision{}, err
	}
	// The write was accepted, so an invalid receipt may hide a commit.
	if claim.Status != domain.ClaimCandidate || !s.withinBound(claim.Revision) {
		return domain.ClaimRevision{}, domain.ErrUnknownOutcome
	}
	return claim, nil
}
func (s *RemoteMemoryService) PutCheckpoint(ctx context.Context, expected string, c domain.Checkpoint) (domain.Checkpoint, error) {
	if domain.ValidateIDs(c.ID, c.Scope.ProjectID, c.Scope.TaskID) != nil {
		return domain.Checkpoint{}, domain.ErrInvalidInput
	}
	checkpoint, err := s.api.PutCheckpoint(ctx, expected, c)
	if err != nil {
		return domain.Checkpoint{}, err
	}
	if !s.withinBound(checkpoint.Revision) {
		return domain.Checkpoint{}, domain.ErrUnknownOutcome
	}
	return checkpoint, nil
}

// withinBound checks a receipt against the bound the server advertised, which
// is what MCP preflight reserved frame space for.
func (s *RemoteMemoryService) withinBound(revision string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return revision != "" && (s.bound == 0 || len(revision) <= s.bound) && len(revision) <= domain.MaxRevisionBytes
}
func (s *RemoteMemoryService) Status(ctx context.Context) (domain.Status, error) {
	return s.api.Status(ctx)
}

// MaxRevisionBytes is the revision bound the authoritative server advertises
// in authenticated status. It is zero, so callers attempt no write, when the
// server is unreachable, advertises nothing (an older server) or advertises
// more than this client's contract. Only a confirmed bound is cached.
func (s *RemoteMemoryService) MaxRevisionBytes(ctx context.Context) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bound > 0 {
		return s.bound
	}
	status, err := s.api.Status(ctx)
	if err != nil || status.MaxRevisionBytes <= 0 || status.MaxRevisionBytes > domain.MaxRevisionBytes {
		return 0
	}
	s.bound = status.MaxRevisionBytes
	return s.bound
}
