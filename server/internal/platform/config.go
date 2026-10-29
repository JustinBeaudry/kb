// Package platform provides composition-root configuration.
package platform

import (
	"net"
	"os"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
)

type Config struct {
	Graph                                  graph.Config
	Address, TLSCert, TLSKey               string
	ReadTimeout, WriteTimeout, IdleTimeout time.Duration
}

func (Config) String() string   { return "platform.Config([redacted])" }
func (Config) GoString() string { return "platform.Config([redacted])" }
func LoadConfig() (Config, error) {
	c := Config{Graph: graph.Config{URI: os.Getenv("KB_GRAPH_URI"), Database: os.Getenv("KB_GRAPH_DATABASE"), User: os.Getenv("KB_GRAPH_USER"), Secret: os.Getenv("KB_GRAPH_SECRET")}, Address: os.Getenv("KB_LISTEN"), TLSCert: os.Getenv("KB_TLS_CERT"), TLSKey: os.Getenv("KB_TLS_KEY"), ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	if c.Graph.Database == "" {
		c.Graph.Database = "neo4j"
	}
	if c.Address == "" {
		c.Address = "127.0.0.1:8080"
	}
	host, _, err := net.SplitHostPort(c.Address)
	if err != nil {
		return Config{}, domain.ErrInvalidInput
	}
	ip := net.ParseIP(host)
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return Config{}, domain.ErrInvalidInput
	}
	if c.TLSCert == "" && (ip == nil || !ip.IsLoopback()) {
		return Config{}, domain.ErrInvalidInput
	}
	if c.Graph.URI == "" || c.Graph.User == "" || c.Graph.Secret == "" {
		return Config{}, domain.ErrInvalidInput
	}
	return c, nil
}
