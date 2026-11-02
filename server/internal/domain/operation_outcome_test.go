package domain_test

import (
	"context"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestOperationOutcomeIsStickyAcrossNestedCancellationContexts(t *testing.T) {
	assert.False(t, domain.OperationSettled(t.Context()))
	ctx := domain.WithOperationOutcome(t.Context())
	assert.True(t, domain.OperationSettled(ctx))
	nested, cancel := context.WithCancel(domain.WithOperationOutcome(ctx))
	cancel()
	domain.MarkEffectUncertain(context.WithoutCancel(nested))
	assert.False(t, domain.OperationSettled(ctx))
}
