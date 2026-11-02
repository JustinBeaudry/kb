package repository_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/handler"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/JustinBeaudry/kb/server/internal/repository"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestIdentityRealGraph(t *testing.T) {
	versionBytes, err := os.ReadFile("../../../deploy/versions.json")
	require.NoError(t, err)
	var pin struct {
		Graph struct {
			Image string `json:"image"`
		} `json:"graph"`
	}
	require.NoError(t, json.Unmarshal(versionBytes, &pin))
	const graphSecret = "identity-graph-fixture-only"
	container, err := testcontainers.GenericContainer(t.Context(), testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: pin.Graph.Image, ExposedPorts: []string{"7687/tcp"}, Env: map[string]string{"NEO4J_AUTH": "neo4j/" + graphSecret, "NEO4J_server_memory_heap_initial__size": "256m", "NEO4J_server_memory_heap_max__size": "512m", "NEO4J_server_memory_pagecache_size": "128m"}, WaitingFor: wait.ForLog("Started.").WithStartupTimeout(2 * time.Minute)}, Started: true})
	if container != nil {
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			require.NoError(t, container.Terminate(ctx))
		})
	}
	require.NoError(t, err, "real graph identity validation must not skip")
	host, err := container.Host(t.Context())
	require.NoError(t, err)
	port, err := container.MappedPort(t.Context(), "7687/tcp")
	require.NoError(t, err)
	store, err := graph.Open(t.Context(), graph.Config{URI: "bolt://" + host + ":" + port.Port(), Database: "neo4j", User: "neo4j", Secret: graphSecret})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close(context.Background())) })
	repo := repository.NewIdentityRepository(store)
	identity := service.NewIdentityService(store, repo, gateway.NostrIdentity{})
	owner := domain.OwnerIdentity{ID: "owner-public", PublicKey: "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"}
	input := service.EnrollInput{ClientID: "agent", Role: domain.RoleAgent, ProjectIDs: []string{"demo"}, Capabilities: []string{"status:read", "retrieval:read"}}
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	binary, maintenance := identityHostFixture(t, "bolt://"+host+":"+port.Port(), graphSecret)
	command := exec.CommandContext(t.Context(), binary, "setup", "--owner-id", owner.ID, "--owner-public-key", owner.PublicKey, "--project", "demo", "--capabilities", "status:read,retrieval:read", "--credential-fd", "3")
	command.ExtraFiles = []*os.File{writer}
	rejected := exec.CommandContext(t.Context(), binary, "setup", "--owner-id", owner.ID, "--owner-public-key", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", "--project", "demo", "--credential-fd", "3")
	rejected.ExtraFiles = []*os.File{writer}
	var rejectedOutput, rejectedDiagnostics bytes.Buffer
	rejected.Stdout = &rejectedOutput
	rejected.Stderr = &rejectedDiagnostics
	require.Error(t, rejected.Run(), "wrong-algorithm setup must fail before initializing owner")
	assert.Empty(t, rejectedOutput.String())
	assert.Contains(t, rejectedDiagnostics.String(), "invalid_input")
	assertIdentityHostDrained(t, maintenance)
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		unsetOwner, ownerErr := repo.Owner(ctx, tx)
		require.NoError(t, ownerErr)
		require.Equal(t, domain.OwnerIdentity{}, unsetOwner, "rejected host setup must not initialize owner")
		return nil
	}))
	// A caller-supplied descriptor hint is not the supervisor's inherited channel.
	forged := exec.CommandContext(t.Context(), binary, "setup", "--owner-id", owner.ID, "--owner-public-key", owner.PublicKey, "--project", "demo", "--credential-fd", "3")
	forged.Env = append(os.Environ(), "KB_HOST_EXECUTION_FD=4")
	forged.ExtraFiles = []*os.File{writer}
	var forgedOutput, forgedDiagnostics bytes.Buffer
	forged.Stdout = &forgedOutput
	forged.Stderr = &forgedDiagnostics
	require.Error(t, forged.Run(), "an arbitrary caller cannot impersonate the supervisor child")
	assert.Empty(t, forgedOutput.String())
	assertIdentityHostDrained(t, maintenance)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	require.NoError(t, command.Run(), "operator setup must succeed through protected host admission without Buzz")
	assertIdentityHostDrained(t, maintenance)
	require.NoError(t, writer.Close())
	transferred, err := io.ReadAll(reader)
	require.NoError(t, err)
	bearer := strings.TrimSpace(string(transferred))
	require.Len(t, bearer, 64)
	assert.Empty(t, stdout.String())
	assert.Empty(t, stderr.String())
	p, err := identity.Authenticate(t.Context(), bearer)
	require.NoError(t, err)
	credential := service.ClientCredential{ClientID: p.ClientID, Bearer: bearer, Principal: p}
	ownerPrincipal := domain.Principal{OwnerID: owner.ID, Role: domain.RoleOwner}
	t.Run("one-time setup and hash-only persistence", func(t *testing.T) {
		_, err := identity.Setup(t.Context(), owner, input)
		assert.ErrorIs(t, err, domain.ErrConflict)
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			storedOwner, err := repo.Owner(ctx, tx)
			require.NoError(t, err)
			assert.Equal(t, owner.ID, storedOwner.ID)
			assert.Equal(t, owner.PublicKey, storedOwner.PublicKey)
			assert.NotEqual(t, storedOwner.ID, storedOwner.PublicKey)
			record, err := repo.FindToken(ctx, tx, service.TokenDigest(credential.Bearer))
			require.NoError(t, err)
			assert.Equal(t, service.TokenDigest(credential.Bearer), record.TokenDigest)
			native, err := store.Unwrap(tx)
			require.NoError(t, err)
			result, err := native.Run(ctx, "MATCH (c:Client) RETURN properties(c)", nil)
			require.NoError(t, err)
			rows, err := result.Collect(ctx)
			require.NoError(t, err)
			encoded, err := json.Marshal(rows[0].Values)
			require.NoError(t, err)
			assert.NotContains(t, string(encoded), credential.Bearer)
			return nil
		}))
	})
	e, err := handler.NewIdentityServer(identity, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	require.NoError(t, err)
	request := func(method, path, body, token string) (int, string) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		e.ServeHTTP(response, req)
		return response.Code, response.Body.String()
	}
	t.Run("scoped metadata without private IDs or tokens", func(t *testing.T) {
		code, body := request("GET", "/v1/status", "", credential.Bearer)
		assert.Equal(t, 200, code)
		assert.Contains(t, body, "demo")
		assert.Contains(t, body, "recovery_unavailable")
		assert.NotContains(t, body, owner.ID)
		assert.NotContains(t, body, credential.Bearer)
	})
	t.Run("role forgery cannot enroll verifier", func(t *testing.T) {
		code, responseBody := request("POST", "/v1/clients", `{"id":"private-project","role":"verifier","project_ids":["private-project"],"capabilities":["verification:write"]}`, credential.Bearer)
		assert.Equal(t, 403, code)
		fixtureSecret := credential.Bearer
		assert.NotContains(t, responseBody, fixtureSecret)
		assert.NotContains(t, responseBody, "private-project")
	})
	t.Run("absent operation fails unavailable", func(t *testing.T) {
		code, body := request("GET", "/v1/clients", "", credential.Bearer)
		assert.Equal(t, 503, code)
		assert.Contains(t, body, "unavailable")
	})
	t.Run("cross project forbidden", func(t *testing.T) {
		p, err := identity.Authenticate(t.Context(), credential.Bearer)
		require.NoError(t, err)
		assert.ErrorIs(t, service.Authorize(p, "retrieval:read", []string{"private-project"}), domain.ErrForbidden)
	})
	t.Run("committed revocation rejects next HTTP request", func(t *testing.T) {
		require.NoError(t, identity.Revoke(t.Context(), ownerPrincipal, credential.ClientID))
		code, responseBody := request(http.MethodGet, "/v1/status", "", credential.Bearer)
		assert.Equal(t, 401, code)
		fixtureSecret := credential.Bearer
		assert.NotContains(t, responseBody, fixtureSecret)
		assert.NotContains(t, responseBody, owner.ID)
	})
	t.Run("revocation increments authoritative epoch", func(t *testing.T) {
		next, err := identity.Enroll(t.Context(), ownerPrincipal, service.EnrollInput{ClientID: "collector", Role: domain.RoleCollector, ProjectIDs: []string{"demo"}, Capabilities: []string{"events:append"}})
		require.NoError(t, err)
		p, err := identity.Authenticate(t.Context(), next.Bearer)
		require.NoError(t, err)
		assert.Equal(t, uint64(1), p.AuthEpoch)
	})
	t.Run("wrong owner cannot administer", func(t *testing.T) {
		err := identity.Revoke(t.Context(), domain.Principal{Role: domain.RoleOwner, OwnerID: "forged"}, "collector")
		assert.ErrorIs(t, err, domain.ErrForbidden)
	})
}
