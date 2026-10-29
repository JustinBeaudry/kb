package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestS3ErrorsDoNotExposeKeysOrSecrets(t *testing.T) {
	for _, status := range []int{403, 404, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `<Error><Code>NoSuchKey</Code><Message>secret-object-key secret-value</Message></Error>`)
			}))
			t.Cleanup(server.Close)
			g, err := NewS3(S3Config{Endpoint: server.URL, Region: "local", Bucket: "bucket", AccessKey: "fixture", SecretKey: "secret-value"})
			require.NoError(t, err)
			_, err = g.Open(t.Context(), "secret-object-key")
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "secret")
			sum := sha256.Sum256([]byte("x"))
			putErr := g.Put(t.Context(), "secret-object-key", bytes.NewBufferString("x"), hex.EncodeToString(sum[:]))
			assert.Error(t, putErr)
			assert.NotContains(t, putErr.Error(), "secret")
			assert.Error(t, g.Delete(t.Context(), "secret-object-key"))
		})
	}
}
func TestS3RejectsInvalidConfig(t *testing.T) {
	for _, cfg := range []S3Config{{}, {Endpoint: "https://user:secret@example.com", Region: "r", Bucket: "b", AccessKey: "a", SecretKey: "secret"}, {Endpoint: "http://remote.example.com", Region: "r", Bucket: "b", AccessKey: "a", SecretKey: "secret"}} {
		_, err := NewS3(cfg)
		assert.ErrorIs(t, err, domain.ErrInvalidInput)
		assert.NotContains(t, cfg.String(), "secret")
	}
}
