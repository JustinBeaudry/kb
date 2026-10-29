package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const qualifiedImage = "neo4j:2026.10.1-community@sha256:86f8e01402fd0d5fd23e8968375fefdc21284a5c1751fa3fa3de8091a08b6dd6"
const fixtureSecret = "qualification-fixture-only"

func graphFixture(t *testing.T, volumes ...string) (*Store, testcontainers.Container) {
	t.Helper()
	ctx := t.Context()
	var mounts testcontainers.ContainerMounts
	if len(volumes) > 0 {
		mounts = testcontainers.Mounts(testcontainers.VolumeMount(volumes[0], "/data"))
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{Image: qualifiedImage, Mounts: mounts, ExposedPorts: []string{"7687/tcp"},
			Env:        map[string]string{"NEO4J_AUTH": "neo4j/" + fixtureSecret, "NEO4J_server_memory_heap_initial__size": "256m", "NEO4J_server_memory_heap_max__size": "512m", "NEO4J_server_memory_pagecache_size": "128m"},
			WaitingFor: wait.ForLog("Started.").WithStartupTimeout(2 * time.Minute)}, Started: true})
	if container != nil {
		t.Cleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			require.NoError(t, container.Terminate(cleanupCtx))
		})
	}
	require.NoError(t, err, "real graph infrastructure is required; never skip qualification")
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "7687/tcp")
	require.NoError(t, err)
	store, err := Open(ctx, Config{URI: "bolt://" + host + ":" + port.Port(), Database: "neo4j", User: "neo4j", Secret: fixtureSecret})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close(context.Background())) })
	return store, container
}

func query(ctx context.Context, store *Store, text string, params map[string]any) ([]*neo4j.Record, error) {
	result, err := neo4j.ExecuteQuery(ctx, store.driver, text, params, neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(store.database))
	if err != nil {
		return nil, err
	}
	return result.Records, nil
}
func txQuery(ctx context.Context, store *Store, handle domain.Tx, text string, params map[string]any) ([]*neo4j.Record, error) {
	tx, err := store.Unwrap(handle)
	if err != nil {
		return nil, err
	}
	result, err := tx.Run(ctx, text, params)
	if err != nil {
		return nil, err
	}
	return result.Collect(ctx)
}

// Removing the OwnerState lock must admit multiple writes from the same head.
func TestQualifiedGraphSerializesHeadChanges(t *testing.T) {
	store, container := graphFixture(t)
	t.Logf("fixture resources: %s", dockerCommand(t, "stats", "--no-stream", "--format", "{{.MemUsage}} CPU={{.CPUPerc}}", container.GetContainerID()))
	_, err := query(t.Context(), store, "CREATE (:Claim {id:'race', head:'r0'})", nil)
	require.NoError(t, err)
	var accepted, conflicted atomic.Int64
	var workers sync.WaitGroup
	start := make(chan struct{})
	failures := make(chan error, 20)
	started := time.Now()
	for worker := range 20 {
		workers.Go(func() {
			<-start
			err := store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
				records, err := txQuery(ctx, store, tx, "MATCH (c:Claim {id:'race'}) RETURN c.head", nil)
				if err != nil {
					return err
				}
				head, _ := records[0].Values[0].(string)
				if head != "r0" {
					return domain.ErrConflict
				}
				// Widen the real read/write race without synchronizing inside a held lock.
				time.Sleep(20 * time.Millisecond)
				_, err = txQuery(ctx, store, tx, "MATCH (c:Claim {id:'race'}) SET c.head=$head", map[string]any{"head": fmt.Sprintf("r%d", worker+1)})
				return err
			})
			switch {
			case err == nil:
				accepted.Add(1)
			case errors.Is(err, domain.ErrConflict):
				conflicted.Add(1)
			default:
				failures <- err
			}
		})
	}
	close(start)
	workers.Wait()
	close(failures)
	for err := range failures {
		assert.NoError(t, err)
	}
	acceptedCount, conflictedCount := int(accepted.Load()), int(conflicted.Load())
	assert.Equal(t, 1, acceptedCount)
	assert.Equal(t, 19, conflictedCount)
	t.Logf("20 contenders elapsed=%s accepted=%d conflicted=%d", time.Since(started), accepted.Load(), conflicted.Load())
}

// Callback failure must roll back every state and outbox write.
func TestQualifiedGraphRollback(t *testing.T) {
	store, _ := graphFixture(t)
	expected := errors.New("fixture rollback")
	err := store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		_, err := txQuery(ctx, store, tx, "CREATE (:Event {id:'rolled-event'}), (:Job {id:'rolled-job'})", nil)
		if err != nil {
			return err
		}
		return expected
	})
	assert.ErrorIs(t, err, expected)
	rows, err := query(t.Context(), store, "MATCH (n) WHERE n.id IN ['rolled-event','rolled-job'] RETURN count(n)", nil)
	require.NoError(t, err)
	assert.Equal(t, int64(0), rows[0].Values[0])
}

// A second identity with the same ID must fail even outside the application lock.
func TestQualifiedGraphUniqueness(t *testing.T) {
	store, _ := graphFixture(t)
	_, err := query(t.Context(), store, "CREATE (:Event {id:'duplicate', idempotencyKey:'one'})", nil)
	require.NoError(t, err)
	_, err = query(t.Context(), store, "CREATE (:Event {id:'duplicate', idempotencyKey:'two'})", nil)
	require.Error(t, err)
	var graphErr *neo4j.Neo4jError
	require.ErrorAs(t, err, &graphErr)
	assert.Equal(t, "Neo.ClientError.Schema.ConstraintValidationFailed", graphErr.Code)
	t.Run("client idempotency key", func(t *testing.T) {
		_, err := query(t.Context(), store, "CREATE (:Event {id:'key-one',clientID:'collector',idempotencyKey:'same'})", nil)
		require.NoError(t, err)
		_, err = query(t.Context(), store, "CREATE (:Event {id:'key-two',clientID:'collector',idempotencyKey:'same'})", nil)
		require.Error(t, err)
		var graphErr *neo4j.Neo4jError
		require.ErrorAs(t, err, &graphErr)
		assert.Equal(t, "Neo.ClientError.Schema.ConstraintValidationFailed", graphErr.Code)
	})
}

// Reapplying schema must preserve identity and remain harmless.
func TestQualifiedGraphSchemaRerun(t *testing.T) {
	store, _ := graphFixture(t)
	_, err := query(t.Context(), store, "CREATE (:Claim {id:'stable', head:'immutable-revision'})", nil)
	require.NoError(t, err)
	for range 2 {
		require.NoError(t, store.migrate(t.Context()))
	}
	rows, err := query(t.Context(), store, "MATCH (c:Claim {id:'stable'}) RETURN c.head", nil)
	require.NoError(t, err)
	assert.Equal(t, "immutable-revision", rows[0].Values[0])
}

// A stopped server must never acknowledge a transaction or disclose credentials.
func TestQualifiedGraphDisconnect(t *testing.T) {
	store, container := graphFixture(t)
	timeout := 10 * time.Second
	require.NoError(t, container.Stop(t.Context(), &timeout))
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	t.Cleanup(cancel)
	called := false
	err := store.Within(ctx, func(context.Context, domain.Tx) error { called = true; return nil })
	assert.ErrorIs(t, err, domain.ErrUnavailable)
	assert.False(t, called)
	assert.NotContains(t, fmt.Sprint(err), fixtureSecret)
}

// A stale worker's fence must fail after another lease is committed, even if it
// supplies the original expected revision. This qualifies the lock/compare pattern;
// job-service policy and lease scheduling are later tasks.
func TestQualifiedGraphFencing(t *testing.T) {
	store, _ := graphFixture(t)
	_, err := query(t.Context(), store, "CREATE (:Job {id:'fenced', fence:1, worker:'old', head:'r0'})", nil)
	require.NoError(t, err)
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		_, err := txQuery(ctx, store, tx, "MATCH (j:Job {id:'fenced'}) SET j.fence=2,j.worker='new'", nil)
		return err
	}))
	err = store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		rows, err := txQuery(ctx, store, tx, "MATCH (j:Job {id:'fenced'}) RETURN j.fence,j.worker", nil)
		if err != nil {
			return err
		}
		if rows[0].Values[0] != int64(1) || rows[0].Values[1] != "old" {
			return domain.ErrStale
		}
		_, err = txQuery(ctx, store, tx, "MATCH (j:Job {id:'fenced'}) SET j.head='stale-result'", nil)
		return err
	})
	assert.ErrorIs(t, err, domain.ErrStale)
	rows, err := query(t.Context(), store, "MATCH (j:Job {id:'fenced'}) RETURN j.head,j.fence", nil)
	require.NoError(t, err)
	assert.Equal(t, []any{"r0", int64(2)}, rows[0].Values)
}

func TestQualifiedGraphExpiredTransactionHandle(t *testing.T) {
	store, _ := graphFixture(t)
	var captured domain.Tx
	require.NoError(t, store.Within(t.Context(), func(_ context.Context, tx domain.Tx) error {
		captured = tx
		_, err := (&Store{}).Unwrap(tx)
		assert.ErrorIs(t, err, domain.ErrInvalidInput)
		return nil
	}))
	_, err := store.Unwrap(nil)
	assert.ErrorIs(t, err, domain.ErrInvalidInput)
	_, err = store.Unwrap(captured)
	assert.ErrorIs(t, err, domain.ErrInvalidInput)
	assert.NotEmpty(t, captured.TransactionID())
	assert.ErrorIs(t, store.Within(t.Context(), nil), domain.ErrInvalidInput)
}

func TestQualifiedGraphConfigIsSecretSafe(t *testing.T) {
	for _, cfg := range []Config{{URI: "bolt://fixture:bad@localhost:7687", Database: "neo4j", User: "neo4j", Secret: fixtureSecret}, {URI: "file:///tmp/fixture", Database: "neo4j", User: "neo4j", Secret: fixtureSecret}, {URI: "bolt://localhost:7687"}} {
		t.Run(fmt.Sprintf("case-%d", len(cfg.URI)), func(t *testing.T) {
			_, err := Open(t.Context(), cfg)
			assert.ErrorIs(t, err, domain.ErrInvalidInput)
			for _, format := range []string{"%v", "%+v", "%#v"} {
				assert.NotContains(t, fmt.Sprintf(format, cfg), fixtureSecret)
			}
		})
	}
}

// Read the actual version pin, so changing test image selection cannot silently
// diverge from deployment. The contract tests validate the complete JSON elsewhere.
func TestQualifiedGraphPinnedImage(t *testing.T) {
	content, err := os.ReadFile("../../../../deploy/versions.json")
	require.NoError(t, err)
	var pin struct {
		Graph struct {
			Image string `json:"image"`
		} `json:"graph"`
	}
	require.NoError(t, json.Unmarshal(content, &pin))
	assert.Equal(t, qualifiedImage, pin.Graph.Image)
}

func dockerCommand(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	require.NoError(t, err, "docker %v failed: %s", args, string(output))
	return strings.TrimSpace(string(output))
}

// A genuine offline dump/load into a fresh volume must preserve opaque IDs,
// relationships, epochs, fencing and uniqueness. No raw filesystem copy substitutes.
func TestQualifiedGraphDumpLoadIdentity(t *testing.T) {
	suffix := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")) + fmt.Sprint(time.Now().UnixNano())
	sourceVolume, backupVolume, targetVolume := "kb-f1-source-"+suffix, "kb-f1-backup-"+suffix, "kb-f1-target-"+suffix
	for _, name := range []string{sourceVolume, backupVolume, targetVolume} {
		dockerCommand(t, "volume", "create", name)
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			output, err := exec.CommandContext(ctx, "docker", "volume", "rm", name).CombinedOutput()
			require.NoError(t, err, string(output))
		})
	}
	source, container := graphFixture(t, sourceVolume)
	_, err := query(t.Context(), source, `CREATE (c:Claim {id:'opaque-claim',head:'revision:immutable'}), (r:ClaimRevision {id:'revision:immutable',text:'fixture-only',status:'candidate'}), (e:Event {id:'opaque-event',clientID:'collector',idempotencyKey:'stable-key'}), (j:Job {id:'opaque-job',fence:7}), (d:Tombstone {id:'deleted-fixture'}) CREATE (c)-[:HAS_REVISION]->(r)-[:SUPPORTED_BY]->(e)`, nil)
	require.NoError(t, err)
	require.NoError(t, source.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		_, err := txQuery(ctx, source, tx, "MATCH (o:OwnerState {id:'owner'}) SET o.deletionEpoch=4,o.authEpoch=3", nil)
		return err
	}))
	require.NoError(t, source.Close(t.Context()))
	timeout := 60 * time.Second
	require.NoError(t, container.Stop(t.Context(), &timeout))
	exitCode := dockerCommand(t, "inspect", "--format", "{{.State.ExitCode}}", container.GetContainerID())
	assert.NotEqual(t, "137", exitCode, "maintenance shutdown must not be SIGKILL")
	t.Logf("offline source stopped with exit_code=%s", exitCode)
	started := time.Now()
	for _, database := range []string{"neo4j", "system"} {
		output := dockerCommand(t, "run", "--rm", "--volumes-from", container.GetContainerID(), "-v", backupVolume+":/backups", "--entrypoint", "neo4j-admin", qualifiedImage, "database", "dump", database, "--to-path=/backups")
		t.Logf("offline %s dump complete (%d output bytes)", database, len(output))
	}
	for _, database := range []string{"neo4j", "system"} {
		output := dockerCommand(t, "run", "--rm", "-v", targetVolume+":/data", "-v", backupVolume+":/backups", "--entrypoint", "neo4j-admin", qualifiedImage, "database", "load", database, "--from-path=/backups", "--overwrite-destination=true")
		t.Logf("offline %s load complete (%d output bytes)", database, len(output))
	}
	restored, _ := graphFixture(t, targetVolume)
	rows, err := query(t.Context(), restored, `MATCH (c:Claim {id:'opaque-claim'})-[:HAS_REVISION]->(r:ClaimRevision)-[:SUPPORTED_BY]->(e:Event), (j:Job {id:'opaque-job'}), (o:OwnerState {id:'owner'}), (d:Tombstone {id:'deleted-fixture'}) RETURN c.id,c.head,r.id,e.id,j.fence,o.deletionEpoch,o.authEpoch,d.id`, nil)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, []any{"opaque-claim", "revision:immutable", "revision:immutable", "opaque-event", int64(7), int64(4), int64(3), "deleted-fixture"}, rows[0].Values)
	_, err = query(t.Context(), restored, "CREATE (:Claim {id:'opaque-claim'})", nil)
	require.Error(t, err)
	t.Logf("dump/load plus restart and identity checks elapsed=%s", time.Since(started))
	t.Logf("backup volume: %s", dockerCommand(t, "run", "--rm", "-v", backupVolume+":/backups", "--entrypoint", "du", qualifiedImage, "-sk", "/backups"))
}

// Qualify the engine's exact-depth traversal and native fulltext capability.
// Service-level visit authorization/budgets remain F5's responsibility.
func TestQualifiedGraphBoundedTraversalAndSearch(t *testing.T) {
	store, _ := graphFixture(t)
	_, err := query(t.Context(), store, `CREATE (s:ClaimRevision {id:'root',text:'quartz'}) WITH s UNWIND range(1,60) AS i CREATE (n:ClaimRevision {id:'child-'+toString(i),text:'quartz'}) CREATE (s)-[:DEPENDS_ON]->(n) WITH n CREATE (n)-[:DEPENDS_ON]->(:ClaimRevision {id:'grandchild-'+n.id,text:'quartz'})`, nil)
	require.NoError(t, err)
	_, err = query(t.Context(), store, "CALL db.awaitIndexes(30)", nil)
	require.NoError(t, err)
	rows, err := query(t.Context(), store, "MATCH (:ClaimRevision {id:'root'})-[:DEPENDS_ON*0..2]->(n) RETURN DISTINCT n.id LIMIT 50", nil)
	require.NoError(t, err)
	assert.Len(t, rows, 50)
	rows, err = query(t.Context(), store, "CALL db.index.fulltext.queryNodes('claim_text','quartz') YIELD node,score RETURN node.id,score LIMIT 5", nil)
	require.NoError(t, err)
	assert.Len(t, rows, 5)
}
