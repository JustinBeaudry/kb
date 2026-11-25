package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestModelRouteCostNeverUnderstates(t *testing.T) {
	for _, tc := range []struct {
		name          string
		route         ModelRoute
		in, out       int64
		cost          int64
		valid, priced bool
	}{
		{"rounds partial micro-dollars up", ModelRoute{Name: "r", Model: "m", MaxOutputTokens: 10, InputMicroUSDPerMTok: 1, OutputMicroUSDPerMTok: 1}, 1, 0, 1, true, true},
		{"exact amount", ModelRoute{Name: "r", Model: "m", MaxOutputTokens: 10, InputMicroUSDPerMTok: 3_000_000, OutputMicroUSDPerMTok: 15_000_000}, 1200, 300, 8100, true, true},
		{"zero usage costs zero", ModelRoute{Name: "r", Model: "m", MaxOutputTokens: 10, InputMicroUSDPerMTok: 1, OutputMicroUSDPerMTok: 1}, 0, 0, 0, true, true},
		{"unpriced route", ModelRoute{Name: "r", Model: "m", MaxOutputTokens: 10}, 0, 0, 0, true, false},
		{"unbounded output is invalid", ModelRoute{Name: "r", Model: "m", MaxOutputTokens: MaxRouteOutputTokens + 1, InputMicroUSDPerMTok: 1, OutputMicroUSDPerMTok: 1}, 0, 0, 0, false, false},
		{"overflowing price is invalid", ModelRoute{Name: "r", Model: "m", MaxOutputTokens: 1, InputMicroUSDPerMTok: MaxPriceMicroUSDPerMTok + 1, OutputMicroUSDPerMTok: 1}, 0, 0, 0, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cost := tc.route.CostMicroUSD(tc.in, tc.out)

			assert.Equal(t, tc.cost, cost)
			assert.Equal(t, tc.valid, tc.route.Valid())
			assert.Equal(t, tc.priced, tc.route.Priced())
		})
	}
}

func TestWorstCaseCoversMaximumOutputAndInputBytes(t *testing.T) {
	route := ModelRoute{Name: "r", Model: "m", MaxOutputTokens: 1000, InputMicroUSDPerMTok: 1_000_000, OutputMicroUSDPerMTok: 5_000_000}
	request := GenerationRequest{Excerpts: []PromptExcerpt{{RevisionID: "ab", Text: "cdef"}}}

	worst := route.WorstCaseMicroUSD(request.InputBytes())

	assert.Equal(t, int64(6), request.InputBytes())
	assert.Equal(t, int64(5006), worst)
}

func TestReservationCommittedCountsUnknownAtWorstCase(t *testing.T) {
	for _, tc := range []struct {
		state ReservationState
		want  int64
	}{{ReservationReserved, 100}, {ReservationUnreconciled, 100}, {ReservationSettled, 7}} {
		t.Run(string(tc.state), func(t *testing.T) {
			r := Reservation{MaxCostMicroUSD: 100, State: tc.state, Usage: Usage{CostMicroUSD: 7, CostKnown: tc.state == ReservationSettled}}

			assert.Equal(t, tc.want, r.Committed())
		})
	}
}

func TestFindingDigestIsStableAcrossFormatting(t *testing.T) {
	a := FindingDigest("p", ProposalContradiction, "Ports  disagree.", []string{"b", "a", "a"})

	b := FindingDigest("p", ProposalContradiction, " ports disagree. ", []string{"a", "b"})

	assert.Equal(t, a, b)
	assert.NotEqual(t, a, FindingDigest("q", ProposalContradiction, "Ports disagree.", []string{"a", "b"}))
	assert.NotEqual(t, a, FindingDigest("p", ProposalConnection, "Ports disagree.", []string{"a", "b"}))
	assert.True(t, ProposalLesson.Valid())
	assert.False(t, ProposalKind("verified").Valid())
	assert.Equal(t, "2026-11-03", BudgetDay(time.Date(2026, 11, 2, 23, 0, 0, 0, time.FixedZone("x", -3600))))
}
