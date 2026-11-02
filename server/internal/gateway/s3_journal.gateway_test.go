package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const journalS3Image = "chrislusf/seaweedfs:4.51@sha256:f7a49e3f3972ebed5be2ed4befa4a0c68eb7bdddba6e692d42729ceb545134b8"

func journalFixture(t *testing.T) (*S3DeletionJournal, S3DeletionJournalConfig) {
	t.Helper()
	c, err := testcontainers.GenericContainer(t.Context(), testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: journalS3Image, ExposedPorts: []string{"8333/tcp"}, Cmd: []string{"mini", "-dir=/data", "-ip.bind=0.0.0.0", "-master.telemetry=false"}, Env: map[string]string{"AWS_ACCESS_KEY_ID": "journal-fixture", "AWS_SECRET_ACCESS_KEY": "journal-fixture-secret", "S3_BUCKET": "journal"}, WaitingFor: wait.ForListeningPort("8333/tcp").WithStartupTimeout(90 * time.Second)}, Started: true})
	if c != nil {
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			require.NoError(t, c.Terminate(ctx))
		})
	}
	require.NoError(t, err, "real independent S3 fixture required")
	host, err := c.Host(t.Context())
	require.NoError(t, err)
	port, err := c.MappedPort(t.Context(), "8333/tcp")
	require.NoError(t, err)
	cfg := journalConfig()
	cfg.Endpoint = "http://" + host + ":" + port.Port()
	g, err := NewS3DeletionJournal(cfg)
	require.NoError(t, err)
	return g, cfg
}
func journalConfig() S3DeletionJournalConfig {
	var key [32]byte
	copy(key[:], "fixture-only-32-byte-journal-key!!")
	return S3DeletionJournalConfig{Endpoint: "https://journal.example.test", Region: "local", Bucket: "journal", Prefix: "deletions", Namespace: "namespace-1", AccessKey: "journal-fixture", SecretKey: "journal-fixture-secret", KeyID: "key-1", Keys: map[string][32]byte{"key-1": key}, AddressingStyle: "path"}
}
func journalRecord(t *testing.T, operation string) domain.JournalRecord {
	t.Helper()
	m := domain.DeletionManifest{ProjectID: "project-1", IDs: []string{"claim-1"}, SourceDigests: []string{fmt.Sprintf("%064x", 1)}, Artifacts: []domain.DeletionArtifact{{ID: "artifact-1", Generation: "generation-1", Digest: fmt.Sprintf("%064x", 2)}}}
	b, err := json.Marshal(m)
	require.NoError(t, err)
	sum := sha256.Sum256(b)
	return domain.JournalRecord{Version: 1, OperationID: operation, Action: "delete", Manifest: m, ManifestDigest: hex.EncodeToString(sum[:]), CreatedAt: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)}
}
func TestJournalRequiresExplicitInitializationAndReplaysCompleteManifest(t *testing.T) {
	g, cfg := journalFixture(t)
	_, err := g.CurrentHead(t.Context())
	require.ErrorIs(t, err, domain.ErrUnavailable)
	initial, err := g.Initialize(t.Context())
	require.NoError(t, err)
	assert.Zero(t, initial.Sequence)
	empty, nextInitial, err := g.ReadPage(t.Context(), domain.JournalBarrier{Namespace: initial.Namespace}, initial, 1)
	require.NoError(t, err)
	assert.Empty(t, empty)
	assert.Equal(t, initial, nextInitial)
	_, err = g.Initialize(t.Context())
	assert.ErrorIs(t, err, domain.ErrConflict)
	record := journalRecord(t, "operation-1")
	record.Manifest.ProjectID = "项目/feature"
	record.Manifest.IDs = append(record.Manifest.IDs, "feature/foo", strings.Repeat("x", 8192))
	record.Manifest.Tasks = []string{"tâche/日本語"}
	manifestJSON, err := json.Marshal(record.Manifest)
	require.NoError(t, err)
	record.ManifestDigest = journalDigest(manifestJSON)
	barrier, err := g.Append(t.Context(), record)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), barrier.Sequence)
	restored, err := NewS3DeletionJournal(cfg)
	require.NoError(t, err)
	records, err := restored.ReadSince(t.Context(), initial)
	require.NoError(t, err)
	assert.Equal(t, []domain.JournalRecord{record}, records)
	require.NoError(t, restored.VerifyContinuity(t.Context(), initial, barrier))
	genesisRecords, err := restored.ReadSince(t.Context(), domain.JournalBarrier{Namespace: initial.Namespace})
	require.NoError(t, err)
	assert.Equal(t, []domain.JournalRecord{record}, genesisRecords)
	replay, err := restored.Append(t.Context(), record)
	require.NoError(t, err)
	assert.Equal(t, barrier, replay)
	record.CreatedAt = record.CreatedAt.Add(time.Second)
	_, err = restored.Append(t.Context(), record)
	assert.ErrorIs(t, err, domain.ErrConflict)
	final, err := restored.Append(t.Context(), journalRecord(t, "operation-2"))
	require.NoError(t, err)
	page, next, err := restored.ReadPage(t.Context(), initial, final, 1)
	require.NoError(t, err)
	require.Len(t, page, 1)
	assert.Equal(t, "operation-1", page[0].OperationID)
	assert.Equal(t, barrier, next)
	page, next, err = restored.ReadPage(t.Context(), next, final, 1)
	require.NoError(t, err)
	require.Len(t, page, 1)
	assert.Equal(t, "operation-2", page[0].OperationID)
	assert.Equal(t, final, next)
	_, _, err = restored.ReadPage(t.Context(), initial, barrier, 1)
	assert.ErrorIs(t, err, domain.ErrStale)
	counter := &countJournalRequests{base: http.DefaultTransport}
	options := restored.client.Options()
	options.HTTPClient = &http.Client{Transport: counter}
	restored.client = s3.New(options)
	_, err = restored.CurrentHead(t.Context())
	require.NoError(t, err)
	assert.Equal(t, int64(1), counter.requests.Load(), "actual S3 current head requires one GET")
	before := counter.requests.Load()
	_, err = restored.Append(t.Context(), journalRecord(t, "operation-3"))
	require.NoError(t, err)
	assert.LessOrEqual(t, counter.requests.Load()-before, int64(9), "actual S3 warmed append is constant I/O")

}
func TestJournalConcurrentConditionalWritersPreserveEveryOperation(t *testing.T) {
	g, cfg := journalFixture(t)
	initial, err := g.Initialize(t.Context())
	require.NoError(t, err)
	const writers = 8
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			other, e := NewS3DeletionJournal(cfg)
			if e == nil {
				_, e = other.Append(t.Context(), journalRecord(t, fmt.Sprintf("operation-%d", i)))
			}
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	records, err := g.ReadSince(t.Context(), initial)
	require.NoError(t, err)
	assert.Len(t, records, writers)
	head, err := g.CurrentHead(t.Context())
	require.NoError(t, err)
	assert.Equal(t, uint64(writers), head.Sequence)
	_, err = g.client.PutObject(t.Context(), &s3.PutObjectInput{Bucket: aws.String(cfg.Bucket), Key: aws.String(g.headKey()), Body: bytes.NewReader([]byte("forged")), IfMatch: aws.String(`"stale-etag"`)})
	require.Error(t, err)
	fresh, err := g.CurrentHead(t.Context())
	require.NoError(t, err)
	assert.Equal(t, head, fresh)
}
func TestJournalRejectsTamperedEnvelope(t *testing.T) {
	g, _ := journalFixture(t)
	_, err := g.Initialize(t.Context())
	require.NoError(t, err)
	_, err = g.Append(t.Context(), journalRecord(t, "operation-1"))
	require.NoError(t, err)
	out, err := g.client.GetObject(t.Context(), &s3.GetObjectInput{Bucket: aws.String(g.bucket), Key: aws.String(g.headKey())})
	require.NoError(t, err)
	body, err := io.ReadAll(out.Body)
	require.NoError(t, err)
	require.NoError(t, out.Body.Close())
	assert.NotContains(t, string(body), "claim-1")
	assert.NotContains(t, string(body), "operation-1")
	body[len(body)-1] ^= 1
	_, err = g.client.PutObject(t.Context(), &s3.PutObjectInput{Bucket: aws.String(g.bucket), Key: aws.String(g.headKey()), Body: bytes.NewReader(body)})
	require.NoError(t, err)
	_, err = g.CurrentHead(t.Context())
	assert.ErrorIs(t, err, domain.ErrUnavailable)
}
func TestJournalRejectsUnsafeConfiguration(t *testing.T) {
	for _, mutate := range []func(*S3DeletionJournalConfig){func(c *S3DeletionJournalConfig) { c.Endpoint = "http://remote.example.test" }, func(c *S3DeletionJournalConfig) { c.Namespace = "../escape" }, func(c *S3DeletionJournalConfig) { c.Keys = nil }, func(c *S3DeletionJournalConfig) { c.AddressingStyle = "" }, func(c *S3DeletionJournalConfig) { c.Prefix = "../escape" }, func(c *S3DeletionJournalConfig) { c.Endpoint = "https://user:secret@journal.example.test" }} {
		cfg := journalConfig()
		mutate(&cfg)
		_, err := NewS3DeletionJournal(cfg)
		assert.ErrorIs(t, err, domain.ErrInvalidInput)
	}
	cfg := journalConfig()
	assert.NotContains(t, fmt.Sprintf("%+v %#v", cfg, cfg), "journal-fixture-secret")
}

func TestJournalAuthenticatesNamespaceKeyAndWriter(t *testing.T) {
	cfg := journalConfig()
	g, err := NewS3DeletionJournal(cfg)
	require.NoError(t, err)
	raw, err := g.seal(journalBody{Kind: "manifest"})
	require.NoError(t, err)
	for _, tc := range []struct {
		name   string
		change func(*S3DeletionJournal, []byte) []byte
	}{
		{"namespace", func(other *S3DeletionJournal, b []byte) []byte { other.namespace = "other-namespace"; return b }},
		{"forged-writer", func(other *S3DeletionJournal, b []byte) []byte { other.keys["key-1"] = [32]byte{1}; return b }},
		{"unknown-key", func(other *S3DeletionJournal, b []byte) []byte { delete(other.keys, "key-1"); return b }},
		{"substituted-key-id", func(other *S3DeletionJournal, b []byte) []byte {
			other.keys["key-2"] = other.keys["key-1"]
			return bytes.Replace(b, []byte(`"KeyID":"key-1"`), []byte(`"KeyID":"key-2"`), 1)
		}},
		{"truncated", func(_ *S3DeletionJournal, b []byte) []byte { return b[:len(b)/2] }},
		{"oversized", func(_ *S3DeletionJournal, _ []byte) []byte { return bytes.Repeat([]byte("x"), journalMaxEnvelope+1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			other, err := NewS3DeletionJournal(cfg)
			require.NoError(t, err)
			_, err = other.open(tc.change(other, bytes.Clone(raw)), "manifest")
			assert.ErrorIs(t, err, domain.ErrUnavailable)
		})
	}
}
func TestJournalUnknownAppendOutcomeIsIdempotent(t *testing.T) {
	g, cfg := journalFixture(t)
	initial, err := g.Initialize(t.Context())
	require.NoError(t, err)
	options := g.client.Options()
	options.HTTPClient = &http.Client{Transport: &loseJournalHeadResponse{base: http.DefaultTransport, headPath: "/" + cfg.Bucket + "/" + g.headKey()}}
	g.client = s3.New(options)
	record := journalRecord(t, "operation-unknown")
	_, err = g.Append(t.Context(), record)
	require.ErrorIs(t, err, domain.ErrUnknownOutcome)
	restarted, err := NewS3DeletionJournal(cfg)
	require.NoError(t, err)
	barrier, err := restarted.Append(t.Context(), record)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), barrier.Sequence)
	records, err := restarted.ReadSince(t.Context(), initial)
	require.NoError(t, err)
	assert.Equal(t, []domain.JournalRecord{record}, records)
}

type loseJournalHeadResponse struct {
	base     http.RoundTripper
	headPath string
}

func (r *loseJournalHeadResponse) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := r.base.RoundTrip(req)
	if err == nil && req.Method == http.MethodPut && req.URL.Path == r.headPath && response.StatusCode < 300 {
		_ = response.Body.Close()
		return nil, errors.New("fixture lost acknowledgement")
	}
	return response, err
}
func TestJournalRejectsBrokenContinuityAndStaleExpectedHead(t *testing.T) {
	g, cfg := journalFixture(t)
	initial, err := g.Initialize(t.Context())
	require.NoError(t, err)
	one, err := g.Append(t.Context(), journalRecord(t, "operation-one"))
	require.NoError(t, err)
	two, err := g.Append(t.Context(), journalRecord(t, "operation-two"))
	require.NoError(t, err)
	assert.ErrorIs(t, g.VerifyContinuity(t.Context(), initial, one), domain.ErrStale)
	require.NoError(t, g.VerifyContinuity(t.Context(), initial, two))
	_, err = g.client.DeleteObject(t.Context(), &s3.DeleteObjectInput{Bucket: aws.String(g.bucket), Key: aws.String(g.manifestKey(one.Digest))})
	require.NoError(t, err)
	restarted, err := NewS3DeletionJournal(cfg)
	require.NoError(t, err)
	// CurrentHead establishes latestness; fresh continuity proves history.
	latest, err := restarted.CurrentHead(t.Context())
	require.NoError(t, err)
	assert.Equal(t, two, latest)
	_, err = restarted.ReadSince(t.Context(), initial)
	assert.ErrorIs(t, err, domain.ErrUnavailable)
	assert.ErrorIs(t, restarted.VerifyContinuity(t.Context(), initial, two), domain.ErrUnavailable)
}

func TestJournalFailsClosedWhenProviderIgnoresConditionalWrites(t *testing.T) {
	var mu sync.Mutex
	objects := map[string][]byte{}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPut {
			data, _ := io.ReadAll(r.Body)
			objects[r.URL.Path] = data
			w.Header().Set("ETag", `"`+journalDigest(data)+`"`)
			w.WriteHeader(http.StatusOK)
			return
		}
		if data, ok := objects[r.URL.Path]; ok {
			w.Header().Set("ETag", `"`+journalDigest(data)+`"`)
			_, _ = w.Write(data)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer fixture.Close()
	cfg := journalConfig()
	cfg.Endpoint = fixture.URL
	g, err := NewS3DeletionJournal(cfg)
	require.NoError(t, err)
	_, err = g.Initialize(t.Context())
	assert.ErrorIs(t, err, domain.ErrUnavailable)
}

func TestJournalContainsNoSourceProse(t *testing.T) {
	g, err := NewS3DeletionJournal(journalConfig())
	require.NoError(t, err)
	record := journalRecord(t, "operation-1")
	raw, err := g.seal(journalBody{Kind: "record", Sequence: 1, PreviousManifestDigest: fmt.Sprintf("%064x", 1), Record: &record})
	require.NoError(t, err)
	for _, identity := range []string{"claim-1", "artifact-1", "project-1", "operation-1", "generation-1"} {
		assert.NotContains(t, string(raw), identity)
	}
	body, err := g.open(raw, "record")
	require.NoError(t, err)
	assert.Equal(t, record, *body.Record)
}

func TestJournalRejectsUnboundedAndInvalidMetadataBeforeIO(t *testing.T) {
	g, err := NewS3DeletionJournal(journalConfig())
	require.NoError(t, err)
	for _, tc := range []struct {
		name   string
		mutate func(*domain.JournalRecord)
		want   error
	}{
		{"serialized-record", func(r *domain.JournalRecord) {
			r.Manifest.IDs = []string{strings.Repeat("i", domain.MaxJournalRecordBytes)}
		}, domain.ErrBudget},
		{"request-digest", func(r *domain.JournalRecord) { r.RequestDigest = "source prose" }, domain.ErrInvalidInput},
		{"action", func(r *domain.JournalRecord) { r.Action = "undelete" }, domain.ErrInvalidInput},
		{"operation", func(r *domain.JournalRecord) { r.OperationID = "../path" }, domain.ErrInvalidInput},
		{"manifest-digest", func(r *domain.JournalRecord) { r.ManifestDigest = fmt.Sprintf("%064x", 9) }, domain.ErrInvalidInput},
		{"source-digest", func(r *domain.JournalRecord) { r.Manifest.SourceDigests = []string{"not-a-digest"} }, domain.ErrInvalidInput},
		{"entries", func(r *domain.JournalRecord) { r.Manifest.IDs = make([]string, domain.MaxDeletionEntries+1) }, domain.ErrBudget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := journalRecord(t, "operation-1")
			tc.mutate(&record)
			_, err := g.Append(t.Context(), record)
			assert.ErrorIs(t, err, tc.want)
		})
	}
}

type countJournalRequests struct {
	base     http.RoundTripper
	requests atomic.Int64
}

func (r *countJournalRequests) RoundTrip(req *http.Request) (*http.Response, error) {
	r.requests.Add(1)
	return r.base.RoundTrip(req)
}
