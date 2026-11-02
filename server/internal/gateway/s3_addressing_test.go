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

type addressingHTTP func(*http.Request) (*http.Response, error)

func (f addressingHTTP) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestEvidenceS3ExplicitAddressing(t *testing.T) {
	for _, tc := range []struct{ style, host, path string }{
		{"", "storage.example", "/evidence/item"},
		{"path", "storage.example", "/evidence/item"},
		{"virtual", "evidence.storage.example", "/item"},
	} {
		t.Run(tc.style, func(t *testing.T) {
			g, err := NewS3(S3Config{Endpoint: "https://storage.example", Region: "local", Bucket: "evidence", AccessKey: "fixture", SecretKey: "fixture-secret", AddressingStyle: tc.style})
			require.NoError(t, err)
			opts := g.client.Options()
			opts.HTTPClient = addressingHTTP(func(r *http.Request) (*http.Response, error) {
				assert.Equal(t, tc.host, r.URL.Host)
				assert.Equal(t, tc.path, r.URL.Path)
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("fixture"))}, nil
			})
			g.client = s3.New(opts)
			body, err := g.Open(t.Context(), "item")
			require.NoError(t, err)
			require.NoError(t, body.Close())
		})
	}
	_, err := NewS3(S3Config{Endpoint: "https://storage.example", Region: "local", Bucket: "evidence", AccessKey: "fixture", SecretKey: "fixture-secret", AddressingStyle: "automatic"})
	require.ErrorIs(t, err, domain.ErrInvalidInput)
}
