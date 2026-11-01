package graph

import (
	"context"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
)

func TestConnectRejectsUnverifiedTLS(t *testing.T) {
	for _, scheme := range []string{"bolt+ssc", "neo4j+ssc"} {
		t.Run(scheme, func(t *testing.T) {
			// Cancellation makes a regression deterministic without a graph server:
			// reaching connectivity verification would return ErrUnavailable.
			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			store, err := Connect(ctx, Config{URI: scheme + "://127.0.0.1:7687", Database: "neo4j", User: "synthetic", Secret: "synthetic"})

			assert.Nil(t, store)
			assert.ErrorIs(t, err, domain.ErrInvalidInput)
		})
	}
}

func TestConnectRetainsSupportedSchemes(t *testing.T) {
	for _, scheme := range []string{"bolt", "neo4j", "bolt+s", "neo4j+s"} {
		t.Run(scheme, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			store, err := Connect(ctx, Config{URI: scheme + "://127.0.0.1:7687", Database: "neo4j", User: "synthetic", Secret: "synthetic"})

			assert.Nil(t, store)
			assert.ErrorIs(t, err, domain.ErrUnavailable, "supported schemes must pass configuration validation")
		})
	}
}
