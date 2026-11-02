package service_test

import (
	"context"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

type hostExecutor struct {
	completed bool
	err       error
}

func (e hostExecutor) Execute(_ context.Context, r domain.HostAdminRequest, _ domain.OperationPermit, _ service.HostAdminFiles) (domain.HostAdminResult, error) {
	return domain.HostAdminResult{RequestID: r.RequestID, Code: "ok", Completed: e.completed, ResultJSON: []byte(`{"ok":true}`)}, e.err
}
func TestHostAdminBootstrapIsSerialAndUnknownNeverDrains(t *testing.T) {
	ctx := context.Background()
	s, _ := newMaintenance(t)
	h := service.NewHostAdminService(s, hostExecutor{completed: true}, "host")
	_, err := h.Run(ctx, domain.HostAdminRequest{Version: 1, RequestID: "request", Action: "verify"}, nil)
	require.ErrorIs(t, err, domain.ErrForbidden)
	result, err := h.Run(ctx, domain.HostAdminRequest{Version: 1, RequestID: "request", Action: "journal-check"}, nil)
	require.NoError(t, err)
	assert.True(t, result.Completed)
	st, _ := s.State(ctx)
	assert.Zero(t, st.Active)
	h = service.NewHostAdminService(s, hostExecutor{completed: false, err: domain.ErrUnknownOutcome}, "host")
	_, err = h.Run(ctx, domain.HostAdminRequest{Version: 1, RequestID: "request", Action: "journal-check"}, nil)
	require.ErrorIs(t, err, domain.ErrUnknownOutcome)
	st, _ = s.State(ctx)
	assert.Equal(t, 1, st.Unknown)
}
