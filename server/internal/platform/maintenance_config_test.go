package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMaintenanceConfigRequiresProtectedSecret(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "control-key")
	secret := strings.Repeat("fixture-secret", 4)
	require.NoError(t, os.WriteFile(path, []byte(secret), 0600))
	t.Setenv("KB_MAINTENANCE_ENDPOINT", "http://graph.railway.internal:9091")
	t.Setenv("KB_MAINTENANCE_SECRET_FILE", path)
	t.Setenv("KB_INSTANCE_ID", "")
	cfg, err := LoadMaintenanceConfig()
	require.NoError(t, err)
	assert.Equal(t, secret, cfg.Secret)
	assert.NotEmpty(t, cfg.InstanceID)
	assert.NotContains(t, cfg.String(), secret)
	require.NoError(t, os.Chmod(path, 0644))
	_, err = LoadMaintenanceConfig()
	require.ErrorIs(t, err, domain.ErrInvalidInput)
	require.NoError(t, os.Chmod(path, 0600))
	link := filepath.Join(dir, "link")
	require.NoError(t, os.Symlink(path, link))
	t.Setenv("KB_MAINTENANCE_SECRET_FILE", link)
	_, err = LoadMaintenanceConfig()
	require.ErrorIs(t, err, domain.ErrInvalidInput)
	t.Setenv("KB_MAINTENANCE_SECRET_FILE", path)
	t.Setenv("KB_MAINTENANCE_ENDPOINT", "")
	_, err = LoadMaintenanceConfig()
	require.ErrorIs(t, err, domain.ErrInvalidInput)
}

func TestObjectConfigProtectedFilesRejectAmbiguousValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(path, []byte("fixture-protected-key"), 0600))
	t.Setenv("KB_S3_ACCESS_KEY", "")
	t.Setenv("KB_S3_SECRET_KEY", "")
	t.Setenv("KB_S3_ACCESS_KEY_FILE", path)
	t.Setenv("KB_S3_SECRET_KEY_FILE", path)
	cfg, err := LoadObjectConfig()
	require.NoError(t, err)
	assert.Equal(t, "fixture-protected-key", cfg.AccessKey)
	assert.Equal(t, "fixture-protected-key", cfg.SecretKey)
	t.Setenv("KB_S3_ACCESS_KEY", "ambiguous")
	_, err = LoadObjectConfig()
	require.ErrorIs(t, err, domain.ErrInvalidInput)
}
