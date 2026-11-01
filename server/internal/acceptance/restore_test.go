//go:build acceptance

package acceptance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/handler"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This exercise launches the packaged binaries. No direct graph reads/writes,
// hidden publication helper, skipped provider, or fabricated evidence is used.
func TestCompiledMemoryRestoreAcceptance(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Minute)
	t.Cleanup(cancel)
	repo, err := filepath.Abs("../../..")
	require.NoError(t, err)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0700))
	f := &compiledFixture{t: t, ctx: ctx, repo: repo, dir: dir, client: &http.Client{Timeout: 30 * time.Second}}
	t.Cleanup(func() {
		if _, err := os.Stat(filepath.Join(dir, "runtime.json")); os.IsNotExist(err) {
			return
		}
		if t.Failed() {
			f.preserveDiagnostics()
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(cleanup, filepath.Join(repo, "deploy/compose-smoke.sh"), "down", dir)
		log, err := os.OpenFile(filepath.Join(dir, "cleanup.log"), os.O_CREATE|os.O_WRONLY, 0600)
		if err == nil {
			cmd.Stdout = log
			cmd.Stderr = log
			defer func() { _ = log.Close() }()
		}
		if err := cmd.Run(); err != nil {
			t.Errorf("owned fixture cleanup failed: %v", err)
		}
	})
	started := time.Now()
	f.script("up")
	raw, err := os.ReadFile(filepath.Join(dir, "runtime.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &f.runtime))
	f.waitFirstBackup()
	originalAgent := f.credential(f.runtime["credential_file"])
	collector := f.enroll("acceptance-collector", "collector", "artifacts:write")
	removed := f.publish(originalAgent, collector, "remove", "Synthetic deletion evidence must remain absent after restore.")
	retained := f.publish(originalAgent, collector, "retain", "Synthetic retained evidence must keep its original citation digest.")
	before := f.search(originalAgent)
	assertChunk(t, before, "published-remove", removed.digest, true)
	assertChunk(t, before, "published-retain", retained.digest, true)
	t.Logf("compiled first authenticated result elapsed=%s (includes build and startup)", time.Since(started))

	f.exerciseDrain(originalAgent, collector)

	backupStarted := time.Now()
	backup := f.operator("backup", "--id", "acceptance-before-delete")
	require.Equal(t, "ok", backup.Code)
	require.Equal(t, "complete", backup.Result.State)
	t.Logf("offline backup and reopen elapsed=%s", time.Since(backupStarted))
	selection := domain.DeleteSelection{ProjectID: "fixture-project", IDs: []string{removed.artifactID}, IdempotencyKey: "acceptance-delete"}
	var preview domain.DeletionPreview
	f.admin(&preview, selection, "delete", "--preview")
	selection.ExpectedSelectionDigest = preview.SelectionDigest
	var deletion domain.DeletionReceipt
	f.admin(&deletion, selection, "delete")
	require.Eventually(t, func() bool {
		var receipt domain.DeletionReceipt
		f.admin(&receipt, map[string]string{"ID": deletion.ID}, "deletion-status")
		return receipt.State == "purge_complete"
	}, 2*time.Minute, 2*time.Second)
	assertChunk(t, f.search(originalAgent), "published-remove", removed.digest, false)

	// Explicit host drain precedes process stops. No idle-window assumption or
	// arbitrary state-file edit can make outstanding effects disappear.
	paused := f.operator("pause", "--id", "acceptance-restore")
	require.Equal(t, "ok", paused.Code)
	f.compose(nil, "stop", "api", "worker")
	f.compose(nil, "down", "--remove-orphans") // volumes retain independent journal and backups
	f.switchRestoreConfiguration()
	f.compose(nil, "up", "-d")
	f.runtime["api_url"] = "http://" + strings.TrimSpace(string(f.compose(nil, "port", "graph", "8080")))
	f.runtime["object_url"] = "http://" + strings.TrimSpace(string(f.compose(nil, "port", "graph", "8333")))
	f.runtime["proxy_url"] = "http://" + strings.TrimSpace(string(f.compose(nil, "port", "graph", "8335")))
	require.Eventually(t, func() bool {
		out, err := f.composeTry(nil, "exec", "-T", "graph", "/usr/local/bin/kb-runtime-entrypoint", "/usr/local/bin/kb-supervisor", "status")
		if err != nil {
			return false
		}
		var state operatorResult
		return json.Unmarshal(out, &state) == nil && state.Code == "ok" && state.Status.Maintenance.Phase == "recovering"
	}, 4*time.Minute, 2*time.Second)
	// Recovering supervisor state does not establish that Seaweed has rebuilt
	// persisted volume locations. Observe full backup reads before the one restore.
	f.waitBackupReadable(backup.Result.ID)
	restoredAt := time.Now()
	restored := f.operator("restore", "--backup-id", backup.Result.ID, "--id", "acceptance-restore")
	require.Equal(t, "ok", restored.Code)
	require.Equal(t, "complete", restored.Result.State)
	require.Less(t, time.Since(restoredAt), 4*time.Hour)
	f.waitReady()
	status, _ := f.http(originalAgent, http.MethodGet, "/v1/status", nil)
	require.Equal(t, http.StatusUnauthorized, status, "snapshot credentials must not revive")
	newAgent := f.enroll("acceptance-after-restore", "agent", "status:read,retrieval:read,evidence:read,candidate:write")
	after := f.search(newAgent)
	assertChunk(t, after, "published-remove", removed.digest, false)
	assertChunk(t, after, "published-retain", retained.digest, true)
	f.assertRestoredObjectBytes("Synthetic deletion evidence must remain absent after restore.", retained.digest)
	t.Logf("fresh graph/object restore and private acceptance elapsed=%s", time.Since(restoredAt))
	newCollector := f.enroll("acceptance-after-restore-collector", "collector", "artifacts:write")
	f.exerciseLostAcknowledgement(newCollector)
}

type compiledFixture struct {
	t         *testing.T
	ctx       context.Context
	repo, dir string
	runtime   map[string]string
	client    *http.Client
}
type publishedFixture struct{ artifactID, digest string }
type operatorResult struct {
	Code   string
	Result domain.BackupResult
	Status struct{ Maintenance domain.MaintenanceState }
}

func (f *compiledFixture) script(action string) {
	f.t.Helper()
	log, err := os.CreateTemp("", "kb-memory-fixture-"+action+"-*.log")
	require.NoError(f.t, err)
	defer func() {
		_ = log.Close()
		if !f.t.Failed() {
			_ = os.Remove(log.Name())
		}
	}()
	command := exec.CommandContext(f.ctx, filepath.Join(f.repo, "deploy/compose-smoke.sh"), action, f.dir)
	command.Stdout = log
	command.Stderr = log
	err = command.Run()
	if err != nil {
		f.logStartupFailure(log.Name(), err)
	}
	require.NoError(f.t, err, "compiled fixture failed; see private fixture log at %s", log.Name())
}
func (f *compiledFixture) composeTry(input []byte, args ...string) ([]byte, error) {
	prefix := []string{"compose", "--env-file", f.runtime["config_file"], "-p", f.runtime["compose_project"], "-f", filepath.Join(f.repo, "deploy/compose.test.yaml")}
	command := exec.CommandContext(f.ctx, "docker", append(prefix, args...)...)
	command.Stdin = bytes.NewReader(input)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	out, err := command.Output()
	if err != nil {
		_ = os.WriteFile(filepath.Join(f.dir, "last-command-error.log"), stderr.Bytes(), 0600)
	}
	return out, err
}
func (f *compiledFixture) compose(input []byte, args ...string) []byte {
	f.t.Helper()
	out, err := f.composeTry(input, args...)
	require.NoError(f.t, err, "fixture command failed; private last-command-error.log contains diagnostic")
	return out
}
func (f *compiledFixture) credential(path string) string {
	f.t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(f.t, err)
	token := strings.TrimSpace(string(b))
	require.GreaterOrEqual(f.t, len(token), 32)
	return token
}
func (f *compiledFixture) enroll(id, role, caps string) string {
	f.t.Helper()
	f.compose(nil, "exec", "-T", "graph", "/usr/local/bin/kb-runtime-entrypoint", "/bin/sh", "-c", `umask 077; exec 3>/data/kb/acceptance-credential; exec /usr/local/bin/kb-admin enroll --client-id "$1" --project fixture-project --role "$2" --capabilities "$3" --credential-fd 3`, "sh", id, role, caps)
	target := filepath.Join(f.dir, id+".credential")
	f.compose(nil, "cp", "graph:/data/kb/acceptance-credential", target)
	require.NoError(f.t, os.Chmod(target, 0600))
	f.compose(nil, "exec", "-T", "graph", "/bin/sh", "-c", "rm -f /data/kb/acceptance-credential")
	return f.credential(target)
}
func (f *compiledFixture) admin(result, input any, args ...string) {
	f.t.Helper()
	var raw []byte
	var err error
	if input != nil {
		raw, err = json.Marshal(input)
		require.NoError(f.t, err)
	}
	command := []string{"exec", "-T", "graph", "/usr/local/bin/kb-runtime-entrypoint", "/usr/local/bin/kb-admin"}
	out := f.compose(raw, append(command, args...)...)
	if result != nil {
		require.NoError(f.t, json.Unmarshal(out, result))
	}
}
func (f *compiledFixture) operator(args ...string) operatorResult {
	f.t.Helper()
	command := []string{"exec", "-T", "graph", "/usr/local/bin/kb-runtime-entrypoint", "/usr/local/bin/kb-supervisor"}
	out := f.compose(nil, append(command, args...)...)
	var result operatorResult
	require.NoError(f.t, json.Unmarshal(out, &result))
	return result
}
func (f *compiledFixture) http(token, method, path string, input any) (int, []byte) {
	f.t.Helper()
	var raw []byte
	var err error
	if input != nil {
		raw, err = json.Marshal(input)
		require.NoError(f.t, err)
	}
	req, err := http.NewRequestWithContext(f.ctx, method, f.runtime["api_url"]+path, bytes.NewReader(raw))
	require.NoError(f.t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := f.client.Do(req)
	require.NoError(f.t, err)
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	require.NoError(f.t, err)
	return response.StatusCode, body
}
func (f *compiledFixture) publish(agent, collector, id, text string) publishedFixture {
	f.t.Helper()
	digestBytes := sha256.Sum256([]byte(text))
	digest := hex.EncodeToString(digestBytes[:])
	task, session := "task-"+id, "session-"+id
	scope := handler.Scope{ProjectId: "fixture-project", TaskId: &task, SessionId: &session}
	path := "fixture.txt"
	status, raw := f.http(collector, http.MethodPost, "/v1/artifacts", handler.ArtifactInput{Bytes: int64(len(text)), ContentBase64: []byte(text), Digest: digest, PolicyRevision: "fixture-policy-1", Scope: scope, SourceLocator: id + "/source", SourcePath: &path})
	require.Equal(f.t, http.StatusCreated, status)
	var artifact handler.ArtifactResult
	require.NoError(f.t, json.Unmarshal(raw, &artifact))
	status, _ = f.http(collector, http.MethodPost, "/v1/artifacts/"+artifact.Artifact.Id+":complete", handler.CompleteArtifactInput{Digest: digest})
	require.Equal(f.t, http.StatusOK, status)
	// Isolate the authenticated grant snapshot from candidate/evidence work.
	// Failure diagnostics expose only the closed problem code, never response data.
	status, statusBody := f.http(agent, http.MethodGet, "/v1/status", nil)
	require.Equal(f.t, http.StatusOK, status, "candidate prerequisite status failed (problem_code=%s)", acceptanceProblemCode(statusBody))
	status, raw = f.http(agent, http.MethodPost, "/v1/candidates", handler.CandidateInput{ClaimId: "candidate-" + id, Scope: scope, Text: text, Kind: "finding", DependsOn: []string{}, Evidence: []handler.SubmittedEvidenceRef{{ArtifactId: artifact.Artifact.Id, Digest: digest, Locator: id + "/source", SourceRevision: "source-v1"}}})
	require.Equal(f.t, http.StatusCreated, status, "candidate submission failed (problem_code=%s)", acceptanceProblemCode(raw))
	var candidate handler.CandidateResult
	require.NoError(f.t, json.Unmarshal(raw, &candidate))
	draft := service.AdjudicationDraft{ProjectID: "fixture-project", CandidateRevision: candidate.Claim.Revision, ExpectedCandidateHead: candidate.Claim.Revision, Action: "verify", IdempotencyKey: "publish-" + id, TargetID: "published-" + id, TargetText: text, TargetKind: "finding", Excerpts: []service.ExcerptSelection{{ArtifactID: artifact.Artifact.Id, SourceRevision: "source-v1", SourceDigest: digest, Start: 0, End: int64(len(text)), ExpectedExcerptDigest: digest}}}
	var preview service.AdjudicationPreview
	f.admin(&preview, draft, "verify", "--preview")
	var published service.AdjudicationResult
	f.admin(&published, service.AdjudicationInput{AdjudicationDraft: draft, ExpectedDigest: preview.Digest}, "verify")
	require.NotEmpty(f.t, published.TargetRevision)
	return publishedFixture{artifact.Artifact.Id, digest}
}
func (f *compiledFixture) search(agent string) handler.RetrievalResult {
	f.t.Helper()
	status, raw := f.http(agent, http.MethodPost, "/v1/retrieval:search", handler.SearchInput{Scope: handler.Scope{ProjectId: "fixture-project"}})
	require.Equal(f.t, http.StatusOK, status)
	var result handler.RetrievalResult
	require.NoError(f.t, json.Unmarshal(raw, &result))
	return result
}
func assertChunk(t *testing.T, result handler.RetrievalResult, id, digest string, present bool) {
	t.Helper()
	found := false
	for _, chunk := range result.Chunks {
		if chunk.RecordId == id {
			found = true
			assert.Equal(t, handler.ClaimStatus("verified"), chunk.ClaimStatus)
			require.NotEmpty(t, chunk.Evidence)
			for _, evidence := range chunk.Evidence {
				assert.Equal(t, digest, evidence.Digest)
			}
		}
	}
	assert.Equal(t, present, found, "publication eligibility for %s", id)
}
func (f *compiledFixture) switchRestoreConfiguration() {
	f.t.Helper()
	raw, err := os.ReadFile(f.runtime["config_file"])
	require.NoError(f.t, err)
	text := string(raw)
	for _, pair := range [][2]string{{filepath.Join(f.dir, "runtime.env"), f.runtime["restore_graph_config_file"]}, {filepath.Join(f.dir, "api.env"), f.runtime["restore_api_config_file"]}, {filepath.Join(f.dir, "worker.env"), f.runtime["restore_worker_config_file"]}} {
		require.NotEmpty(f.t, pair[1])
		require.Contains(f.t, text, pair[0])
		text = strings.ReplaceAll(text, pair[0], pair[1])
	}
	require.NoError(f.t, os.WriteFile(f.runtime["config_file"], []byte(text), 0600))
}
func (f *compiledFixture) waitReady() {
	f.t.Helper()
	require.Eventually(f.t, func() bool {
		req, err := http.NewRequestWithContext(f.ctx, http.MethodGet, f.runtime["api_url"]+"/readyz", nil)
		if err != nil {
			return false
		}
		response, err := f.client.Do(req)
		if err != nil {
			return false
		}
		defer func() { _ = response.Body.Close() }()
		return response.StatusCode == http.StatusOK
	}, 3*time.Minute, time.Second)
}

func (f *compiledFixture) fixtureEnvironment(path string) map[string]string {
	f.t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(f.t, err)
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			values[key] = strings.Trim(value, "'")
		}
	}
	return values
}

func (f *compiledFixture) waitBackupReadable(backupID string) {
	f.t.Helper()
	values := f.fixtureEnvironment(filepath.Join(f.dir, "runtime.env"))
	maximum, err := strconv.ParseInt(values["KB_BACKUP_MAX_BYTES"], 10, 64)
	require.NoError(f.t, err)
	require.Positive(f.t, maximum)
	client := s3.New(s3.Options{BaseEndpoint: aws.String(f.runtime["object_url"]), Region: values["KB_BACKUP_REGION"], UsePathStyle: true, Credentials: credentials.NewStaticCredentialsProvider(values["KB_BACKUP_ACCESS_KEY_CONTENTS"], values["KB_BACKUP_SECRET_KEY_CONTENTS"], ""), HTTPClient: f.client, Retryer: aws.NopRetryer{}})
	ctx, cancel := context.WithTimeout(f.ctx, 4*time.Minute)
	defer cancel()
	require.NoError(f.t, waitForBackupReadability(ctx, client, values["KB_BACKUP_BUCKET"], values["KB_BACKUP_NAMESPACE"], backupID, maximum, time.Second), "persisted backup bodies were not readable after fixture restart")
}

// Only completion and ciphertext are external objects. manifest.json is inside
// archive.age; the real Restore retains all authentication/decryption checks.
func waitForBackupReadability(ctx context.Context, client *s3.Client, bucket, namespace, backupID string, maximum int64, interval time.Duration) error {
	prefix := "backups/" + namespace + "/" + backupID + "/"
	read := func(ctx context.Context, key string, limit int64) bool {
		object, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
		if err != nil {
			return false
		}
		if object.ContentLength == nil || *object.ContentLength < 1 || *object.ContentLength > limit {
			_ = object.Body.Close()
			return false
		}
		n, readErr := io.Copy(io.Discard, io.LimitReader(object.Body, limit+1))
		closeErr := object.Body.Close()
		return readErr == nil && closeErr == nil && n == *object.ContentLength
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		attempt, cancel := context.WithTimeout(ctx, 10*time.Second)
		ready := read(attempt, prefix+"complete.json", 16<<10) && read(attempt, prefix+"archive.age", maximum)
		cancel()
		if ready {
			return nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (f *compiledFixture) assertRestoredObjectBytes(deletedText, retainedDigest string) {
	f.t.Helper()
	values := f.fixtureEnvironment(f.runtime["restore_graph_config_file"])
	client := s3.New(s3.Options{BaseEndpoint: aws.String(f.runtime["object_url"]), Region: values["KB_S3_REGION"], UsePathStyle: true, Credentials: credentials.NewStaticCredentialsProvider(values["KB_S3_ACCESS_KEY_CONTENTS"], values["KB_S3_SECRET_KEY_CONTENTS"], ""), HTTPClient: f.client, RetryMaxAttempts: 1})
	bucket := values["KB_S3_BUCKET"]
	require.Equal(f.t, "restored-evidence", bucket)
	list, err := client.ListObjectsV2(f.ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket), MaxKeys: aws.Int32(100)})
	require.NoError(f.t, err)
	require.False(f.t, aws.ToBool(list.IsTruncated))
	require.NotEmpty(f.t, list.Contents)
	retained := false
	markers := 0
	for _, item := range list.Contents {
		object, err := client.GetObject(f.ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: item.Key})
		require.NoError(f.t, err)
		body, readErr := io.ReadAll(io.LimitReader(object.Body, (8<<20)+1))
		closeErr := object.Body.Close()
		require.NoError(f.t, readErr)
		require.NoError(f.t, closeErr)
		require.LessOrEqual(f.t, len(body), 8<<20)
		assert.NotContains(f.t, string(body), deletedText, "deleted payload cannot be restored behind graph visibility checks")
		if object.Metadata["kb-deleted"] == "1" {
			markers++
			assert.Empty(f.t, body)
		}
		digest := sha256.Sum256(body)
		if hex.EncodeToString(digest[:]) == retainedDigest {
			retained = true
		}
	}
	assert.True(f.t, retained, "retained citation bytes survive with exact digest")
	assert.Positive(f.t, markers, "live deletion replay installs permanent storage fences")
}

// Retain bounded logs outside TempDir so failed subprocess diagnostics survive
// cleanup. Runtime secret files and scoped credentials are never copied.
func (f *compiledFixture) preserveDiagnostics() {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.dir, "runtime.json"))
	if err != nil {
		return
	}
	var runtime map[string]string
	if json.Unmarshal(raw, &runtime) != nil {
		return
	}
	log, err := os.CreateTemp("", "kb-memory-acceptance-failure-*.log")
	if err != nil {
		return
	}
	defer func() { _ = log.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "compose", "--env-file", filepath.Join(f.dir, "compose.env"), "-p", runtime["compose_project"], "-f", filepath.Join(f.repo, "deploy/compose.test.yaml"), "logs", "--no-color", "--tail", "200")
	cmd.Stdout = log
	cmd.Stderr = log
	_ = cmd.Run()
	if diagnostic, err := os.ReadFile(filepath.Join(f.dir, "last-command-error.log")); err == nil {
		_, _ = log.Write(diagnostic)
	}
	f.t.Logf("protected failed-fixture diagnostics: %s", log.Name())
}

func (f *compiledFixture) waitFirstBackup() {
	f.t.Helper()
	// A fresh supervisor schedules its first backup immediately after opening.
	// Observe that real scheduled snapshot before exercising ordinary mutations.
	require.Eventually(f.t, func() bool {
		raw, err := f.composeTry(nil, "exec", "-T", "graph", "/usr/local/bin/kb-runtime-entrypoint", "/usr/local/bin/kb-supervisor", "status")
		if err != nil {
			return false
		}
		var status operatorResult
		return json.Unmarshal(raw, &status) == nil && status.Code == "ok" && status.Status.Maintenance.Phase == "open" && !status.Status.Maintenance.LastSuccessfulBackup.IsZero()
	}, 4*time.Minute, time.Second)
	f.waitReady()
}

// acceptanceProblemCode never prints raw errors, response payloads, or IDs.
func acceptanceProblemCode(raw []byte) string {
	var problem struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(raw, &problem) != nil {
		return "invalid_problem_response"
	}
	switch problem.Code {
	case "unauthorized", "forbidden", "invalid_input", "stale", "budget", "incomplete", "deleted", "unknown_outcome", "conflict", "unavailable":
		return problem.Code
	default:
		return "unrecognized_problem_code"
	}
}
