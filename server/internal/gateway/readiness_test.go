package gateway

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadinessAuthenticatesStorageWithoutSourceReads(t *testing.T) {
	g, err := NewS3(S3Config{Endpoint: "https://storage.example", Region: "local", Bucket: "evidence", AccessKey: "fixture", SecretKey: "fixture-only"})
	require.NoError(t, err)
	opts := g.client.Options()
	opts.HTTPClient = addressingHTTP(func(r *http.Request) (*http.Response, error) {
		assert.Equal(t, http.MethodHead, r.Method)
		assert.Equal(t, "/evidence", r.URL.Path)
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	g.client = s3.New(opts)
	require.NoError(t, g.Probe(t.Context()))
	journal, _, _ := fastJournalFixture(t)
	_, _, err = journal.CheckHealth(t.Context())
	require.Error(t, err)
	_, err = journal.Initialize(t.Context())
	require.NoError(t, err)
	seq, capacity, err := journal.CheckHealth(t.Context())
	require.NoError(t, err)
	assert.Zero(t, seq)
	assert.Equal(t, uint64(10000), capacity)
	_, err = journal.Append(t.Context(), journalRecord(t, "health-record"))
	require.NoError(t, err)
	seq, _, err = journal.CheckHealth(t.Context())
	require.NoError(t, err)
	assert.Equal(t, uint64(1), seq)
	assert.NotErrorIs(t, err, domain.ErrUnavailable)
}
