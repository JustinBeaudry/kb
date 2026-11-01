package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/platform"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/JustinBeaudry/kb/server/internal/repository"
	"github.com/JustinBeaudry/kb/server/internal/service"
)

// hostLifecycle accepts no caller-supplied principal or remote owner credential.
// Each implementation resolves the current persisted host owner itself.
type hostLifecycle interface {
	PreviewHost(context.Context, domain.DeleteSelection) (domain.DeletionPreview, error)
	DeleteHost(context.Context, domain.DeleteSelection) (domain.DeletionReceipt, error)
	DeletionStatusHost(context.Context, string) (domain.DeletionReceipt, error)
	PolicyHost(context.Context, string) (domain.Policy, error)
	UpdatePolicyHost(context.Context, string, string, domain.Policy) (domain.Policy, error)
	InitializeJournalHost(context.Context) (domain.JournalBarrier, error)
	CheckJournalHost(context.Context) (domain.JournalBarrier, error)
}

type hostRecovery interface {
	Recover(context.Context) error
}

func requiresRecovery(command string, preview bool) bool {
	switch command {
	case "setup", "journal-init", "journal-check", "deletion-status":
		return false
	case "delete":
		// Exact deletion execution is the host repair path for pending journal
		// delivery. Requiring readiness first would prevent the retry itself.
		return preview
	default:
		return true
	}
}

type lifecycleInput struct {
	selection domain.DeleteSelection
	status    struct{ ID string }
	project   struct{ ProjectID string }
	policy    struct {
		ProjectID, ExpectedRevision string
		Policy                      domain.Policy
	}
}

func lifecycleCommand(command string) bool {
	switch command {
	case "delete", "deletion-status", "policy", "policy-update", "journal-init", "journal-check":
		return true
	default:
		return false
	}
}

func (input *lifecycleInput) target(command string) any {
	switch command {
	case "delete":
		return &input.selection
	case "deletion-status":
		return &input.status
	case "policy":
		return &input.project
	default:
		return &input.policy
	}
}

func readAdminInput(path string, stdin io.Reader, target any) error {
	if path == "-" {
		return readAdminJSON(stdin, target)
	}
	file, err := os.Open(path)
	if err != nil {
		return domain.ErrInvalidInput
	}
	defer func() { _ = file.Close() }()
	return readAdminJSON(file, target)
}

func runLifecycle(ctx context.Context, command string, preview bool, input lifecycleInput, host hostLifecycle, output io.Writer) error {
	if host == nil {
		return domain.ErrUnavailable
	}
	var result any
	var err error
	switch command {
	case "delete":
		if preview {
			result, err = host.PreviewHost(ctx, input.selection)
		} else {
			result, err = host.DeleteHost(ctx, input.selection)
		}
	case "deletion-status":
		result, err = host.DeletionStatusHost(ctx, input.status.ID)
	case "policy":
		result, err = host.PolicyHost(ctx, input.project.ProjectID)
	case "policy-update":
		result, err = host.UpdatePolicyHost(ctx, input.policy.ProjectID, input.policy.ExpectedRevision, input.policy.Policy)
	case "journal-init":
		result, err = host.InitializeJournalHost(ctx)
	case "journal-check":
		result, err = host.CheckJournalHost(ctx)
	default:
		return domain.ErrInvalidInput
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(result)
}

func composeLifecycleAdministration(ctx context.Context, _ bool) (administration, func(), error) {
	cfg, err := platform.LoadConfig()
	if err != nil {
		return administration{}, nil, err
	}
	objectConfig, err := platform.LoadObjectConfig()
	if err != nil {
		return administration{}, nil, err
	}
	objects, err := gateway.NewS3(objectConfig)
	if err != nil {
		return administration{}, nil, err
	}
	store, err := graph.Connect(ctx, cfg.Graph)
	if err != nil {
		return administration{}, nil, err
	}
	closeStore := func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = store.Close(closeCtx)
	}
	lifecycle, err := composeLifecycleService(store, objects)
	if err != nil {
		closeStore()
		return administration{}, nil, err
	}
	return administration{lifecycle: lifecycle, recovery: lifecycle}, closeStore, nil
}

func composeLifecycleService(store *graph.Store, objects service.LifecycleObjects) (*service.LifecycleService, error) {
	journalConfig, err := platform.LoadJournalConfig()
	if err != nil {
		return nil, err
	}
	journal, err := gateway.NewS3DeletionJournal(journalConfig)
	if err != nil {
		return nil, err
	}
	return service.NewLifecycleService(store, repository.NewLifecycleRepository(store), repository.NewIdentityRepository(store), repository.NewJobsRepository(store), objects, journal, time.Now), nil
}
