package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func outcomeClient(t *testing.T, f func(*http.Request) (*http.Response, error)) *s3.Client {
	t.Helper()
	g, err := NewS3(S3Config{Endpoint: "https://storage.example", Region: "local", Bucket: "evidence", AccessKey: "fixture", SecretKey: "fixture"})
	require.NoError(t, err)
	opts := g.client.Options()
	opts.HTTPClient = addressingHTTP(f)
	return s3.New(opts)
}
func outcomeResponse(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: http.Header{"Etag": []string{`"fixture"`}}, Body: io.NopCloser(strings.NewReader(body))}
}
func TestMutationSDKCannotRetryLostAcknowledgementIntoKnownConflict(t *testing.T) {
	attempts := 0
	client := outcomeClient(t, func(*http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return nil, errors.New("lost acknowledgement")
		}
		return outcomeResponse(412, `<Error><Code>PreconditionFailed</Code></Error>`), nil
	})
	ctx := domain.WithOperationOutcome(t.Context())
	_, err := putObject(ctx, client, &s3.PutObjectInput{Bucket: aws.String("evidence"), Key: aws.String("item"), Body: strings.NewReader("body")})
	require.Error(t, err)
	assert.Equal(t, 1, attempts)
	assert.False(t, domain.OperationSettled(ctx))
	_, err = putObject(ctx, client, &s3.PutObjectInput{Bucket: aws.String("evidence"), Key: aws.String("item"), Body: strings.NewReader("body")})
	assert.True(t, conditionalConflict(err))
	assert.False(t, domain.OperationSettled(ctx))
}
func TestJournalReadbackDoesNotClearUnknownImmutablePut(t *testing.T) {
	client := outcomeClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPut {
			return nil, errors.New("lost ack")
		}
		return outcomeResponse(200, "same body"), nil
	})
	journal := &S3DeletionJournal{client: client, bucket: "journal"}
	ctx := domain.WithOperationOutcome(t.Context())
	require.NoError(t, journal.immutable(ctx, "entry", []byte("same body")))
	assert.False(t, domain.OperationSettled(ctx))
}
func TestQualificationCleanupTracksPhysicalDeleteFailure(t *testing.T) {
	ctx := domain.WithOperationOutcome(t.Context())
	calls := 0
	client := outcomeClient(t, func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("lost delete ack") })
	deleteQualificationObject(ctx, client, &s3.DeleteObjectInput{Bucket: aws.String("evidence"), Key: aws.String("qualification")})
	assert.Equal(t, 1, calls)
	assert.False(t, domain.OperationSettled(ctx))
}
func TestCanceledBeforeMutationDispatchIsSettled(t *testing.T) {
	ctx, cancel := context.WithCancel(domain.WithOperationOutcome(t.Context()))
	cancel()
	client := outcomeClient(t, func(*http.Request) (*http.Response, error) { t.Fatal("dispatched canceled mutation"); return nil, nil })
	_, err := putObject(ctx, client, &s3.PutObjectInput{})
	require.ErrorIs(t, err, context.Canceled)
	assert.True(t, domain.OperationSettled(ctx))
}

func TestEvidencePhysicalLostAcknowledgementRemainsUncertain(t *testing.T) {
	var attempts atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if attempts.Add(1) == 1 {
			// Provider accepted the complete write, but its response is lost.
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
			return
		}
		w.WriteHeader(http.StatusPreconditionFailed)
		_, _ = io.WriteString(w, `<Error><Code>PreconditionFailed</Code></Error>`)
	}))
	t.Cleanup(provider.Close)
	g, err := NewS3(S3Config{Endpoint: provider.URL, Region: "local", Bucket: "evidence", AccessKey: "fixture", SecretKey: "fixture"})
	require.NoError(t, err)
	g.qualified = true
	ctx := domain.WithOperationOutcome(t.Context())
	sum := sha256.Sum256([]byte("body"))
	require.ErrorIs(t, g.Put(ctx, "item", strings.NewReader("body"), hex.EncodeToString(sum[:])), domain.ErrUnavailable)
	assert.Equal(t, int32(1), attempts.Load())
	assert.False(t, domain.OperationSettled(ctx))
}
