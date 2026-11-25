package handler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type decisionHandlerService struct {
	err  error
	seen *domain.DecisionReply
}

func (f decisionHandlerService) Respond(_ context.Context, r domain.DecisionReply) (domain.Decision, error) {
	*f.seen = r
	if f.err != nil {
		return domain.Decision{}, f.err
	}
	return domain.Decision{ID: r.DecisionID, State: domain.DecisionAccepted}, nil
}

func TestRespondDecisionHTTP(t *testing.T) {
	const body = `{"candidate_revision":"rev-1","evidence_digest":"d","action":"approve","event_id":"nonce","signer_id":"owner-key","signature":"c2ln"}`
	for _, tc := range []struct {
		name, body string
		err        error
		status     int
	}{
		{"accepted", body, nil, 200},
		{"unsigned or wrong owner", body, domain.ErrForbidden, 403},
		{"stale revision", body, domain.ErrStale, 409},
		{"unknown field rejected by contract", `{"candidate_revision":"r","evidence_digest":"d","action":"approve","event_id":"e","signer_id":"s","signature":"c2ln","grant":"x"}`, nil, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var seen domain.DecisionReply
			e, err := NewServer(captureIdentity{}, nil, nil, nil, nil, nil, nil, nil, slog.New(slog.NewJSONHandler(io.Discard, nil)), WithDecisions(decisionHandlerService{err: tc.err, seen: &seen}))
			require.NoError(t, err)
			req := httptest.NewRequest("POST", "/v1/decisions/dec-1:respond", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer fixture")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			e.ServeHTTP(rec, req)

			assert.Equal(t, tc.status, rec.Code, rec.Body.String())
			if tc.status != 200 {
				return
			}
			var out map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
			assert.Equal(t, map[string]any{"id": "dec-1", "state": "accepted", "duplicate": false}, out["receipt"])
			assert.Equal(t, "dec-1", seen.DecisionID)
			assert.Equal(t, []byte("sig"), seen.Signature)
		})
	}
	t.Run("route absent without decision service", func(t *testing.T) {
		e, err := NewServer(captureIdentity{}, nil, nil, nil, nil, nil, nil, nil, slog.New(slog.NewJSONHandler(io.Discard, nil)))
		require.NoError(t, err)
		req := httptest.NewRequest("POST", "/v1/decisions/dec-1:respond", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer fixture")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		e.ServeHTTP(rec, req)

		assert.Equal(t, 503, rec.Code)
	})
}
