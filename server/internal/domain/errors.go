package domain

import "errors"

var (
	ErrUnauthorized = errors.New("unauthorized")
	ErrForbidden    = errors.New("forbidden")
	ErrConflict     = errors.New("conflict")
	ErrStale        = errors.New("stale")
	ErrIncomplete   = errors.New("incomplete")
	ErrBudget       = errors.New("budget exceeded")
	ErrUnavailable  = errors.New("unavailable")
	ErrDeleted      = errors.New("deleted")
	ErrInvalidInput = errors.New("invalid input")
	// ErrUnknownOutcome reports a write whose request may have committed but
	// whose receipt was lost or invalid. Callers reconcile; they never retry blind.
	ErrUnknownOutcome = errors.New("unknown outcome")
)
