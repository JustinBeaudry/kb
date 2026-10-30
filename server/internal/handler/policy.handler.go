package handler

import (
	"context"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/labstack/echo/v4"
	"log/slog"
	"time"
)

type PolicyService interface {
	Get(context.Context, domain.Principal, string) (domain.Policy, error)
}

func NewPolicyServer(i IdentityService, c CaptureService, t CheckpointService, m MemoryService, r RetrievalService, p PolicyService, l *slog.Logger) (*echo.Echo, error) {
	return configuredMemoryServer(i, c, t, m, r, p, l)
}
func (h *IdentityHandler) GetPolicy(ctx context.Context, request GetPolicyRequestObject) (GetPolicyResponseObject, error) {
	p, err := h.policy.Get(ctx, principal(ctx), request.Project)
	if err != nil {
		return nil, err
	}
	return GetPolicy200JSONResponse{SchemaVersion: "3", Policy: Policy{Revision: p.Revision, Paused: p.Paused, ExcludedPaths: stringsAPI(p.ExcludedPaths), ExcludedFields: stringsAPI(p.ExcludedFields), AllowedModelRoutes: stringsAPI(p.AllowedModelRoutes), RawHistoryRetentionSeconds: int64(p.RawHistoryRetention / time.Second), InactiveTaskRetentionSeconds: int64(p.InactiveTaskRetention / time.Second), BackupRetentionSeconds: int64(p.BackupRetention / time.Second), SpoolLimitBytes: p.SpoolLimitBytes}}, nil
}
