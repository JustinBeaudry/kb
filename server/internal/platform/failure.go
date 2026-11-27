package platform

import (
	"context"
	"errors"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

// FailureStages is the closed vocabulary for the "stage" log attribute. A stage
// names where a process failed without carrying any vendor error text.
var FailureStages = []string{
	"config", "objects", "journal", "maintenance", "health", "admission",
	"graph_connect", "lifecycle_recover", "lifecycle_expire", "capture_sweep",
	"finish_operation", "dreaming", "worker_run", "startup_deadline",
}

// ErrorClasses is the closed vocabulary for the "error_class" log attribute.
var ErrorClasses = []string{
	"unavailable", "invalid_input", "unauthorized", "forbidden", "conflict",
	"stale", "incomplete", "budget", "deleted", "unknown_outcome", "deadline",
	"canceled", "other",
}

// StageError records the stage at which an operation failed. It unwraps to the
// original error, so errors.Is keeps working for callers and tests.
type StageError struct {
	Stage string
	Err   error
}

func (e *StageError) Error() string { return e.Stage + ": " + e.Err.Error() }
func (e *StageError) Unwrap() error { return e.Err }

// AtStage tags a non-nil error with a stage. The first tag wins, so the stage
// nearest the failure is kept. Stages outside FailureStages are never logged.
func AtStage(stage string, err error) error {
	if err == nil {
		return nil
	}
	var existing *StageError
	if errors.As(err, &existing) {
		return err
	}
	return &StageError{Stage: stage, Err: err}
}

// FailureStage returns the innermost recorded stage, or "" when none is known.
func FailureStage(err error) string {
	var staged *StageError
	if errors.As(err, &staged) && oneOf(staged.Stage, FailureStages...) {
		return staged.Stage
	}
	return ""
}

// ErrorClass maps an error onto the closed ErrorClasses vocabulary.
func ErrorClass(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, domain.ErrUnknownOutcome):
		return "unknown_outcome"
	case errors.Is(err, domain.ErrUnavailable):
		return "unavailable"
	case errors.Is(err, domain.ErrInvalidInput):
		return "invalid_input"
	case errors.Is(err, domain.ErrUnauthorized):
		return "unauthorized"
	case errors.Is(err, domain.ErrForbidden):
		return "forbidden"
	case errors.Is(err, domain.ErrConflict):
		return "conflict"
	case errors.Is(err, domain.ErrStale):
		return "stale"
	case errors.Is(err, domain.ErrIncomplete):
		return "incomplete"
	case errors.Is(err, domain.ErrBudget):
		return "budget"
	case errors.Is(err, domain.ErrDeleted):
		return "deleted"
	}
	return "other"
}

// Transient reports whether a failure is worth retrying in-process: a
// dependency was unreachable or timed out. Configuration and policy failures
// are not transient and must fail closed.
func Transient(err error) bool {
	return errors.Is(err, domain.ErrUnavailable) || errors.Is(err, context.DeadlineExceeded)
}
