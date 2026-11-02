package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type delayedEvidencePut struct {
	base             aws.HTTPClient
	entered, release chan struct{}
	once             sync.Once
	trace            func(string, ...any)
}

func (c *delayedEvidencePut) Do(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodPut && r.URL.Path == "/journal/late-upload" && r.Header.Get("X-Amz-Meta-Kb-Deleted") == "" {
		c.once.Do(func() { close(c.entered) })
		select {
		case <-c.release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	}
	out, err := c.base.Do(r)
	if c.trace != nil {
		status := 0
		if out != nil {
			status = out.StatusCode
		}
		c.trace("fixture request method=%s conditional=%s marker=%s status=%d transportError=%t", r.Method, r.Header.Get("If-None-Match"), r.Header.Get("X-Amz-Meta-Kb-Deleted"), status, err != nil)
	}
	return out, err
}
func TestEvidenceDeletionFencesLateAndRepeatedUploads(t *testing.T) {
	_, cfg := journalFixture(t)
	g, err := NewS3(S3Config{Endpoint: cfg.Endpoint, Region: cfg.Region, Bucket: cfg.Bucket, AccessKey: cfg.AccessKey, SecretKey: cfg.SecretKey})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		_, err := g.client.HeadBucket(t.Context(), &s3.HeadBucketInput{Bucket: aws.String(cfg.Bucket)})
		return err == nil
	}, 10*time.Second, 100*time.Millisecond)
	original := g.client
	delayed := &delayedEvidencePut{base: original.Options().HTTPClient, entered: make(chan struct{}), release: make(chan struct{}), trace: t.Logf}
	options := original.Options()
	options.HTTPClient = delayed
	g.client = s3.New(options)
	data := []byte("synthetic late private source")
	sum := sha256.Sum256(data)
	result := make(chan error, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	go func() { result <- g.Put(ctx, "late-upload", bytes.NewReader(data), hex.EncodeToString(sum[:])) }()
	select {
	case <-delayed.entered:
	case err := <-result:
		t.Fatalf("upload failed before delayed request: %v", err)
	case <-ctx.Done():
		t.Fatal("upload did not reach fixture barrier")
	}
	require.NoError(t, g.Delete(t.Context(), "late-upload"))
	close(delayed.release)
	assert.ErrorIs(t, <-result, domain.ErrDeleted)
	require.NoError(t, g.Delete(t.Context(), "late-upload"))
	_, err = g.Open(t.Context(), "late-upload")
	assert.ErrorIs(t, err, domain.ErrIncomplete)
	// The durable fence contains no source bytes; it is not a hidden retained copy.
	raw, err := original.GetObject(t.Context(), &s3.GetObjectInput{Bucket: aws.String(cfg.Bucket), Key: aws.String("late-upload")})
	require.NoError(t, err)
	body, err := io.ReadAll(raw.Body)
	require.NoError(t, err)
	require.NoError(t, raw.Body.Close())
	assert.Empty(t, body)
	assert.Equal(t, "1", raw.Metadata["kb-deleted"])
	assert.ErrorIs(t, g.Put(t.Context(), "late-upload", bytes.NewReader(data), hex.EncodeToString(sum[:])), domain.ErrDeleted)
	t.Run("exact retries never overwrite", func(t *testing.T) {
		require.NoError(t, g.Put(t.Context(), "immutable", bytes.NewReader(data), hex.EncodeToString(sum[:])))
		require.NoError(t, g.Put(t.Context(), "immutable", bytes.NewReader(data), hex.EncodeToString(sum[:])))
		changed := []byte("changed")
		hash := sha256.Sum256(changed)
		assert.ErrorIs(t, g.Put(t.Context(), "immutable", bytes.NewReader(changed), hex.EncodeToString(hash[:])), domain.ErrConflict)
	})
	t.Run("empty evidence cannot impersonate deletion marker", func(t *testing.T) {
		emptyHash := sha256.Sum256(nil)
		require.NoError(t, g.Put(t.Context(), "empty", bytes.NewReader(nil), hex.EncodeToString(emptyHash[:])))
		require.NoError(t, g.Delete(t.Context(), "empty"))
		assert.ErrorIs(t, g.Put(t.Context(), "empty", bytes.NewReader(nil), hex.EncodeToString(emptyHash[:])), domain.ErrDeleted)
	})
	t.Run("unknown marker write never acknowledges deletion", func(t *testing.T) {
		previous := g.client
		options := previous.Options()
		options.RetryMaxAttempts = 1
		options.HTTPClient = &unknownEvidenceMarkerAck{base: original.Options().HTTPClient, remaining: 1}
		g.client = s3.New(options)
		t.Cleanup(func() { g.client = previous })
		assert.ErrorIs(t, g.Delete(t.Context(), "unknown-marker"), domain.ErrUnavailable)
		_, err := g.Open(t.Context(), "unknown-marker")
		assert.ErrorIs(t, err, domain.ErrIncomplete)
		require.NoError(t, g.Delete(t.Context(), "unknown-marker"))
	})
}

type unknownEvidenceMarkerAck struct {
	base      aws.HTTPClient
	remaining int
}

func (c *unknownEvidenceMarkerAck) Do(r *http.Request) (*http.Response, error) {
	out, err := c.base.Do(r)
	if err == nil && r.Method == http.MethodPut && r.Header.Get("X-Amz-Meta-Kb-Deleted") == "1" && c.remaining > 0 {
		c.remaining--
		_ = out.Body.Close()
		return nil, errors.New("synthetic lost marker acknowledgment")
	}
	return out, err
}

func TestEvidenceFenceRejectsIgnoredCreatePreconditions(t *testing.T) {
	var touched atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bucket/evidence-key" {
			touched.Store(true)
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("ETag", `"ignored-conditional-fixture"`)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	g, err := NewS3(S3Config{Endpoint: server.URL, Region: "local", Bucket: "bucket", AccessKey: "fixture", SecretKey: "fixture-only"})
	require.NoError(t, err)
	sum := sha256.Sum256([]byte("value"))
	assert.ErrorIs(t, g.Put(t.Context(), "evidence-key", bytes.NewReader([]byte("value")), hex.EncodeToString(sum[:])), domain.ErrUnavailable)
	assert.ErrorIs(t, g.Delete(t.Context(), "evidence-key"), domain.ErrUnavailable)
	assert.False(t, touched.Load())
}
