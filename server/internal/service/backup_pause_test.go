package service

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestPauseClosesAndDrainsWithoutStoppingGraph(t *testing.T) {
	f := &backupFixture{}
	s := fixtureBackupService(t, f)
	require.NoError(t, s.Pause(t.Context(), "restore-1"))
	assert.Equal(t, []string{"begin", "drain", "recovering", "persist:paused"}, f.calls)
	assert.False(t, f.resumed)
	assert.Equal(t, "pause", f.state.Kind)
}
