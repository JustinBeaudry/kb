//go:build integration

package gateway

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/JustinBeaudry/kb/server/internal/repository"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CI runs this explicit tag. It never skips for missing Docker/configuration.
// The pinned image executes the real Go process gateway as UID7474, including
// its child, exact local tools, both dump/load commands and fresh data paths.
func TestNeo4jProcessRealOfflineRoundTrip(t *testing.T) {
	if os.Getenv("KB_PROCESS_INSIDE_FIXTURE") == "1" {
		testRealNeo4jChild(t)
		return
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "gateway.test")
	build := exec.CommandContext(t.Context(), "go", "test", "-c", "-tags", "integration", "-o", binary, ".")
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	output, err := build.CombinedOutput()
	require.NoError(t, err, string(output))
	source, err := os.ReadFile("../../../scripts/Neo4jInitialPassword.java")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Neo4jInitialPassword.java"), source, 0644))
	versions, err := os.ReadFile("../../../deploy/versions.json")
	require.NoError(t, err)
	var pin struct {
		Graph struct {
			Image string `json:"image"`
		} `json:"graph"`
	}
	require.NoError(t, json.Unmarshal(versions, &pin))
	ctx, cancel := context.WithTimeout(t.Context(), 13*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "docker", "run", "--rm", "--platform", "linux/amd64", "--cpus", "2", "--memory", "4g", "--user", "7474:7474", "--entrypoint", "/bin/sh", "-e", "KB_PROCESS_INSIDE_FIXTURE=1", "-v", dir+":/fixture:ro", pin.Graph.Image, "-c", `mkdir /tmp/kb-auth-classes && javac -cp '/var/lib/neo4j/lib/*' -d /tmp/kb-auth-classes /fixture/Neo4jInitialPassword.java && /fixture/gateway.test -test.run '^TestNeo4jProcessRealOfflineRoundTrip$' -test.v -test.timeout=12m`)
	output, err = command.CombinedOutput()
	require.NoError(t, err, string(output))
	t.Log(string(output))
}
func testRealNeo4jChild(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "kb-real-process-")
	require.NoError(t, err)
	// The disposable --rm container owns filesystem cleanup; emulated Go
	// recursive removal of database stores can outlast the fixture itself.
	state := &processMemory{}
	process, err := NewNeo4jProcess(Neo4jProcessConfig{Root: filepath.Join(root, "graph"), Home: "/var/lib/neo4j", Neo4jBinary: "/var/lib/neo4j/bin/neo4j", AdminBinary: "/var/lib/neo4j/bin/neo4j-admin", JavaBinary: "/opt/java/openjdk/bin/java", AuthClassPath: "/tmp/kb-auth-classes:/var/lib/neo4j/lib/*", GraphSecret: "fixture-process-password"}, state, t.Context())
	require.NoError(t, err)
	require.NoError(t, process.Start(t.Context()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_ = process.Stop(ctx)
		_ = process.WaitStopped(ctx)
	})
	connect := func() *graph.Store {
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
		defer cancel()
		for {
			store, e := graph.Connect(ctx, graph.Config{URI: "bolt://127.0.0.1:7687", Database: "neo4j", User: "neo4j", Secret: "fixture-process-password"})
			if e == nil {
				if e = store.Probe(ctx); e == nil {
					return store
				}
				_ = store.Close(context.WithoutCancel(ctx))
			}
			select {
			case <-ctx.Done():
				t.Fatal("real Neo4j child did not become connectable; inspect protected fixture logs")
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	store := connect()
	if err = store.InitializeSchema(t.Context()); err != nil {
		for _, name := range []string{"debug.log", "neo4j.log"} {
			data, readErr := os.ReadFile(filepath.Join(state.state.Root, "logs", name))
			if readErr == nil {
				if len(data) > 16384 {
					data = data[len(data)-16384:]
				}
				t.Logf("protected fixture %s tail: %s", name, strings.ReplaceAll(string(data), "fixture-process-password", "[redacted]"))
			}
		}
	}
	require.NoError(t, err)
	assertQualifiedNeo4jLanguageAndCandidate(t, store, "before-restore")
	identity := service.NewIdentityService(store, repository.NewIdentityRepository(store), NostrIdentity{})
	owner := domain.OwnerIdentity{ID: "restore-owner", PublicKey: "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"}
	old, err := identity.Setup(t.Context(), owner, service.EnrollInput{ClientID: "snapshot-client", Role: domain.RoleAgent, ProjectIDs: []string{"fixture"}, Capabilities: []string{"status:read"}})
	require.NoError(t, err)
	require.NoError(t, store.Close(t.Context()))
	started := time.Now()
	require.NoError(t, process.Stop(t.Context()))
	require.NoError(t, process.WaitStopped(t.Context()))
	data, system, err := process.Dump(t.Context(), filepath.Join(root, "backup"))
	require.NoError(t, err)
	require.FileExists(t, data)
	require.FileExists(t, system)
	original := state.state.Root
	require.NoError(t, process.Load(t.Context(), data, system))
	assert.NotEqual(t, original, state.state.Root)
	require.DirExists(t, original)
	require.NoError(t, process.Start(t.Context()))
	restored := connect()
	t.Cleanup(func() { _ = restored.Close(context.Background()) })
	require.NoError(t, restored.InitializeSchema(t.Context()))
	assertQualifiedNeo4jLanguageAndCandidate(t, restored, "after-restore")
	restoredIdentity := service.NewIdentityService(restored, repository.NewIdentityRepository(restored), NostrIdentity{})
	_, err = restoredIdentity.Authenticate(t.Context(), old.Bearer)
	require.NoError(t, err, "both graph records and system auth must restore before revocation")
	repo := repository.NewBackupRepository(restored)
	invalidate := func() {
		require.NoError(t, restored.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			return repo.InvalidateRestoredClients(ctx, tx, "restore-unique-1")
		}))
	}
	invalidate()
	_, err = restoredIdentity.Authenticate(t.Context(), old.Bearer)
	require.Error(t, err)
	fresh, err := restoredIdentity.Enroll(t.Context(), domain.Principal{OwnerID: owner.ID, Role: domain.RoleOwner}, service.EnrollInput{ClientID: "fresh-client", Role: domain.RoleAgent, ProjectIDs: []string{"fixture"}, Capabilities: []string{"status:read"}})
	require.NoError(t, err)
	invalidate()
	_, err = restoredIdentity.Authenticate(t.Context(), fresh.Bearer)
	require.NoError(t, err, "retry must preserve post-restore enrollment")
	t.Logf("real supervisor stop/dump both/load fresh/start/revoke elapsed=%s uid=%d", time.Since(started), os.Geteuid())
}

// The image's stock config selects Cypher 25, but the supervisor generates its
// own config. Inspect the database itself and execute the ordinary repository
// candidate query so silently reverting to Neo4j's Cypher 5 default is caught.
func assertQualifiedNeo4jLanguageAndCandidate(t *testing.T, store *graph.Store, id string) {
	t.Helper()
	driver, err := neo4j.NewDriver("bolt://127.0.0.1:7687", neo4j.BasicAuth("neo4j", "fixture-process-password", ""))
	require.NoError(t, err)
	t.Cleanup(func() { _ = driver.Close(context.Background()) })
	result, err := neo4j.ExecuteQuery(t.Context(), driver, "SHOW DATABASE neo4j YIELD defaultLanguage RETURN defaultLanguage", nil, neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase("system"))
	require.NoError(t, err)
	require.Len(t, result.Records, 1)
	require.Equal(t, "CYPHER 25", result.Records[0].Values[0], "generated process config must create and retain the qualified database language")
	candidate := domain.ClaimRevision{ClaimID: id, Revision: id + "-revision", SubmittedBy: "fixture-client", Text: "Synthetic process dialect regression", Kind: "finding", Scope: domain.Scope{ProjectID: "fixture", TaskID: id}, Status: domain.ClaimCandidate}
	repo := repository.NewMemoryRepository(store)
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		return repo.PutCandidate(ctx, tx, candidate)
	}))
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		got, client, err := repo.Head(ctx, tx, candidate.ClaimID)
		if err != nil {
			return err
		}
		assert.Equal(t, candidate, got)
		assert.Equal(t, candidate.SubmittedBy, client)
		return nil
	}))
}
