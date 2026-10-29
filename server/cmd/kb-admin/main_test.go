package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHelpDescribesProtectedOneTimeSetup(t *testing.T) {
	var output bytes.Buffer
	require.NoError(t, run(t.Context(), []string{"help"}, &output))
	for _, want := range []string{"controlled service host", "public signing identity", "second setup", "credential-fd", "recovery_unavailable", "No owner bearer"} {
		assert.Contains(t, output.String(), want)
	}
	assert.NotContains(t, output.String(), "--token")
}
func TestProtectedCredentialDescriptor(t *testing.T) {
	for _, fd := range []int{0, 1, 2, -1, 999999} {
		t.Run(fmt.Sprintf("fd-%d", fd), func(t *testing.T) {
			_, err := protectedDescriptor(t.Context(), fd)
			assert.ErrorIs(t, err, domain.ErrInvalidInput)
		})
	}
	f, err := os.OpenFile(filepath.Join(t.TempDir(), "credential"), os.O_CREATE|os.O_RDWR, 0600)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, f.Close()) })
	protected, err := protectedDescriptor(t.Context(), int(f.Fd()))
	require.NoError(t, err)
	require.NoError(t, protected.Close())
	require.NoError(t, f.Chmod(0644))
	_, err = protectedDescriptor(t.Context(), int(f.Fd()))
	assert.ErrorIs(t, err, domain.ErrInvalidInput)
}
func TestAdminRejectsUnsupportedAndSecretArguments(t *testing.T) {
	for _, args := range [][]string{{"recover"}, {"remote"}, {"setup", "--token", "fixture-secret"}, {"setup", "--credential-fd", "1"}} {
		var out bytes.Buffer
		err := run(t.Context(), args, &out)
		require.Error(t, err)
		assert.Empty(t, out.String())
		assert.NotContains(t, safeError(err), "fixture-secret")
	}
}

func TestReadOnlyCredentialDestinationRejectedBeforeSetup(t *testing.T) {
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	file, err := protectedDescriptor(t.Context(), int(reader.Fd()))
	if file != nil {
		_ = file.Close()
	}
	assert.ErrorIs(t, err, domain.ErrInvalidInput)
}

func TestPublicNamedPipeRejectedBeforeSetup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential-pipe")
	require.NoError(t, syscall.Mkfifo(path, 0600))
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = file.Close() })
	require.NoError(t, file.Chmod(0666))
	protected, err := protectedDescriptor(t.Context(), int(file.Fd()))
	if protected != nil {
		_ = protected.Close()
	}
	assert.ErrorIs(t, err, domain.ErrInvalidInput)
}

func TestPrivateInheritedPipeAccepted(t *testing.T) {
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	protected, err := protectedDescriptor(t.Context(), int(writer.Fd()))
	require.NoError(t, err)
	require.NoError(t, protected.Close())
}
