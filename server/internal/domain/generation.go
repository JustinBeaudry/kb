package domain

import (
	"errors"
	"time"
)

// Dreaming bounds are fixed policy, not tuning knobs: one invocation sends at
// most MaxExcerptBytes of filtered excerpt text plus fixed instructions, waits at
// most GenerationDeadline, and a job makes at most MaxGenerationAttempts calls.
const (
	GenerationDeadline    = 120 * time.Second
	MaxGenerationAttempts = 3
	MaxExcerptBytes       = 16384
	MaxProposals          = 20
	MaxProposalTextBytes  = 2000
	MaxRationaleBytes     = 1000
	MaxProposalSupport    = 20
	// MaxRouteOutputTokens bounds owner configuration, not the provider.
	MaxRouteOutputTokens = 64000
	// MaxPriceMicroUSDPerMTok rejects configuration that could overflow int64
	// cost arithmetic (16 KiB input plus 64k output tokens stays far below).
	MaxPriceMicroUSDPerMTok = 1_000_000_000_000
)

var (
	// ErrModelRouteUnavailable means policy and configuration authorize no
	// model route for this project. Jobs stay pending with a visible reason.
	ErrModelRouteUnavailable = errors.New("model route unavailable")
	// ErrModelPricingUnavailable means the authorized route has no configured
	// price, so worst-case spend cannot be reserved.
	ErrModelPricingUnavailable = errors.New("model pricing unavailable")
	// ErrModelBudgetUnconfigured means the owner has not set a daily cap. There
	// is no implicit spend permission.
	ErrModelBudgetUnconfigured = errors.New("model budget unconfigured")
)

// ModelRoute is owner configuration for one provider model. Zero prices mean
// pricing is unknown and the route cannot spend.
type ModelRoute struct {
	Name, Model                                 string
	MaxOutputTokens                             int64
	InputMicroUSDPerMTok, OutputMicroUSDPerMTok int64
}

func (r ModelRoute) Valid() bool {
	return ValidateIDs(r.Name, r.Model) == nil && r.MaxOutputTokens > 0 && r.MaxOutputTokens <= MaxRouteOutputTokens &&
		r.InputMicroUSDPerMTok >= 0 && r.InputMicroUSDPerMTok <= MaxPriceMicroUSDPerMTok &&
		r.OutputMicroUSDPerMTok >= 0 && r.OutputMicroUSDPerMTok <= MaxPriceMicroUSDPerMTok
}

func (r ModelRoute) Priced() bool {
	return r.Valid() && r.InputMicroUSDPerMTok > 0 && r.OutputMicroUSDPerMTok > 0
}

// CostMicroUSD rounds up so measured spend is never understated.
func (r ModelRoute) CostMicroUSD(inputTokens, outputTokens int64) int64 {
	return ceilDiv(inputTokens*r.InputMicroUSDPerMTok+outputTokens*r.OutputMicroUSDPerMTok, 1_000_000)
}

// WorstCaseMicroUSD bounds a call before it is made. Input tokens never exceed
// input bytes for byte-level BPE tokenizers, so bytes are a safe upper bound.
func (r ModelRoute) WorstCaseMicroUSD(inputBytes int64) int64 {
	return r.CostMicroUSD(inputBytes, r.MaxOutputTokens)
}

func ceilDiv(n, d int64) int64 {
	if n <= 0 {
		return 0
	}
	return (n + d - 1) / d
}

// PromptExcerpt is filtered, already-captured claim text. It is data for the
// model, never instructions.
type PromptExcerpt struct{ RevisionID, Text string }

// GenerationRequest is the provider-neutral request the LLM gateway accepts.
type GenerationRequest struct {
	Route                                 ModelRoute
	Schema, Instructions                  string
	Excerpts                              []PromptExcerpt
	IdempotencyKey, PolicyRevision, JobID string
	Deadline                              time.Time
}

// InputBytes counts what the request sends beyond fixed instructions.
func (g GenerationRequest) InputBytes() int64 {
	var n int64
	for _, e := range g.Excerpts {
		n += int64(len(e.RevisionID) + len(e.Text))
	}
	return n
}

type ProposalKind string

const (
	ProposalConnection    ProposalKind = "connection"
	ProposalContradiction ProposalKind = "contradiction"
	ProposalStale         ProposalKind = "stale"
	ProposalLesson        ProposalKind = "lesson"
)

func (k ProposalKind) Valid() bool {
	switch k {
	case ProposalConnection, ProposalContradiction, ProposalStale, ProposalLesson:
		return true
	}
	return false
}

// Proposal is model output after gateway schema validation. Status is always
// candidate; nothing a model says can change it.
type Proposal struct {
	Kind               ProposalKind
	Text, Rationale    string
	SupportRevisionIDs []string
	Applicability      Applicability
	Sensitive          bool
	Status             ClaimStatus
}

// Generation carries usage even when the call fails after reaching the
// provider, so callers can settle the reservation they made.
type Generation struct {
	Route, Model, StopReason string
	Proposals                []Proposal
	Rejected                 int
	Usage                    Usage
}

// GenerationRecord is the only instrumentation payload: no prompts, outputs or
// credentials.
type GenerationRecord struct {
	Route, Model, PolicyRevision, Outcome string
	InputTokens, OutputTokens             int64
	Usage                                 Usage
	Duration                              time.Duration
}

type ReservationState string

const (
	ReservationReserved     ReservationState = "reserved"
	ReservationSettled      ReservationState = "settled"
	ReservationUnreconciled ReservationState = "unreconciled"
)

// Reservation is one worst-case hold against the owner's daily cap. Reserved
// and unreconciled holds count their full MaxCostMicroUSD; settled holds count
// measured cost.
type Reservation struct {
	ID, Route, Day  string
	Scope           Scope
	MaxCostMicroUSD int64
	State           ReservationState
	Usage           Usage
	CreatedAt       time.Time
}

// Committed is the amount this reservation holds against its day.
func (r Reservation) Committed() int64 {
	if r.State == ReservationSettled {
		return r.Usage.CostMicroUSD
	}
	return r.MaxCostMicroUSD
}

// BudgetDay formats the UTC day a reservation belongs to.
func BudgetDay(t time.Time) string { return t.UTC().Format("2006-01-02") }
