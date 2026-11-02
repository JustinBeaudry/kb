package platform

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCaptureObjectConfig(t *testing.T) {
	t.Setenv("KB_S3_ENDPOINT", "http://127.0.0.1:8333")
	t.Setenv("KB_S3_BUCKET", "evidence")
	t.Setenv("KB_S3_REGION", "local")
	t.Setenv("KB_S3_ACCESS_KEY", "fixture")
	t.Setenv("KB_S3_SECRET_KEY", "fixture-secret-only")
	c, err := LoadObjectConfig()
	require.NoError(t, err)
	assert.Equal(t, "evidence", c.Bucket)
	assert.NotContains(t, c.String(), "fixture-secret-only")
}
