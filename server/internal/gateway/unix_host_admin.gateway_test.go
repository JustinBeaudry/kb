package gateway_test

import (
	"context"
	"encoding/binary"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type localHostRunner struct{}

func (localHostRunner) Run(_ context.Context, r domain.HostAdminRequest, sink *os.File) (domain.HostAdminResult, error) {
	if sink != nil {
		_, err := sink.Write([]byte("fixture-secret"))
		if err != nil {
			return domain.HostAdminResult{}, err
		}
	}
	return domain.HostAdminResult{RequestID: r.RequestID, Code: "ok", Stage: "completed", Completed: true, ResultJSON: []byte(`{"ok":true}`)}, nil
}
func TestProtectedHostUnixRoundTripKeepsBearerOnDescriptor(t *testing.T) {
	dir, err := privateHostSocketDir(t)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0700))
	path := filepath.Join(dir, "admin.sock")
	server, err := gateway.NewUnixHostAdminServer(path, os.Geteuid(), localHostRunner{})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	t.Cleanup(func() { _ = server.Close(); require.NoError(t, <-done) })
	client, err := gateway.NewUnixHostAdminClient(path, os.Geteuid())
	require.NoError(t, err)
	sink, err := os.CreateTemp(dir, "credential")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sink.Close() })
	result, err := client.Run(ctx, domain.HostAdminRequest{Version: 1, RequestID: "one", Action: "enroll", Options: domain.HostAdminOptions{ProjectID: "project"}}, sink)
	require.NoError(t, err)
	assert.NotContains(t, string(result.ResultJSON), "fixture-secret")
	_, err = sink.Seek(0, 0)
	require.NoError(t, err)
	data := make([]byte, 32)
	n, err := sink.Read(data)
	require.NoError(t, err)
	assert.Equal(t, "fixture-secret", string(data[:n]))
}
func TestProtectedHostUnixRejectsSymlinkPublicDirectoryAndExistingSocket(t *testing.T) {
	dir, err := privateHostSocketDir(t)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0700))
	path := filepath.Join(dir, "admin.sock")
	listener, err := gateway.ListenProtectedUnix(path, os.Geteuid())
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	_, err = gateway.ListenProtectedUnix(path, os.Geteuid())
	require.Error(t, err)
	link := filepath.Join(dir, "link")
	require.NoError(t, os.Symlink(path, link))
	_, err = gateway.NewUnixHostAdminClient(link, os.Geteuid())
	require.Error(t, err)
	require.NoError(t, os.Chmod(dir, 0755))
	_, err = gateway.NewUnixHostAdminClient(path, os.Geteuid())
	require.Error(t, err)
}
func TestProtectedHostRejectsWrongPeerUID(t *testing.T) {
	dir, err := privateHostSocketDir(t)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0700))
	path := filepath.Join(dir, "admin.sock")
	listener, err := gateway.ListenProtectedUnix(path, os.Geteuid())
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	done := make(chan error, 1)
	go func() {
		conn, err := listener.AcceptUnix()
		if err == nil {
			defer func() { _ = conn.Close() }()
			err = gateway.ValidateUnixPeer(conn, os.Geteuid()+1)
		}
		done <- err
	}()
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("peer check blocked")
	}
}

func privateHostSocketDir(t *testing.T) (string, error) {
	t.Helper()
	dir, err := os.MkdirTemp(os.TempDir(), "kh-")
	if err != nil {
		return "", err
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.EvalSymlinks(dir)
}
func TestProtectedHostCredentialAnonymousPipe(t *testing.T) {
	dir, err := privateHostSocketDir(t)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0700))
	path := filepath.Join(dir, "admin.sock")
	server, err := gateway.NewUnixHostAdminServer(path, os.Geteuid(), localHostRunner{})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	t.Cleanup(func() { _ = server.Close(); require.NoError(t, <-done) })
	client, err := gateway.NewUnixHostAdminClient(path, os.Geteuid())
	require.NoError(t, err)
	read, write, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = read.Close(); _ = write.Close() })
	result, err := client.Run(ctx, domain.HostAdminRequest{Version: 1, RequestID: "pipe", Action: "enroll"}, write)
	require.NoError(t, err)
	require.NoError(t, write.Close())
	data, err := io.ReadAll(read)
	require.NoError(t, err)
	assert.Equal(t, "fixture-secret", string(data))
	assert.NotContains(t, string(result.ResultJSON), "fixture-secret")
}
func TestHostChildRejectsFabricatedPipeDescriptor(t *testing.T) {
	read, write, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = write.Close() })
	_, err = gateway.BeginHostAdminChild(context.Background(), domain.HostAdminRequest{Version: 1, Action: "journal-init"}, read)
	require.ErrorIs(t, err, domain.ErrForbidden)
}

type observedHostRunner struct{ calls chan struct{} }

func (r observedHostRunner) Run(_ context.Context, input domain.HostAdminRequest, _ *os.File) (domain.HostAdminResult, error) {
	r.calls <- struct{}{}
	return domain.HostAdminResult{RequestID: input.RequestID, Code: "ok", Completed: true}, nil
}
func TestProtectedHostRejectsExtraAndLateDescriptorsBeforeAdmission(t *testing.T) {
	for _, kind := range []string{"extra-fd", "late-fd", "trailing-frame", "unknown-field", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			dir, err := privateHostSocketDir(t)
			require.NoError(t, err)
			require.NoError(t, os.Chmod(dir, 0700))
			calls := make(chan struct{}, 1)
			path := filepath.Join(dir, "admin.sock")
			server, err := gateway.NewUnixHostAdminServer(path, os.Geteuid(), observedHostRunner{calls: calls})
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			done := make(chan error, 1)
			go func() { done <- server.Serve(ctx) }()
			t.Cleanup(func() { _ = server.Close(); require.NoError(t, <-done) })
			conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
			require.NoError(t, err)
			t.Cleanup(func() { _ = conn.Close() })
			require.NoError(t, conn.SetDeadline(time.Now().Add(time.Second)))
			file, err := os.CreateTemp(dir, "sink")
			require.NoError(t, err)
			t.Cleanup(func() { _ = file.Close() })
			payload := []byte(`{"version":1,"request_id":"one","action":"journal-check","options":{}}`)
			if kind == "unknown-field" {
				payload = []byte(`{"version":1,"request_id":"one","action":"journal-check","options":{},"secret":"forged"}`)
			}
			header := make([]byte, 4)
			binary.BigEndian.PutUint32(header, uint32(len(payload)))
			switch kind {
			case "extra-fd":
				_, _, err = conn.WriteMsgUnix(append(header, payload...), unix.UnixRights(int(file.Fd()), int(file.Fd())), nil)
			case "late-fd":
				_, err = conn.Write(header)
				require.NoError(t, err)
				_, _, err = conn.WriteMsgUnix(payload, unix.UnixRights(int(file.Fd())), nil)
			case "trailing-frame":
				_, err = conn.Write(append(append(header, payload...), 0))
			case "oversized":
				binary.BigEndian.PutUint32(header, 3<<20)
				_, err = conn.Write(header)
			default:
				_, err = conn.Write(append(header, payload...))
			}
			require.NoError(t, err)
			require.NoError(t, conn.CloseWrite())
			var result [1]byte
			_, _ = conn.Read(result[:])
			select {
			case <-calls:
				t.Fatal("invalid host transport admitted operation")
			default:
			}
		})
	}
}

type knownErrorHostRunner struct {
	code string
	err  error
}

func (r knownErrorHostRunner) Run(_ context.Context, input domain.HostAdminRequest, _ *os.File) (domain.HostAdminResult, error) {
	return domain.HostAdminResult{RequestID: input.RequestID, Code: r.code, Stage: "completed", Completed: true}, r.err
}
func TestProtectedHostKnownCompletionErrorsSurviveUnixRoundTrip(t *testing.T) {
	for _, test := range []struct {
		code string
		err  error
	}{{"conflict", domain.ErrConflict}, {"stale", domain.ErrStale}, {"unauthorized", domain.ErrUnauthorized}, {"incomplete", domain.ErrIncomplete}, {"deleted", domain.ErrDeleted}, {"invalid_input", domain.ErrInvalidInput}, {"forbidden", domain.ErrForbidden}} {
		t.Run(test.code, func(t *testing.T) {
			dir, err := privateHostSocketDir(t)
			require.NoError(t, err)
			require.NoError(t, os.Chmod(dir, 0700))
			path := filepath.Join(dir, "admin.sock")
			server, err := gateway.NewUnixHostAdminServer(path, os.Geteuid(), knownErrorHostRunner{code: test.code, err: test.err})
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			done := make(chan error, 1)
			go func() { done <- server.Serve(ctx) }()
			t.Cleanup(func() { _ = server.Close(); require.NoError(t, <-done) })
			client, err := gateway.NewUnixHostAdminClient(path, os.Geteuid())
			require.NoError(t, err)
			result, err := client.Run(ctx, domain.HostAdminRequest{Version: 1, RequestID: "known", Action: "journal-check"}, nil)
			require.ErrorIs(t, err, test.err)
			assert.Equal(t, test.code, result.Code)
			assert.True(t, result.Completed)
			assert.Equal(t, "completed", result.Stage)
		})
	}
}
