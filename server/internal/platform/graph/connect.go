// Package graph contains the Neo4j adapter; driver types must stay in this layer
// and repositories. Services use the opaque domain.Tx handle.
package graph

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/migrations"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j/config"
)

type Config struct {
	URI      string `json:"-"`
	Database string `json:"-"`
	User     string `json:"-"`
	Secret   string `json:"-"`
}

func (Config) String() string   { return "graph.Config([redacted])" }
func (Config) GoString() string { return "graph.Config([redacted])" }

type Store struct {
	driver   neo4j.Driver
	database string
}

func Open(ctx context.Context, cfg Config) (*Store, error) {
	parsed, err := url.Parse(cfg.URI)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || cfg.Database == "" || cfg.User == "" || cfg.Secret == "" {
		return nil, domain.ErrInvalidInput
	}
	switch parsed.Scheme {
	case "bolt", "bolt+s", "bolt+ssc", "neo4j", "neo4j+s", "neo4j+ssc":
	default:
		return nil, domain.ErrInvalidInput
	}
	driver, err := neo4j.NewDriver(cfg.URI, neo4j.BasicAuth(cfg.User, cfg.Secret, ""), func(c *config.Config) {
		c.ConnectionAcquisitionTimeout = 5 * time.Second
		c.SocketConnectTimeout = 5 * time.Second
	})
	if err != nil {
		return nil, domain.ErrUnavailable
	}
	if err = driver.VerifyConnectivity(ctx); err != nil {
		_ = driver.Close(ctx)
		return nil, domain.ErrUnavailable
	}
	store := &Store{driver: driver, database: cfg.Database}
	if err := store.migrate(ctx); err != nil {
		_ = driver.Close(ctx)
		return nil, err
	}
	return store, nil
}

func (s *Store) Close(ctx context.Context) error {
	if err := s.driver.Close(ctx); err != nil {
		return domain.ErrUnavailable
	}
	return nil
}

func (s *Store) migrate(ctx context.Context) error {
	for _, statement := range strings.Split(migrations.Identity+"\n"+migrations.Clients+"\n"+migrations.Jobs+"\n"+migrations.Evidence, ";") {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		_, err := neo4j.ExecuteQuery(ctx, s.driver, statement, nil, neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(s.database))
		if err != nil {
			return domain.ErrUnavailable
		}
	}
	return nil
}
