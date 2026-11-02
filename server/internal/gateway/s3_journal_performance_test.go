package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type countedJournalS3 struct {
	failPath string
	mu       sync.Mutex
	objects  map[string][]byte
	requests atomic.Int64
	failGet  atomic.Bool
	server   *httptest.Server
}

func fastJournalFixture(t *testing.T) (*S3DeletionJournal, S3DeletionJournalConfig, *countedJournalS3) {
	t.Helper()
	fixture := &countedJournalS3{objects: map[string][]byte{}}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.requests.Add(1)
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		old, exists := fixture.objects[r.URL.Path]
		etag := `"` + journalDigest(old) + `"`
		switch r.Method {
		case http.MethodPut:
			if (r.Header.Get("If-None-Match") == "*" && exists) || (r.Header.Get("If-Match") != "" && (!exists || r.Header.Get("If-Match") != etag)) {
				w.WriteHeader(http.StatusPreconditionFailed)
				_, _ = io.WriteString(w, `<Error><Code>PreconditionFailed</Code></Error>`)
				return
			}
			data, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			fixture.objects[r.URL.Path] = data
			w.Header().Set("ETag", `"`+journalDigest(data)+`"`)
		case http.MethodGet:
			if fixture.failGet.Load() || r.URL.Path == fixture.failPath {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `<Error><Code>NoSuchKey</Code></Error>`)
				return
			}
			w.Header().Set("ETag", etag)
			_, _ = w.Write(old)
		case http.MethodDelete:
			delete(fixture.objects, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(fixture.server.Close)
	cfg := journalConfig()
	cfg.Endpoint = fixture.server.URL
	g, err := NewS3DeletionJournal(cfg)
	require.NoError(t, err)
	return g, cfg, fixture
}
func TestJournalWarmOperationsUseBoundedProviderRequests(t *testing.T) {
	g, _, provider := fastJournalFixture(t)
	_, err := g.Initialize(t.Context())
	require.NoError(t, err)
	for i := range 25 {
		_, err = g.Append(t.Context(), journalRecord(t, fmt.Sprintf("operation-%d", i)))
		require.NoError(t, err)
	}
	before := provider.requests.Load()
	_, err = g.CurrentHead(t.Context())
	require.NoError(t, err)
	t.Logf("warm CurrentHead requests=%d", provider.requests.Load()-before)
	assert.LessOrEqual(t, provider.requests.Load()-before, int64(1), "fresh CurrentHead is one provider read")
	before = provider.requests.Load()
	_, err = g.Append(t.Context(), journalRecord(t, "operation-next"))
	require.NoError(t, err)
	t.Logf("warm Append requests=%d", provider.requests.Load()-before)
	assert.LessOrEqual(t, provider.requests.Load()-before, int64(9), "warm append reads only the new authenticated suffix")
}
func TestJournalLargeRestorePagesHaveLinearProviderReads(t *testing.T) {
	g, cfg, provider := fastJournalFixture(t)
	initial, err := g.Initialize(t.Context())
	require.NoError(t, err)
	const operations = 1001
	for i := range operations {
		_, err = g.Append(t.Context(), journalRecord(t, fmt.Sprintf("operation-%d", i)))
		require.NoError(t, err)
	}
	restored, err := NewS3DeletionJournal(cfg)
	require.NoError(t, err)
	before := provider.requests.Load()
	target, err := restored.CurrentHead(t.Context())
	require.NoError(t, err)
	cursor := initial
	seen := 0
	for cursor != target {
		records, next, err := restored.ReadPage(t.Context(), cursor, target, 100)
		require.NoError(t, err)
		require.NotEmpty(t, records)
		seen += len(records)
		cursor = next
	}
	assert.Equal(t, operations, seen)
	t.Logf("restore operations=%d providerRequests=%d", operations, provider.requests.Load()-before)
	assert.LessOrEqual(t, provider.requests.Load()-before, int64(operations*4+100), "restore must fetch metadata once and each selected page once")
	// Idempotency lookup is membership in this exact authenticated chain.
	before = provider.requests.Load()
	receipt, err := restored.Append(t.Context(), journalRecord(t, "operation-0"))
	require.NoError(t, err)
	assert.Equal(t, uint64(1), receipt.Sequence)
	assert.LessOrEqual(t, provider.requests.Load()-before, int64(2))
}
func TestJournalInterruptedIndexCannotTrustPartialMembership(t *testing.T) {
	g, cfg, provider := fastJournalFixture(t)
	initial, err := g.Initialize(t.Context())
	require.NoError(t, err)
	first, err := g.Append(t.Context(), journalRecord(t, "operation-1"))
	require.NoError(t, err)
	_, err = g.Append(t.Context(), journalRecord(t, "operation-2"))
	require.NoError(t, err)
	restored, err := NewS3DeletionJournal(cfg)
	require.NoError(t, err)
	head, err := restored.CurrentHead(t.Context())
	require.NoError(t, err)
	provider.mu.Lock()
	provider.failPath = "/" + cfg.Bucket + "/" + g.manifestKey(initial.Digest)
	provider.mu.Unlock()
	_, _, err = restored.ReadPage(t.Context(), initial, head, 100)
	require.Error(t, err, "index must fail after reading partial suffix but before authenticating genesis")
	// Removing a previously visited record after the failed traversal must still
	// fail reconciliation; no partial traversal has trusted operation membership.
	g.metadataMu.Lock()
	recordKey := g.links[first.Digest].body.RecordKey
	g.metadataMu.Unlock()
	path := "/" + cfg.Bucket + "/" + recordKey
	provider.mu.Lock()
	provider.failPath = ""
	recordBytes := provider.objects[path]
	delete(provider.objects, path)
	provider.mu.Unlock()
	_, err = restored.Append(t.Context(), journalRecord(t, "operation-1"))
	assert.ErrorIs(t, err, domain.ErrUnavailable)
	provider.mu.Lock()
	provider.objects[path] = recordBytes
	provider.mu.Unlock()
	records, _, err := restored.ReadPage(t.Context(), initial, head, 100)
	require.NoError(t, err)
	require.Len(t, records, 2)
}
func TestJournalCachedBranchCannotSatisfyReplayOnAnotherFreshHead(t *testing.T) {
	g, cfg, provider := fastJournalFixture(t)
	_, err := g.Initialize(t.Context())
	require.NoError(t, err)
	headPath := "/" + cfg.Bucket + "/" + g.headKey()
	provider.mu.Lock()
	genesis := append([]byte(nil), provider.objects[headPath]...)
	provider.mu.Unlock()
	_, err = g.Append(t.Context(), journalRecord(t, "orphan-operation"))
	require.NoError(t, err)
	// Simulate an independently authenticated competing branch. A cache union of
	// IDs from the old branch would incorrectly report its orphan as committed.
	provider.mu.Lock()
	provider.objects[headPath] = genesis
	provider.mu.Unlock()
	competing, err := NewS3DeletionJournal(cfg)
	require.NoError(t, err)
	winner, err := competing.Append(t.Context(), journalRecord(t, "winning-operation"))
	require.NoError(t, err)
	_, err = g.Append(t.Context(), journalRecord(t, "orphan-operation"))
	assert.ErrorIs(t, err, domain.ErrUnavailable)
	fresh, err := NewS3DeletionJournal(cfg)
	require.NoError(t, err)
	accepted, err := fresh.Append(t.Context(), journalRecord(t, "orphan-operation"))
	require.NoError(t, err)
	assert.Equal(t, winner.Sequence+1, accepted.Sequence)
}
