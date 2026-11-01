package service

import (
	"context"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"os"
)

type HostAdminAdmission interface {
	Admit(context.Context, string, string) (domain.OperationPermit, error)
	AdmitRecovery(context.Context, domain.MaintenanceFence, string, string) (domain.OperationPermit, error)
	Finish(context.Context, domain.OperationPermit) error
	State(context.Context) (domain.MaintenanceState, error)
}

// CredentialSink is borrowed for Execute; transport owns and closes received descriptors.
type HostAdminFiles = domain.HostAdminFiles
type HostAdminExecutor interface {
	Execute(context.Context, domain.HostAdminRequest, domain.OperationPermit, HostAdminFiles) (domain.HostAdminResult, error)
}
type HostAdminRunner interface {
	Run(context.Context, domain.HostAdminRequest, *os.File) (domain.HostAdminResult, error)
}
type HostAdminService struct {
	admission HostAdminAdmission
	executor  HostAdminExecutor
	instance  string
}

func NewHostAdminService(admission HostAdminAdmission, executor HostAdminExecutor, instance string) *HostAdminService {
	return &HostAdminService{admission: admission, executor: executor, instance: instance}
}
func (s *HostAdminService) Run(ctx context.Context, r domain.HostAdminRequest, sink *os.File) (domain.HostAdminResult, error) {
	result := domain.HostAdminResult{RequestID: r.RequestID, Code: "maintenance", Stage: "admission"}
	if r.Version != 1 || !validAdmissionName(r.RequestID) || !hostAdminAction(r.Action) || len(r.InputJSON) > 1<<20 {
		return result, domain.ErrInvalidInput
	}
	if s.admission == nil || s.executor == nil {
		return result, domain.ErrUnavailable
	}
	state, err := s.admission.State(ctx)
	if err != nil {
		return result, err
	}
	kind := "host-admin:" + r.Action
	var p domain.OperationPermit
	if state.Phase == "bootstrap" || state.Phase == "recovering" {
		p, err = s.admission.AdmitRecovery(ctx, state.Fence, s.instance, kind)
	} else {
		host, ok := s.admission.(interface {
			AdmitHost(context.Context, string, string) (domain.OperationPermit, error)
		})
		if !ok {
			return result, domain.ErrForbidden
		}
		p, err = host.AdmitHost(ctx, s.instance, kind)
	}
	if err != nil {
		return result, err
	}
	// A disconnected requester never cancels an already admitted child. The fixed
	// executor owns its bounded lifetime and explicit effect-completion handshake.
	workctx := context.WithoutCancel(ctx)
	result, err = s.executor.Execute(workctx, r, p, HostAdminFiles{CredentialSink: sink})
	result.RequestID = r.RequestID
	result.OperationID = p.ID
	if !result.Completed {
		if marker, ok := s.admission.(interface {
			MarkUnknown(context.Context, domain.OperationPermit) error
		}); ok {
			_ = marker.MarkUnknown(workctx, p)
		}
		result.Code = "unknown_outcome"
		result.Stage = "reconcile_operation"
		return result, domain.ErrUnknownOutcome
	}
	if finishErr := s.admission.Finish(workctx, p); finishErr != nil {
		result.Code = "unknown_outcome"
		result.Stage = "finish"
		return result, domain.ErrUnknownOutcome
	}
	return result, err
}
func hostAdminAction(action string) bool {
	switch action {
	case "setup", "enroll", "revoke", "list", "verify", "reject", "invalidate", "delete", "deletion-status", "policy", "policy-update", "journal-init", "journal-check":
		return true
	}
	return false
}
