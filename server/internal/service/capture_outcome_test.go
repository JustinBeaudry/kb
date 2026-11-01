package service

import (
	"bytes"
	"context"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"io"
	"testing"
)

type unknownCapturePut struct {
	ObjectStore
	after func()
}

func (o unknownCapturePut) Put(ctx context.Context, _ string, _ io.Reader, _ string) error {
	domain.MarkEffectUncertain(ctx)
	o.after()
	return domain.ErrUnavailable
}
func TestCaptureLaterBusinessErrorCannotSettleLostPut(t *testing.T) {
	for _, outcome := range []error{domain.ErrForbidden, domain.ErrStale, domain.ErrDeleted} {
		t.Run(outcome.Error(), func(t *testing.T) {
			s, r, o, p, m := captureFixture(t)
			ctx := domain.WithOperationOutcome(t.Context())
			s.objects = unknownCapturePut{ObjectStore: o, after: func() {
				switch outcome {
				case domain.ErrForbidden:
					r.policy.Paused = true
				case domain.ErrStale:
					r.policy.Revision = "new"
				case domain.ErrDeleted:
					for id, a := range r.artifacts {
						a.State = domain.ArtifactDeleted
						r.artifacts[id] = a
					}
				}
			}}
			_, err := s.Stage(ctx, p, m, bytes.NewBufferString("exit_code=0"))
			require.ErrorIs(t, err, outcome)
			assert.False(t, domain.OperationSettled(ctx))
		})
	}
}
