package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestServerInvalidConfigurationFailsSafely(t *testing.T) {
	t.Setenv("KB_GRAPH_URI", "")
	err := run(t.Context(), slog.New(slog.NewJSONHandler(io.Discard, nil)))
	assert.ErrorIs(t, err, domain.ErrInvalidInput)
}
func TestServerLoopbackAuthenticationAndGracefulShutdown(t *testing.T) {
	versions, err := os.ReadFile("../../../deploy/versions.json")
	require.NoError(t, err)
	var pin struct {
		Graph struct {
			Image string `json:"image"`
		} `json:"graph"`
	}
	require.NoError(t, json.Unmarshal(versions, &pin))
	const secret = "server-composition-fixture-only"
	container, err := testcontainers.GenericContainer(t.Context(), testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: pin.Graph.Image, ExposedPorts: []string{"7687/tcp"}, Env: map[string]string{"NEO4J_AUTH": "neo4j/" + secret, "NEO4J_server_memory_heap_initial__size": "256m", "NEO4J_server_memory_heap_max__size": "512m", "NEO4J_server_memory_pagecache_size": "128m"}, WaitingFor: wait.ForLog("Started.").WithStartupTimeout(2 * time.Minute)}, Started: true})
	if container != nil {
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			require.NoError(t, container.Terminate(ctx))
		})
	}
	require.NoError(t, err)
	host, err := container.Host(t.Context())
	require.NoError(t, err)
	port, err := container.MappedPort(t.Context(), "7687/tcp")
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	t.Setenv("KB_GRAPH_URI", "bolt://"+host+":"+port.Port())
	t.Setenv("KB_GRAPH_USER", "neo4j")
	t.Setenv("KB_GRAPH_SECRET", secret)
	t.Setenv("KB_GRAPH_DATABASE", "neo4j")
	t.Setenv("KB_LISTEN", address)
	t.Setenv("KB_TLS_CERT", "")
	t.Setenv("KB_TLS_KEY", "")
	t.Setenv("KB_RETRIEVAL_HMAC_KEY", strings.Repeat("a", 64))
	t.Setenv("KB_S3_ENDPOINT", "http://127.0.0.1:8333")
	t.Setenv("KB_S3_REGION", "local")
	t.Setenv("KB_S3_BUCKET", "evidence")
	t.Setenv("KB_S3_ACCESS_KEY", "fixture")
	t.Setenv("KB_S3_SECRET_KEY", "fixture-only")
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	t.Cleanup(cancel)
	var logs strings.Builder
	result := make(chan error, 1)
	go func() { result <- run(ctx, slog.New(slog.NewJSONHandler(&logs, nil))) }()
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: time.Second}
	ready := time.NewTicker(20 * time.Millisecond)
	t.Cleanup(ready.Stop)
	var response *http.Response
	for response == nil {
		select {
		case err := <-result:
			require.NoError(t, err)
			t.Fatal("server exited before serving")
		case <-ctx.Done():
			t.Fatal("server never became ready")
		case <-ready.C:
			req, err := http.NewRequestWithContext(ctx, "GET", "http://"+address+"/v1/status", nil)
			require.NoError(t, err)
			response, err = client.Do(req)
			if err != nil {
				response = nil
			}
		}
	}
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, 401, response.StatusCode)
	assert.Contains(t, string(body), "unauthorized")
	assert.NotContains(t, string(body), secret)
	cancel()
	require.NoError(t, <-result)
	assert.NotContains(t, logs.String(), secret)
	assert.Contains(t, logs.String(), "request_id")
}
