package gateway

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"golang.org/x/sys/unix"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const hostMaxFrame = 2 << 20

func ListenProtectedUnix(path string, uid int) (*net.UnixListener, error) {
	if uid != os.Geteuid() || !filepath.IsAbs(path) {
		return nil, domain.ErrForbidden
	}
	dir, err := openPrivateDirectory(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	if _, err = os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return nil, domain.ErrConflict
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, domain.ErrUnavailable
	}
	if os.Chmod(path, 0600) != nil {
		_ = listener.Close()
		return nil, domain.ErrUnavailable
	}
	return listener, nil
}
func ValidateUnixPeer(conn *net.UnixConn, uid int) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return domain.ErrForbidden
	}
	valid := false
	if err = raw.Control(func(fd uintptr) { peerUID, _, e := unixPeer(int(fd)); valid = e == nil && peerUID == uid }); err != nil || !valid {
		return domain.ErrForbidden
	}
	return nil
}
func validateHostSocket(path string, uid int) error {
	if uid != os.Geteuid() || !filepath.IsAbs(path) {
		return domain.ErrForbidden
	}
	dir, err := openPrivateDirectory(filepath.Dir(path))
	if err != nil {
		return err
	}
	_ = dir.Close()
	info, err := os.Lstat(path)
	if err != nil {
		return domain.ErrUnavailable
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 || stat.Uid != uint32(uid) {
		return domain.ErrForbidden
	}
	return nil
}
func validateCredentialSink(f *os.File, uid int) error {
	if f == nil {
		return domain.ErrInvalidInput
	}
	flags, err := unix.FcntlInt(f.Fd(), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE == unix.O_RDONLY {
		return domain.ErrInvalidInput
	}
	info, err := f.Stat()
	if err != nil {
		return domain.ErrInvalidInput
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(uid) {
		return domain.ErrForbidden
	}
	pipe := info.Mode()&os.ModeNamedPipe != 0
	if !info.Mode().IsRegular() && !pipe {
		return domain.ErrInvalidInput
	}
	anonymous := pipe && anonymousCredentialPipe(int(f.Fd()), info)
	if !anonymous && info.Mode().Perm()&0077 != 0 {
		return domain.ErrForbidden
	}
	return nil
}

type UnixHostAdminClient struct {
	path string
	uid  int
}

func NewUnixHostAdminClient(path string, uid int) (*UnixHostAdminClient, error) {
	if err := validateHostSocket(path, uid); err != nil {
		return nil, err
	}
	return &UnixHostAdminClient{path: path, uid: uid}, nil
}
func (c *UnixHostAdminClient) Run(ctx context.Context, r domain.HostAdminRequest, sink *os.File) (domain.HostAdminResult, error) {
	if err := validateHostSocket(c.path, c.uid); err != nil {
		return domain.HostAdminResult{}, err
	}
	if sink != nil {
		if err := validateCredentialSink(sink, c.uid); err != nil {
			return domain.HostAdminResult{}, err
		}
	}
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "unix", c.path)
	if err != nil {
		return domain.HostAdminResult{}, domain.ErrUnavailable
	}
	defer func() { _ = conn.Close() }()
	unixConn, ok := conn.(*net.UnixConn)
	if !ok || ValidateUnixPeer(unixConn, c.uid) != nil {
		return domain.HostAdminResult{}, domain.ErrForbidden
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	data, err := json.Marshal(r)
	if err != nil || len(data) > hostMaxFrame {
		return domain.HostAdminResult{}, domain.ErrInvalidInput
	}
	frame := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(frame, uint32(len(data)))
	copy(frame[4:], data)
	var rights []byte
	if sink != nil {
		rights = unix.UnixRights(int(sink.Fd()))
	}
	n, _, err := unixConn.WriteMsgUnix(frame, rights, nil)
	if err != nil || n != len(frame) {
		return domain.HostAdminResult{}, domain.ErrUnknownOutcome
	}
	_ = unixConn.CloseWrite()
	var result domain.HostAdminResult
	if readHostFrame(unixConn, &result) != nil {
		return result, domain.ErrUnknownOutcome
	}
	if result.RequestID != r.RequestID {
		return domain.HostAdminResult{}, domain.ErrUnknownOutcome
	}
	if safeHostCompletion(result.Code) {
		return result, hostCompletionError(result.Code)
	}
	switch result.Code {
	case "maintenance":
		return result, domain.ErrMaintenance
	case "unknown_outcome":
		return result, domain.ErrUnknownOutcome
	}
	return result, domain.ErrUnavailable
}

type UnixHostAdminServer struct {
	listener *net.UnixListener
	uid      int
	runner   localHostAdminRunner
}

func NewUnixHostAdminServer(path string, uid int, runner localHostAdminRunner) (*UnixHostAdminServer, error) {
	if runner == nil {
		return nil, domain.ErrInvalidInput
	}
	l, err := ListenProtectedUnix(path, uid)
	if err != nil {
		return nil, err
	}
	return &UnixHostAdminServer{listener: l, uid: uid, runner: runner}, nil
}
func (s *UnixHostAdminServer) Close() error { return s.listener.Close() }
func (s *UnixHostAdminServer) Serve(ctx context.Context) error {
	stop := context.AfterFunc(ctx, func() { _ = s.listener.Close() })
	defer stop()
	for {
		conn, err := s.listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return domain.ErrUnavailable
		}
		go s.serve(ctx, conn)
	}
}
func (s *UnixHostAdminServer) serve(ctx context.Context, conn *net.UnixConn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if ValidateUnixPeer(conn, s.uid) != nil {
		return
	}
	data, files, err := receiveHostRequest(conn)
	defer func() {
		for _, f := range files {
			_ = f.Close()
		}
	}()
	if err != nil {
		return
	}
	var r domain.HostAdminRequest
	if decodeHostJSON(data, &r) != nil {
		return
	}
	var sink *os.File
	if len(files) == 1 {
		sink = files[0]
		if validateCredentialSink(sink, s.uid) != nil {
			return
		}
	}
	needsSink := r.Action == "setup" || r.Action == "enroll"
	if needsSink != (sink != nil) {
		return
	}
	result, runErr := s.runner.Run(ctx, r, sink)
	if runErr != nil {
		result.Code = hostErrorCode(runErr)
	}
	result.RequestID = r.RequestID
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_ = writeHostFrame(conn, result)
}
func decodeHostJSON(data []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(target) != nil || d.Decode(&struct{}{}) != io.EOF {
		return domain.ErrInvalidInput
	}
	return nil
}
func writeHostFrame(w io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil || len(data) > hostMaxFrame {
		return domain.ErrUnavailable
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	if _, err = w.Write(header[:]); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}
func readHostFrame(r io.Reader, value any) error {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 || n > hostMaxFrame {
		return domain.ErrInvalidInput
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	return decodeHostJSON(data, value)
}

type localHostAdminRunner interface {
	Run(context.Context, domain.HostAdminRequest, *os.File) (domain.HostAdminResult, error)
}

// receiveHostRequest reads ancillary data on every segment. A second rights
// attachment, unknown control message, or bytes after the one frame are rejected
// before admission, and every received descriptor is closed by the caller.
func receiveHostRequest(conn *net.UnixConn) ([]byte, []*os.File, error) {
	files := []*os.File{}
	data := []byte{}
	first := true
	for {
		buffer := make([]byte, 32<<10)
		oob := make([]byte, unix.CmsgSpace(16*4))
		n, on, flags, _, readErr := conn.ReadMsgUnix(buffer, oob)
		bad := flags&unix.MSG_CTRUNC != 0
		if on > 0 {
			messages, err := unix.ParseSocketControlMessage(oob[:on])
			if err != nil {
				return nil, files, domain.ErrInvalidInput
			}
			for _, message := range messages {
				if message.Header.Level != unix.SOL_SOCKET || message.Header.Type != unix.SCM_RIGHTS {
					bad = true
					continue
				}
				fds, err := unix.ParseUnixRights(&message)
				if err != nil {
					return nil, files, domain.ErrInvalidInput
				}
				for _, fd := range fds {
					unix.CloseOnExec(fd)
					files = append(files, os.NewFile(uintptr(fd), "host-credential"))
				}
				if !first {
					bad = true
				}
			}
		}
		if bad || len(files) > 1 {
			return nil, files, domain.ErrInvalidInput
		}
		first = false
		data = append(data, buffer[:n]...)
		if len(data) > hostMaxFrame+4 {
			return nil, files, domain.ErrInvalidInput
		}
		if len(data) >= 4 {
			size := binary.BigEndian.Uint32(data)
			if size == 0 || size > hostMaxFrame || len(data) > int(size)+4 {
				return nil, files, domain.ErrInvalidInput
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				return nil, files, domain.ErrInvalidInput
			}
			break
		}
		if n == 0 && on == 0 {
			return nil, files, domain.ErrInvalidInput
		}
	}
	if len(data) < 4 || len(data) != int(binary.BigEndian.Uint32(data))+4 {
		return nil, files, domain.ErrInvalidInput
	}
	return data[4:], files, nil
}

func hostErrorCode(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, domain.ErrConflict):
		return "conflict"
	case errors.Is(err, domain.ErrStale):
		return "stale"
	case errors.Is(err, domain.ErrUnauthorized):
		return "unauthorized"
	case errors.Is(err, domain.ErrIncomplete):
		return "incomplete"
	case errors.Is(err, domain.ErrDeleted):
		return "deleted"
	case errors.Is(err, domain.ErrForbidden):
		return "forbidden"
	case errors.Is(err, domain.ErrInvalidInput):
		return "invalid_input"
	case errors.Is(err, domain.ErrMaintenance):
		return "maintenance"
	case errors.Is(err, domain.ErrUnknownOutcome):
		return "unknown_outcome"
	}
	return "unavailable"
}
