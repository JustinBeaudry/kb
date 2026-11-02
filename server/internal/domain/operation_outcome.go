package domain

import (
	"context"
	"sync/atomic"
)

type operationOutcomeKey struct{}
type operationOutcome struct{ uncertain atomic.Bool }

// WithOperationOutcome begins effect accounting, or preserves the current
// operation when nested. It grants no admission authority. Detached cleanup
// contexts must retain this value, and all child work must join before checking.
func WithOperationOutcome(ctx context.Context) context.Context {
	if _, ok := ctx.Value(operationOutcomeKey{}).(*operationOutcome); ok {
		return ctx
	}
	return context.WithValue(ctx, operationOutcomeKey{}, &operationOutcome{})
}

// MarkEffectUncertain records a mutation whose final remote outcome is unknown.
// This is sticky: a retry, reconciliation read, or later business error cannot
// prove that an earlier physical attempt has stopped executing.
func MarkEffectUncertain(ctx context.Context) {
	if outcome, ok := ctx.Value(operationOutcomeKey{}).(*operationOutcome); ok {
		outcome.uncertain.Store(true)
	}
}

// OperationSettled is valid only after every local effect and cleanup joined.
// Missing accounting fails closed rather than providing completion evidence.
func OperationSettled(ctx context.Context) bool {
	outcome, ok := ctx.Value(operationOutcomeKey{}).(*operationOutcome)
	return ok && !outcome.uncertain.Load()
}
