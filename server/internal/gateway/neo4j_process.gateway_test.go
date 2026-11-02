package gateway

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type processMemory struct{ state domain.BackupProcessState }

func (p *processMemory) LoadProcess(context.Context) (domain.BackupProcessState, error) {
	return p.state, nil
}
func (p *processMemory) SaveProcess(_ context.Context, s domain.BackupProcessState) error {
	p.state = s
	return nil
}
func TestNeo4jSupervisorOwnsChildAndBothOfflineArchives(t *testing.T) {
	root, e := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, e)
	require.NoError(t, os.Chmod(root, 0700))
	bin := filepath.Join(root, "fixture-tool")
	script := `#!/bin/sh
if [ "$1" = "console" ]; then exec sleep 300; fi
if [ "$1" = "-cp" ]; then cat >/dev/null; exit 0; fi
if [ "$1" = "database" ] && [ "$2" = "dump" ]; then
  out="${4#--to-path=}"; printf 'fixture dump %s' "$3" > "$out/$3.dump"; exit 0
fi
if [ "$1" = "database" ] && [ "$2" = "load" ]; then exit 0; fi
exit 1
`
	require.NoError(t, os.WriteFile(bin, []byte(script), 0700))
	state := &processMemory{}
	g, e := NewNeo4jProcess(Neo4jProcessConfig{Root: filepath.Join(root, "graph"), Home: root, Neo4jBinary: bin, AdminBinary: bin, JavaBinary: bin, AuthClassPath: root, GraphSecret: "fixture-only-password"}, state, t.Context())
	require.NoError(t, e)
	require.NoError(t, g.Start(t.Context()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = g.Stop(ctx)
		_ = g.WaitStopped(ctx)
	})
	_, _, e = g.Dump(t.Context(), filepath.Join(root, "backup"))
	require.Error(t, e, "dump while child runs must refuse")
	require.NoError(t, g.Stop(t.Context()))
	require.NoError(t, g.WaitStopped(t.Context()))
	data, system, e := g.Dump(t.Context(), filepath.Join(root, "backup"))
	require.NoError(t, e)
	assert.FileExists(t, data)
	assert.FileExists(t, system)
	oldRoot := state.state.Root
	require.NoError(t, g.Load(t.Context(), data, system))
	require.NoError(t, g.Load(t.Context(), data, system), "exact completed load retry must preserve its generation")
	assert.NotEqual(t, oldRoot, state.state.Root)
	assert.DirExists(t, oldRoot)
	require.NoError(t, g.Start(t.Context()))
	require.NoError(t, g.Stop(t.Context()))
	require.NoError(t, g.WaitStopped(t.Context()))
}
