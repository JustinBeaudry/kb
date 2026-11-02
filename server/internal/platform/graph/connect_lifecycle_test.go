package graph

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConnectDoesNotInitializeSchema(t *testing.T) {
	existing, container := graphFixture(t)
	_, err := query(t.Context(), existing, "DROP CONSTRAINT claim_id", nil)
	require.NoError(t, err)
	host, err := container.Host(t.Context())
	require.NoError(t, err)
	port, err := container.MappedPort(t.Context(), "7687/tcp")
	require.NoError(t, err)
	connected, err := Connect(t.Context(), Config{URI: "bolt://" + host + ":" + port.Port(), Database: "neo4j", User: "neo4j", Secret: fixtureSecret})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connected.Close(context.Background())) })
	var spans []string
	connected.SetTrace(func(ctx context.Context, operation string, fn func(context.Context) error) error {
		spans = append(spans, operation)
		return fn(ctx)
	})
	rows, err := query(t.Context(), existing, "SHOW CONSTRAINTS YIELD name WHERE name='claim_id' RETURN name", nil)
	require.NoError(t, err)
	assert.Empty(t, rows, "ordinary connection must not migrate")
	require.NoError(t, connected.InitializeSchema(t.Context()))
	rows, err = query(t.Context(), existing, "SHOW CONSTRAINTS YIELD name WHERE name='claim_id' RETURN name", nil)
	require.NoError(t, err)
	assert.Len(t, rows, 1)
	require.NoError(t, connected.Probe(t.Context()))
	assert.Contains(t, spans, "graph_connect")
}
