package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

const syntheticBearer = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"

func credentialFD(t *testing.T, secret string) int {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close() })
	_, err = io.WriteString(w, secret)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	// run closes the descriptor it is given, as the inherited one in production;
	// hand it a duplicate so the test's own *os.File is closed exactly once.
	fd, err := unix.Dup(int(r.Fd()))
	require.NoError(t, err)
	return fd
}

func TestKBMCPServesStdioWithPerClientCredential(t *testing.T) {
	var auth atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"schema_version":"3","connection_state":"available","authorized_project_ids":["demo"],"authorized_capabilities":["status:read"],"capability_gaps":["recovery_unavailable"]}`)
	}))
	t.Cleanup(server.Close)
	fd := credentialFD(t, syntheticBearer+"\n")
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_status","arguments":{}}}` + "\n")
	var out, diagnostics strings.Builder

	code := run(t.Context(), []string{"--endpoint", server.URL, "--credential-fd", strconv.Itoa(fd)}, in, &out, &diagnostics)

	assert.Equal(t, 0, code, diagnostics.String())
	assert.Equal(t, "Bearer "+syntheticBearer, auth.Load())
	assert.Contains(t, out.String(), `authorized_project_ids`)
	assert.NotContains(t, out.String()+diagnostics.String(), syntheticBearer)
}

func TestKBMCPRejectsUnsafeStartup(t *testing.T) {
	for _, tc := range []struct {
		name string
		args func(t *testing.T) []string
		code int
	}{
		{"no arguments", func(*testing.T) []string { return nil }, 2},
		{"help", func(*testing.T) []string { return []string{"--help"} }, 0},
		{"standard descriptor", func(*testing.T) []string {
			return []string{"--endpoint", "https://example.test", "--credential-fd", "0"}
		}, 2},
		{"closed descriptor", func(*testing.T) []string {
			return []string{"--endpoint", "https://example.test", "--credential-fd", "987"}
		}, 1},
		{"malformed bearer", func(t *testing.T) []string {
			return []string{"--endpoint", "https://example.test", "--credential-fd", strconv.Itoa(credentialFD(t, "not-a-bearer"))}
		}, 1},
		{"oversized credential", func(t *testing.T) []string {
			return []string{"--endpoint", "https://example.test", "--credential-fd", strconv.Itoa(credentialFD(t, strings.Repeat("a", 4096)))}
		}, 1},
		{"plaintext remote origin", func(t *testing.T) []string {
			return []string{"--endpoint", "http://example.test", "--credential-fd", strconv.Itoa(credentialFD(t, syntheticBearer))}
		}, 1},
		{"extra argument", func(*testing.T) []string {
			return []string{"--endpoint", "https://example.test", "--credential-fd", "3", "extra"}
		}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, diagnostics strings.Builder

			code := run(t.Context(), tc.args(t), strings.NewReader(""), &out, &diagnostics)

			assert.Equal(t, tc.code, code)
			assert.Empty(t, out.String(), "stdout carries MCP frames only")
			assert.NotContains(t, diagnostics.String(), syntheticBearer)
			if tc.code != 0 {
				assert.Contains(t, diagnostics.String(), "kb-mcp")
			}
		})
	}
}

func TestCredentialDescriptorRule(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    os.FileMode
		nlink   uint64
		allowed bool
	}{
		{"linux socketpair from launcher", os.ModeSocket | 0o777, 1, true},
		{"darwin socketpair from launcher", os.ModeSocket | 0o666, 0, true},
		{"linux anonymous pipe", os.ModeNamedPipe | 0o600, 1, true},
		{"darwin anonymous pipe", os.ModeNamedPipe | 0o660, 0, true},
		{"owner-only regular file", 0o600, 1, true},
		{"group-readable regular file", 0o640, 1, false},
		{"world-readable named fifo", os.ModeNamedPipe | 0o644, 1, false},
		{"directory", os.ModeDir | 0o700, 2, false},
		{"character device", os.ModeDevice | os.ModeCharDevice | 0o600, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.allowed, credentialDescriptorAllowed(tc.mode, tc.nlink))
		})
	}
}

func TestKBMCPReadsCredentialFromInheritedSocketpair(t *testing.T) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	require.NoError(t, err)
	_, err = unix.Write(pair[1], []byte(syntheticBearer+"\n"))
	require.NoError(t, err)
	require.NoError(t, unix.Close(pair[1]))

	got, err := readCredential(pair[0])

	require.NoError(t, err)
	assert.Equal(t, syntheticBearer, got)
}
