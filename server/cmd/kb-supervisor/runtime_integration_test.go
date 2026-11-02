package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/platform"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/JustinBeaudry/kb/server/internal/repository"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func runtimeComponentContainer(t *testing.T, request testcontainers.ContainerRequest) testcontainers.Container {
	t.Helper()
	container, err := testcontainers.GenericContainer(t.Context(), testcontainers.GenericContainerRequest{ContainerRequest: request, Started: true})
	if container != nil {
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			require.NoError(t, container.Terminate(ctx))
		})
	}
	require.NoError(t, err, "real runtime component fixture is required; never skip")
	return container
}
func runtimeComponentGraph(t *testing.T) graph.Config {
	t.Helper()
	raw, err := os.ReadFile("../../../deploy/versions.json")
	require.NoError(t, err)
	var pins struct {
		Graph struct {
			Image string `json:"image"`
		} `json:"graph"`
	}
	require.NoError(t, json.Unmarshal(raw, &pins))
	const secret = "runtime-component-graph-only"
	container := runtimeComponentContainer(t, testcontainers.ContainerRequest{Image: pins.Graph.Image, ExposedPorts: []string{"7687/tcp"}, Env: map[string]string{"NEO4J_AUTH": "neo4j/" + secret, "NEO4J_server_memory_heap_initial__size": "256m", "NEO4J_server_memory_heap_max__size": "512m", "NEO4J_server_memory_pagecache_size": "128m"}, WaitingFor: wait.ForLog("Started.").WithStartupTimeout(2 * time.Minute)})
	host, err := container.Host(t.Context())
	require.NoError(t, err)
	port, err := container.MappedPort(t.Context(), "7687/tcp")
	require.NoError(t, err)
	return graph.Config{URI: "bolt://" + host + ":" + port.Port(), Database: "neo4j", User: "neo4j", Secret: secret}
}
func runtimeComponentObjects(t *testing.T) (map[string]*gateway.S3, *gateway.S3DeletionJournal, *gateway.S3BackupStore, *s3.Client) {
	t.Helper()
	container := runtimeComponentContainer(t, testcontainers.ContainerRequest{Image: "chrislusf/seaweedfs:4.51@sha256:f7a49e3f3972ebed5be2ed4befa4a0c68eb7bdddba6e692d42729ceb545134b8", ExposedPorts: []string{"8333/tcp"}, Cmd: []string{"mini", "-dir=/data", "-ip.bind=0.0.0.0", "-master.telemetry=false"}, Env: map[string]string{"AWS_ACCESS_KEY_ID": "fixture", "AWS_SECRET_ACCESS_KEY": "fixture-secret-only", "S3_BUCKET": "evidence"}, WaitingFor: wait.ForListeningPort("8333/tcp").WithStartupTimeout(90 * time.Second)})
	host, err := container.Host(t.Context())
	require.NoError(t, err)
	port, err := container.MappedPort(t.Context(), "8333/tcp")
	require.NoError(t, err)
	endpoint := "http://" + host + ":" + port.Port()
	client := s3.New(s3.Options{Region: "local", BaseEndpoint: aws.String(endpoint), UsePathStyle: true, Credentials: credentials.NewStaticCredentialsProvider("fixture", "fixture-secret-only", "")})
	for _, bucket := range []string{"journal", "backups", "fresh-evidence"} {
		_, err = client.CreateBucket(t.Context(), &s3.CreateBucketInput{Bucket: aws.String(bucket)})
		require.NoError(t, err)
	}
	config := func(bucket string) gateway.S3Config {
		return gateway.S3Config{Endpoint: endpoint, Region: "local", Bucket: bucket, AccessKey: "fixture", SecretKey: "fixture-secret-only", AddressingStyle: "path"}
	}
	objects := map[string]*gateway.S3{}
	for _, bucket := range []string{"evidence", "fresh-evidence"} {
		objects[bucket], err = gateway.NewS3(config(bucket))
		require.NoError(t, err)
	}
	deadline := time.Now().Add(90 * time.Second)
	for {
		probe := "component-ready-" + service.TokenDigest(time.Now().String())
		err = objects["evidence"].Put(t.Context(), probe, strings.NewReader("synthetic"), service.TokenDigest("synthetic"))
		if err == nil {
			require.NoError(t, objects["evidence"].Delete(t.Context(), probe))
			break
		}
		require.True(t, time.Now().Before(deadline), "object fixture did not become writable")
		time.Sleep(250 * time.Millisecond)
	}
	var journalKey [32]byte
	copy(journalKey[:], bytes.Repeat([]byte{7}, 32))
	journal, err := gateway.NewS3DeletionJournal(gateway.S3DeletionJournalConfig{Endpoint: endpoint, Region: "local", Bucket: "journal", Prefix: "deletions", Namespace: "runtime-component", AccessKey: "fixture", SecretKey: "fixture-secret-only", AddressingStyle: "path", KeyID: "journal-key", Keys: map[string][32]byte{"journal-key": journalKey}})
	require.NoError(t, err)
	backups, err := gateway.NewS3BackupStore(config("backups"))
	require.NoError(t, err)
	return objects, journal, backups, client
}
func runtimeComponentArchive(t *testing.T, store *gateway.S3BackupStore, objects *gateway.S3) (*gateway.BackupArchiveGateway, gateway.BackupArchiveConfig) {
	t.Helper()
	identity, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	var key [32]byte
	copy(key[:], bytes.Repeat([]byte{9}, 32))
	config := gateway.BackupArchiveConfig{Namespace: "runtime-backups", ScratchRoot: filepath.Join(root, "scratch"), AuthorityKeyID: "manifest-key", AuthorityKeys: map[string][32]byte{"manifest-key": key}, Recipients: map[string]string{"archive-old": identity.Recipient().String()}, Identities: map[string]string{"archive-old": identity.String()}, MaxArchiveBytes: 16 << 20}
	archive, err := gateway.NewBackupArchive(store, objects, config)
	require.NoError(t, err)
	return archive, config
}

func TestRuntimeSnapshotRealGraphEvidenceAndRecoveryAuthority(t *testing.T) {
	tools, err := filepath.Abs("../../../deploy/versions.json")
	require.NoError(t, err)
	t.Setenv("KB_TOOL_VERSIONS_FILE", tools)
	t.Setenv("KB_RETRIEVAL_HMAC_KEY_FILE", "")
	t.Setenv("KB_RETRIEVAL_HMAC_KEY", strings.Repeat("a", 64))
	graphConfig := runtimeComponentGraph(t)
	objects, journal, backups, rawObjects := runtimeComponentObjects(t)
	archive, archiveConfig := runtimeComponentArchive(t, backups, objects["evidence"])
	config := supervisorConfig{Platform: platform.Config{Graph: graphConfig}, EvidenceNamespace: "original-evidence"}
	snapshot, err := newRuntimeSnapshot(config, objects["evidence"], journal, archive)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, snapshot.Close(context.Background())) })
	assert.False(t, snapshot.HostReady())
	require.ErrorIs(t, snapshot.Probe(t.Context()), domain.ErrUnavailable)
	_, err = snapshot.ReconcileJournal(t.Context())
	require.ErrorIs(t, err, domain.ErrUnavailable)
	_, err = snapshot.CaptureManifest(t.Context(), "before-init")
	require.ErrorIs(t, err, domain.ErrUnavailable)
	require.ErrorIs(t, snapshot.InvalidateRestoredClients(t.Context(), "before-init"), domain.ErrUnavailable)
	require.ErrorIs(t, snapshot.VerifyRestore(t.Context(), domain.BackupManifest{}), domain.ErrUnavailable)
	require.ErrorIs(t, snapshot.ValidateRestoredSnapshot(t.Context(), domain.BackupManifest{}), domain.ErrUnavailable)
	require.ErrorIs(t, snapshot.bootstrapPrerequisites(t.Context()), domain.ErrUnavailable)
	require.NoError(t, snapshot.InitializeAndRecover(t.Context()))
	assert.True(t, snapshot.HostReady())
	require.NoError(t, snapshot.Probe(t.Context()))
	require.ErrorIs(t, snapshot.bootstrapPrerequisites(t.Context()), domain.ErrIncomplete)
	telemetry, err := platform.NewTelemetry(io.Discard, "graph")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, telemetry.Shutdown(context.Background())) })
	snapshot.trace = telemetry.TraceIO
	require.NoError(t, snapshot.InitializeAndRecover(t.Context()), "reconnect closes the previous driver and explicitly reinitializes schema")
	owners := repository.NewIdentityRepository(snapshot.store)
	jobs := repository.NewJobsRepository(snapshot.store)
	identity := service.NewIdentityService(snapshot.store, owners, gateway.NostrIdentity{})
	collector, err := identity.Setup(t.Context(), domain.OwnerIdentity{ID: "runtime-owner", PublicKey: "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"}, service.EnrollInput{ClientID: "runtime-collector", Role: domain.RoleCollector, ProjectIDs: []string{"demo"}, Capabilities: []string{"artifacts:write"}})
	require.NoError(t, err)
	lifecycle := service.NewLifecycleService(snapshot.store, repository.NewLifecycleRepository(snapshot.store), owners, jobs, objects["evidence"], journal, time.Now)
	_, err = lifecycle.InitializeJournalHost(t.Context())
	require.NoError(t, err)
	require.ErrorIs(t, snapshot.bootstrapPrerequisites(t.Context()), domain.ErrIncomplete, "owner and journal do not substitute for policy")
	_, err = lifecycle.UpdatePolicyHost(t.Context(), "demo", "", domain.Policy{Revision: "runtime-policy", SpoolLimitBytes: 1 << 20})
	require.NoError(t, err)
	require.NoError(t, snapshot.bootstrapPrerequisites(t.Context()))
	barrier, err := snapshot.ReconcileJournal(t.Context())
	require.NoError(t, err)
	capture := service.NewCaptureService(snapshot.store, repository.NewEvidenceRepository(snapshot.store), jobs, objects["evidence"])
	source := "pinned runtime evidence"
	artifact, err := capture.Stage(t.Context(), collector.Principal, service.ArtifactInput{Scope: domain.Scope{ProjectID: "demo"}, Digest: service.TokenDigest(source), Bytes: int64(len(source)), PolicyRevision: "runtime-policy", SourceLocator: "runtime-source", SourcePath: "runtime.txt"}, strings.NewReader(source))
	require.NoError(t, err)
	_, err = capture.Complete(t.Context(), collector.Principal, artifact.ID, artifact.Digest)
	require.NoError(t, err)
	pending, err := capture.Stage(t.Context(), collector.Principal, service.ArtifactInput{Scope: domain.Scope{ProjectID: "demo"}, Digest: service.TokenDigest("not-completed"), Bytes: 13, PolicyRevision: "runtime-policy", SourceLocator: "pending-source", SourcePath: "pending.txt"}, strings.NewReader("not-completed"))
	require.NoError(t, err)
	manifest, err := snapshot.CaptureManifest(t.Context(), "runtime-backup")
	require.NoError(t, err)
	require.Len(t, manifest.Objects, 2)
	manifest.Journal = barrier
	states := map[string]domain.BackupObject{}
	for _, object := range manifest.Objects {
		states[object.ArtifactID] = object
	}
	assert.True(t, states[artifact.ID].Available)
	assert.False(t, states[pending.ID].Available)
	require.NoError(t, snapshot.ValidateRestoredSnapshot(t.Context(), manifest))
	wrong := manifest
	wrong.Journal.Sequence++
	require.ErrorIs(t, snapshot.ValidateRestoredSnapshot(t.Context(), wrong), domain.ErrStale)
	wrong = manifest
	wrong.Objects = nil
	require.ErrorIs(t, snapshot.ValidateRestoredSnapshot(t.Context(), wrong), domain.ErrIncomplete)
	wrong = manifest
	wrong.SchemaDigest = "wrong-schema"
	require.ErrorIs(t, snapshot.VerifyRestore(t.Context(), wrong), domain.ErrUnavailable)
	_, err = snapshot.CaptureManifest(t.Context(), "invalid/id")
	require.ErrorIs(t, err, domain.ErrInvalidInput)
	require.ErrorIs(t, snapshot.InvalidateRestoredClients(t.Context(), "invalid/id"), domain.ErrInvalidInput)
	dumpRoot := t.TempDir()
	dataPath, systemPath := filepath.Join(dumpRoot, "neo4j.dump"), filepath.Join(dumpRoot, "system.dump")
	require.NoError(t, os.WriteFile(dataPath, []byte("component-envelope-data"), 0600))
	require.NoError(t, os.WriteFile(systemPath, []byte("component-envelope-system"), 0600))
	require.NoError(t, archive.Write(t.Context(), manifest, dataPath, systemPath))
	archived, err := archive.Restore(t.Context(), manifest.BackupID, filepath.Join(archiveConfig.ScratchRoot, "restore-runtime"))
	require.NoError(t, err)
	require.NoError(t, snapshot.Close(t.Context()))
	t.Setenv("KB_RETRIEVAL_HMAC_KEY", strings.Repeat("b", 64))
	config.EvidenceNamespace = "fresh-evidence"
	restored, err := newRuntimeSnapshot(config, objects["fresh-evidence"], journal, archive)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, restored.Close(context.Background())) })
	require.NoError(t, restored.ValidateBackup(t.Context(), archived))
	require.NoError(t, restored.InitializeAndRecover(t.Context()))
	require.NoError(t, restored.ValidateRestoredSnapshot(t.Context(), archived))
	require.NoError(t, restored.VerifyRestore(t.Context(), archived))
	body, err := objects["fresh-evidence"].Open(t.Context(), artifact.ObjectKey)
	require.NoError(t, err)
	data, err := io.ReadAll(body)
	require.NoError(t, err)
	require.NoError(t, body.Close())
	assert.Equal(t, source, string(data))
	owners = repository.NewIdentityRepository(restored.store)
	jobs = repository.NewJobsRepository(restored.store)
	identity = service.NewIdentityService(restored.store, owners, gateway.NostrIdentity{})
	capture = service.NewCaptureService(restored.store, repository.NewEvidenceRepository(restored.store), jobs, objects["fresh-evidence"])
	require.NoError(t, objects["fresh-evidence"].Delete(t.Context(), artifact.ObjectKey))
	_, err = capture.Complete(t.Context(), collector.Principal, artifact.ID, artifact.Digest)
	require.ErrorIs(t, err, domain.ErrIncomplete)
	_, err = restored.CaptureManifest(t.Context(), "missing-source")
	require.ErrorIs(t, err, domain.ErrIncomplete, "known unavailable ready evidence cannot become a completed backup")
	require.ErrorIs(t, restored.VerifyRestore(t.Context(), archived), domain.ErrStale)
	lifecycle = service.NewLifecycleService(restored.store, repository.NewLifecycleRepository(restored.store), owners, jobs, objects["fresh-evidence"], journal, time.Now)
	selection := domain.DeleteSelection{ProjectID: "demo", IDs: []string{artifact.ID}, IdempotencyKey: "runtime-delete"}
	preview, err := lifecycle.PreviewHost(t.Context(), selection)
	require.NoError(t, err)
	selection.ExpectedSelectionDigest = preview.SelectionDigest
	_, err = lifecycle.DeleteHost(t.Context(), selection)
	require.NoError(t, err)
	require.NoError(t, restored.VerifyRestore(t.Context(), archived), "live deletion must fence archived source bytes rather than reintroduce them")
	_, err = objects["fresh-evidence"].Open(t.Context(), artifact.ObjectKey)
	require.ErrorIs(t, err, domain.ErrIncomplete)
	require.ErrorIs(t, objects["fresh-evidence"].Put(t.Context(), artifact.ObjectKey, strings.NewReader(source), artifact.Digest), domain.ErrDeleted)
	require.ErrorIs(t, restored.ValidateRestoredSnapshot(t.Context(), archived), domain.ErrStale)
	updated, err := restored.CaptureManifest(t.Context(), "after-delete")
	require.NoError(t, err)
	var deleted bool
	for _, object := range updated.Objects {
		if object.ArtifactID == artifact.ID {
			deleted = object.Deleted
		}
	}
	assert.True(t, deleted)
	require.NoError(t, restored.InvalidateRestoredClients(t.Context(), "runtime-restore"))
	_, err = identity.Authenticate(t.Context(), collector.Bearer)
	require.ErrorIs(t, err, domain.ErrUnauthorized)
	renewed, err := identity.EnrollHost(t.Context(), service.EnrollInput{ClientID: "runtime-renewed", Role: domain.RoleCollector, ProjectIDs: []string{"demo"}, Capabilities: []string{"artifacts:write"}})
	require.NoError(t, err)
	require.NoError(t, restored.InvalidateRestoredClients(t.Context(), "runtime-restore"))
	_, err = identity.Authenticate(t.Context(), renewed.Bearer)
	require.NoError(t, err, "same restore retry preserves fresh enrollment")
	require.NoError(t, restored.InvalidateRestoredClients(t.Context(), "runtime-restore-two"))
	_, err = identity.Authenticate(t.Context(), renewed.Bearer)
	require.ErrorIs(t, err, domain.ErrUnauthorized)
	replacement, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	archiveConfig.Recipients = map[string]string{"archive-new": replacement.Recipient().String()}
	archiveConfig.Identities = map[string]string{"archive-new": replacement.String()}
	missingKeys, err := gateway.NewBackupArchive(backups, objects["fresh-evidence"], archiveConfig)
	require.NoError(t, err)
	require.ErrorIs(t, missingKeys.CheckRecoveryKeys(t.Context()), domain.ErrUnavailable, "retained archive requires its original decryption identity")
	_, err = rawObjects.DeleteObject(t.Context(), &s3.DeleteObjectInput{Bucket: aws.String("journal"), Key: aws.String("deletions/runtime-component/head")})
	require.NoError(t, err)
	_, err = restored.ReconcileJournal(t.Context())
	require.ErrorIs(t, err, domain.ErrUnavailable)
	require.ErrorIs(t, restored.bootstrapPrerequisites(t.Context()), domain.ErrUnavailable, "historical journal loss is never a fresh-install signal")
	require.NoError(t, restored.Close(t.Context()))
	require.NoError(t, restored.Close(t.Context()))
	require.ErrorIs(t, restored.Probe(t.Context()), domain.ErrUnavailable)
	failed := &runtimeSnapshot{cfg: supervisorConfig{Platform: platform.Config{Graph: graph.Config{URI: "bolt://127.0.0.1:1", Database: "neo4j", User: "neo4j", Secret: "fixture-only"}}}}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, failed.InitializeAndRecover(canceled), domain.ErrUnavailable)
	assert.False(t, failed.HostReady())
}
