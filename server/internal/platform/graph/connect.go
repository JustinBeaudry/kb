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
	trace    func(context.Context, string, func(context.Context) error) error
}

// SetTrace is composition-only, before publishing the store to callers. The
// hook sees closed operation codes, never queries, parameters or vendor errors.
func (s *Store) SetTrace(trace func(context.Context, string, func(context.Context) error) error) {
	s.trace = trace
}
func (s *Store) traced(ctx context.Context, name string, fn func(context.Context) error) error {
	if s.trace != nil {
		return s.trace(ctx, name, fn)
	}
	return fn(ctx)
}

func (s *Store) InitializeSchema(ctx context.Context) error { return s.migrate(ctx) }
func (s *Store) Probe(ctx context.Context) error {
	return s.traced(ctx, "graph_connect", func(ctx context.Context) error {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		session := s.driver.NewSession(probeCtx, neo4j.SessionConfig{DatabaseName: s.database, AccessMode: neo4j.AccessModeRead})
		defer func() {
			cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer stop()
			_ = session.Close(cleanup)
		}()
		result, err := session.Run(probeCtx, "RETURN 1", nil, neo4j.WithTxTimeout(5*time.Second))
		if err != nil {
			return domain.ErrUnavailable
		}
		if _, err = result.Consume(probeCtx); err != nil {
			return domain.ErrUnavailable
		}
		return nil
	})
}

// Connect verifies connectivity without changing schema. Production callers must
// hold a supervisor operation permit before opening a connection.
func Connect(ctx context.Context, cfg Config) (*Store, error) {
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
	return &Store{driver: driver, database: cfg.Database}, nil
}

// Open is the explicit connection-and-schema convenience for local fixtures.
// Production schema initialization belongs to the closed graph supervisor.
func Open(ctx context.Context, cfg Config) (*Store, error) {
	store, err := Connect(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := store.InitializeSchema(ctx); err != nil {
		_ = store.Close(ctx)
		return nil, err
	}
	return store, nil
}

func (s *Store) Close(ctx context.Context) error {
	return s.traced(ctx, "graph_close", func(ctx context.Context) error {
		if s.driver.Close(ctx) != nil {
			return domain.ErrUnavailable
		}
		return nil
	})
}

func (s *Store) migrate(ctx context.Context) error {
	for _, statement := range strings.Split(migrations.Identity+"\n"+migrations.Clients+"\n"+migrations.Jobs+"\n"+migrations.Evidence+"\n"+migrations.Memory+"\n"+migrations.Verification+"\n"+migrations.Lifecycle, ";") {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		err := s.schemaStatement(ctx, statement)
		if err != nil {
			return domain.ErrUnavailable
		}
	}
	return nil
}

// Explicit transactions avoid managed-driver retries hiding a lost commit ack.
func (s *Store) schemaStatement(ctx context.Context, statement string) error {
	session := s.driver.NewSession(ctx, neo4j.SessionConfig{DatabaseName: s.database})
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if session.Close(cleanup) != nil {
			domain.MarkEffectUncertain(ctx)
		}
	}()
	tx, err := session.BeginTransaction(ctx, neo4j.WithTxTimeout(15*time.Second))
	if err != nil {
		return domain.ErrUnavailable
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if tx.Close(cleanup) != nil {
			domain.MarkEffectUncertain(ctx)
		}
	}()
	result, err := tx.Run(ctx, statement, nil)
	if err != nil {
		return domain.ErrUnavailable
	}
	if _, err = result.Consume(ctx); err != nil {
		return domain.ErrUnavailable
	}
	if tx.Commit(ctx) != nil {
		domain.MarkEffectUncertain(ctx)
		return domain.ErrUnavailable
	}
	return nil
}
