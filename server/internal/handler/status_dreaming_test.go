package handler

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type dreamingStatusService struct{}

func (dreamingStatusService) Status(context.Context, domain.Principal) (domain.Status, error) {
	return domain.Status{ConnectionState: "available", AuthorizedProjectIDs: []string{"demo"}, AuthorizedCapabilities: []string{"status:read"}, CapabilityGaps: []string{"recovery_unavailable"}, Dreaming: &domain.DreamingStatus{State: "pending", PendingJobs: 2, ReasonCodes: []string{"model_route_unavailable"}, DailyCapMicroUSD: 5000, CommittedMicroUSD: 1200, UnreconciledReservations: 1}}, nil
}

func TestStatusHTTPReportsDreamingState(t *testing.T) {
	server, err := NewServer(fixtureIdentity{}, nil, nil, nil, nil, nil, dreamingStatusService{}, nil, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	require.NoError(t, err)
	req := httptest.NewRequest("GET", "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer fixture-secret")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"schema_version":"3","connection_state":"available","authorized_project_ids":["demo"],"authorized_capabilities":["status:read"],"capability_gaps":["recovery_unavailable"],"dreaming":{"state":"pending","pending_jobs":2,"reason_codes":["model_route_unavailable"],"daily_cap_micro_usd":5000,"committed_micro_usd":1200,"unreconciled_reservations":1}}`, rec.Body.String())
}
