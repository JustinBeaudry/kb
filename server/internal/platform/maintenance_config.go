package platform

import (
	"crypto/rand"
	"io"
	"os"
	"path/filepath"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"golang.org/x/sys/unix"
)

type MaintenanceConfig struct{ Endpoint, Secret, InstanceID string }

func (MaintenanceConfig) String() string   { return "MaintenanceConfig([redacted])" }
func (MaintenanceConfig) GoString() string { return "MaintenanceConfig([redacted])" }

func LoadMaintenanceConfig() (MaintenanceConfig, error) {
	c := MaintenanceConfig{Endpoint: os.Getenv("KB_MAINTENANCE_ENDPOINT"), InstanceID: os.Getenv("KB_INSTANCE_ID")}
	secret, err := ReadProtectedSecret(os.Getenv("KB_MAINTENANCE_SECRET_FILE"))
	if err != nil {
		return MaintenanceConfig{}, err
	}
	c.Secret = secret
	if c.InstanceID == "" {
		c.InstanceID = rand.Text()
	}
	if _, err = gateway.NewMaintenanceHTTPClient(c.Endpoint, c.Secret, c.InstanceID); err != nil {
		return MaintenanceConfig{}, err
	}
	return c, nil
}

// ReadProtectedSecret reads a bounded current-owner-only regular file without
// following a final symlink. Values are never included in returned errors.
func ReadProtectedSecret(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", domain.ErrInvalidInput
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", domain.ErrInvalidInput
	}
	file := os.NewFile(uintptr(fd), "protected-secret")
	defer func() { _ = file.Close() }()
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0077 != 0 || stat.Uid != uint32(os.Geteuid()) || stat.Size < 1 || stat.Size > 4096 {
		return "", domain.ErrInvalidInput
	}
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(data) < 1 || len(data) > 4096 {
		return "", domain.ErrInvalidInput
	}
	return string(data), nil
}

func secretSetting(name string) (string, error) {
	value, path := os.Getenv(name), os.Getenv(name+"_FILE")
	if path == "" {
		return value, nil
	}
	if value != "" {
		return "", domain.ErrInvalidInput
	}
	return ReadProtectedSecret(path)
}
