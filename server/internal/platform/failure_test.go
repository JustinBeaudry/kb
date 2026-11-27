package platform

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAtStageKeepsErrorIdentityAndInnermostStage(t *testing.T) {
	assert.NoError(t, AtStage("graph_connect", nil))
	err := AtStage("worker_run", AtStage("graph_connect", fmt.Errorf("dial: %w", domain.ErrUnavailable)))
	assert.ErrorIs(t, err, domain.ErrUnavailable)
	assert.Equal(t, "graph_connect", FailureStage(err))
	assert.Equal(t, "", FailureStage(errors.New("untagged")))
	assert.Equal(t, "", FailureStage(AtStage("private prose", domain.ErrUnavailable)))
}

func TestErrorClassIsClosed(t *testing.T) {
	for err, want := range map[error]string{
		domain.ErrUnavailable: "unavailable", domain.ErrInvalidInput: "invalid_input",
		domain.ErrUnknownOutcome: "unknown_outcome", context.DeadlineExceeded: "deadline",
		context.Canceled: "canceled", errors.New("vendor secret text"): "other",
	} {
		assert.Equal(t, want, ErrorClass(err))
		assert.Contains(t, ErrorClasses, want)
	}
	assert.Equal(t, "", ErrorClass(nil))
}

func TestTransientOnlyForDependencyFailures(t *testing.T) {
	assert.True(t, Transient(AtStage("graph_connect", domain.ErrUnavailable)))
	assert.True(t, Transient(context.DeadlineExceeded))
	assert.False(t, Transient(domain.ErrInvalidInput))
	assert.False(t, Transient(domain.ErrForbidden))
}

func TestStageAndErrorClassReachLogsOnlyFromClosedSets(t *testing.T) {
	var out bytes.Buffer
	telemetry, err := NewTelemetry(&out, "worker")
	require.NoError(t, err)
	logger := telemetry.Logger()
	logger.Warn("worker_startup_retry", "stage", "graph_connect", "error_class", "unavailable", "attempt", 3, "retry_after_ms", 4000)
	logger.Warn("worker_startup_retry", "stage", "bolt://user:secret@host", "error_class", "dial tcp secret")
	require.NoError(t, telemetry.Shutdown(t.Context()))
	logs := out.String()
	assert.Contains(t, logs, `"stage":"graph_connect"`)
	assert.Contains(t, logs, `"error_class":"unavailable"`)
	assert.Contains(t, logs, `"attempt":3`)
	assert.Contains(t, logs, `"retry_after_ms":4000`)
	assert.NotContains(t, logs, "secret")
}
