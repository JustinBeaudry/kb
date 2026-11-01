package platform

import (
	"fmt"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigRequiresTLSOutsideLoopback(t *testing.T) {
	t.Setenv("KB_INGRESS_TRANSPORT", "")
	t.Setenv("KB_GRAPH_URI", "bolt://fixture:7687")
	t.Setenv("KB_GRAPH_USER", "neo4j")
	t.Setenv("KB_GRAPH_SECRET", "fixture-secret")
	t.Setenv("KB_TLS_CERT", "")
	t.Setenv("KB_TLS_KEY", "")
	for _, tc := range []struct {
		address string
		valid   bool
	}{{"", true}, {"127.0.0.1:8080", true}, {"[::1]:8080", true}, {":8080", false}, {"0.0.0.0:8080", false}, {"localhost:8080", false}, {"bad-address", false}} {
		t.Run(tc.address, func(t *testing.T) {
			t.Setenv("KB_LISTEN", tc.address)
			cfg, err := LoadConfig()
			if tc.valid {
				require.NoError(t, err)
				assert.NotContains(t, fmt.Sprintf("%+v %#v", cfg, cfg), "fixture-secret")
			} else {
				assert.ErrorIs(t, err, domain.ErrInvalidInput)
			}
		})
	}
	t.Setenv("KB_LISTEN", ":8080")
	t.Setenv("KB_TLS_CERT", "fixture-cert")
	t.Setenv("KB_TLS_KEY", "fixture-key")
	_, err := LoadConfig()
	require.NoError(t, err)
	t.Setenv("KB_TLS_KEY", "")
	_, err = LoadConfig()
	assert.ErrorIs(t, err, domain.ErrInvalidInput)
}

func TestConfigRequiresExplicitRailwayEdgeTransport(t *testing.T) {
	t.Setenv("KB_GRAPH_URI", "bolt://fixture:7687")
	t.Setenv("KB_GRAPH_USER", "neo4j")
	t.Setenv("KB_GRAPH_SECRET", "fixture-secret")
	t.Setenv("KB_LISTEN", "[::]:8080")
	t.Setenv("KB_TLS_CERT", "")
	t.Setenv("KB_TLS_KEY", "")
	for _, tc := range []struct {
		mode  string
		valid bool
	}{
		{"", false}, {"direct", false}, {"railway-edge", true}, {"trust-proxy", false},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			t.Setenv("KB_INGRESS_TRANSPORT", tc.mode)
			_, err := LoadConfig()
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, domain.ErrInvalidInput)
			}
		})
	}
	t.Setenv("KB_INGRESS_TRANSPORT", "railway-edge")
	t.Setenv("KB_TLS_CERT", "cert")
	t.Setenv("KB_TLS_KEY", "key")
	_, err := LoadConfig()
	require.ErrorIs(t, err, domain.ErrInvalidInput)
}
