package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"golang.org/x/sys/unix"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type HostAdminPermitValidator interface {
	ValidatePermit(context.Context, domain.OperationPermit) error
}
type HostAdminProcessExecutor struct {
	binary    string
	validator HostAdminPermitValidator
}

func NewHostAdminProcessExecutor(binary string, validator HostAdminPermitValidator) (*HostAdminProcessExecutor, error) {
	if !filepath.IsAbs(binary) || validator == nil {
		return nil, domain.ErrInvalidInput
	}
	info, err := os.Stat(binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Mode().Perm()&0111 == 0 {
		return nil, domain.ErrForbidden
	}
	return &HostAdminProcessExecutor{binary: binary, validator: validator}, nil
}

type executionFrame struct {
	Version   int    `json:"version"`
	Stage     string `json:"stage"`
	Challenge string `json:"challenge"`
	Digest    string `json:"digest,omitempty"`
	Code      string `json:"code,omitempty"`
}

func hostRequestDigest(r domain.HostAdminRequest) (string, error) {
	var input any
	if len(r.InputJSON) > 0 {
		d := json.NewDecoder(bytes.NewReader(r.InputJSON))
		d.UseNumber()
		if d.Decode(&input) != nil || d.Decode(&struct{}{}) != io.EOF {
			return "", domain.ErrInvalidInput
		}
	}
	data, err := json.Marshal(struct {
		Version int
		Action  string
		Options domain.HostAdminOptions
		Input   any
	}{r.Version, r.Action, r.Options, input})
	if err != nil {
		return "", domain.ErrInvalidInput
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
func childArguments(r domain.HostAdminRequest) ([]string, error) {
	o := r.Options
	args := []string{r.Action}
	add := func(key, value string) {
		if value != "" {
			args = append(args, key, value)
		}
	}
	switch r.Action {
	case "setup", "enroll":
		if r.Action == "setup" {
			add("--owner-id", o.OwnerID)
			add("--owner-public-key", o.PublicKey)
		} else {
			add("--client-id", o.ClientID)
		}
		add("--project", o.ProjectID)
		add("--role", o.Role)
		add("--capabilities", o.Capabilities)
		args = append(args, "--credential-fd", "3")
	case "revoke":
		add("--client-id", o.ClientID)
	case "list":
		if o.Limit != 0 {
			args = append(args, "--limit", strconv.Itoa(o.Limit))
		}
		add("--after", o.After)
	case "journal-init", "journal-check":
	case "verify", "reject", "invalidate", "delete", "deletion-status", "policy", "policy-update":
		args = append(args, "--input", "-")
		if o.Preview {
			if r.Action != "verify" && r.Action != "reject" && r.Action != "invalidate" && r.Action != "delete" {
				return nil, domain.ErrInvalidInput
			}
			args = append(args, "--preview")
		}
	default:
		return nil, domain.ErrInvalidInput
	}
	for _, arg := range args {
		if len(arg) > 4096 || strings.ContainsRune(arg, 0) {
			return nil, domain.ErrInvalidInput
		}
	}
	return args, nil
}
func (e *HostAdminProcessExecutor) Execute(ctx context.Context, r domain.HostAdminRequest, p domain.OperationPermit, files domain.HostAdminFiles) (result domain.HostAdminResult, resultErr error) {
	result = domain.HostAdminResult{RequestID: r.RequestID, Code: "unavailable", Stage: "not_started", Completed: true}
	// Before Start succeeds no child has execution authority or remote effects.
	// These completed no-effect rejections can safely release their registration.
	defer func() {
		if result.Stage == "not_started" {
			result.Code = hostErrorCode(resultErr)
		}
	}()
	args, err := childArguments(r)
	if err != nil {
		return result, err
	}
	digest, err := hostRequestDigest(r)
	if err != nil {
		return result, err
	}
	if len(r.InputJSON) > 1<<20 {
		return result, domain.ErrInvalidInput
	}
	needsSink := r.Action == "setup" || r.Action == "enroll"
	if needsSink != (files.CredentialSink != nil) {
		return result, domain.ErrInvalidInput
	}
	if needsSink && validateCredentialSink(files.CredentialSink, os.Geteuid()) != nil {
		return result, domain.ErrForbidden
	}
	syscall.ForkLock.RLock()
	descriptors, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err == nil {
		unix.CloseOnExec(descriptors[0])
		unix.CloseOnExec(descriptors[1])
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return result, domain.ErrUnavailable
	}
	parentFile := os.NewFile(uintptr(descriptors[0]), "supervisor-execution")
	childFile := os.NewFile(uintptr(descriptors[1]), "child-execution")
	defer func() { _ = parentFile.Close(); _ = childFile.Close() }()
	connection, err := net.FileConn(parentFile)
	if err != nil {
		return result, domain.ErrUnavailable
	}
	defer func() { _ = connection.Close() }()
	sink := files.CredentialSink
	if sink == nil {
		sink, err = os.Open(os.DevNull)
		if err != nil {
			return result, domain.ErrUnavailable
		}
		defer func() { _ = sink.Close() }()
	}
	runctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(runctx, e.binary, args...)
	cmd.ExtraFiles = []*os.File{sink, childFile}
	cmd.Stdin = bytes.NewReader(r.InputJSON)
	cmd.Env = hostChildEnvironment()
	out := &boundedHostOutput{}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		return result, domain.ErrUnavailable
	}
	// From this point onward only the explicit completion protocol proves that
	// effects settled; failed/canceled grants and child exit alone stay unknown.
	result.Completed = false
	result.Code = "unknown_outcome"
	result.Stage = "child"
	_ = childFile.Close()
	_ = parentFile.Close()
	_ = connection.SetDeadline(time.Now().Add(10 * time.Second))
	challenge, err := randomHex()
	if err == nil {
		err = writeHostFrame(connection, executionFrame{Version: 1, Stage: "challenge", Challenge: challenge})
	}
	var hello executionFrame
	if err == nil {
		err = readHostFrame(connection, &hello)
	}
	if err == nil && (hello.Version != 1 || hello.Stage != "hello" || hello.Challenge != challenge || hello.Digest != digest) {
		err = domain.ErrForbidden
	}
	if err == nil {
		err = e.validator.ValidatePermit(runctx, p)
	}
	if err == nil {
		err = writeHostFrame(connection, executionFrame{Version: 1, Stage: "grant", Challenge: challenge, Digest: digest})
	}
	var completion executionFrame
	if err == nil {
		deadline, _ := runctx.Deadline()
		_ = connection.SetDeadline(deadline)
		err = readHostFrame(connection, &completion)
	}
	completed := err == nil && completion.Version == 1 && completion.Stage == "complete" && completion.Challenge == challenge && completion.Digest == digest && safeHostCompletion(completion.Code)
	if completed {
		err = writeHostFrame(connection, executionFrame{Version: 1, Stage: "completed", Challenge: challenge, Digest: digest, Code: completion.Code})
		completed = err == nil
	} else {
		cancel()
	}
	_ = connection.Close()
	waitErr := cmd.Wait()
	_ = waitErr // Exit status alone cannot prove effect completion; explicit protocol can.
	if !completed || runctx.Err() != nil || out.overflow {
		return result, domain.ErrUnknownOutcome
	}
	result.Completed = true
	result.Code = completion.Code
	result.Stage = "completed"
	result.ResultJSON = append([]byte(nil), out.Bytes()...)
	return result, hostCompletionError(completion.Code)
}
func hostChildEnvironment() []string {
	env := []string{}
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if !strings.HasPrefix(key, "KB_HOST_EXECUTION_") {
			env = append(env, value)
		}
	}
	return append(env, "KB_HOST_EXECUTION_FD=4")
}

type boundedHostOutput struct {
	bytes.Buffer
	overflow bool
}

func (w *boundedHostOutput) Write(p []byte) (int, error) {
	if w.Len()+len(p) > 1<<20 {
		w.overflow = true
		return 0, domain.ErrUnavailable
	}
	return w.Buffer.Write(p)
}
func safeHostCompletion(code string) bool {
	switch code {
	case "ok", "conflict", "stale", "invalid_input", "forbidden", "unauthorized", "incomplete", "deleted", "unavailable":
		return true
	}
	return false
}
func hostCompletionError(code string) error {
	switch code {
	case "ok":
		return nil
	case "conflict":
		return domain.ErrConflict
	case "stale":
		return domain.ErrStale
	case "invalid_input":
		return domain.ErrInvalidInput
	case "forbidden":
		return domain.ErrForbidden
	case "unauthorized":
		return domain.ErrUnauthorized
	case "unavailable":
		return domain.ErrUnavailable
	case "incomplete":
		return domain.ErrIncomplete
	case "deleted":
		return domain.ErrDeleted
	}
	return domain.ErrUnknownOutcome
}

type HostAdminChildSession struct {
	mu                sync.Mutex
	connection        *net.UnixConn
	challenge, digest string
	closed            bool
}

// BeginHostAdminChild consumes channel. Its kernel peer must be the actual
// launching parent, with the runtime UID, before any graph or external I/O.
func BeginHostAdminChild(ctx context.Context, r domain.HostAdminRequest, channel *os.File) (*HostAdminChildSession, error) {
	if channel == nil {
		return nil, domain.ErrForbidden
	}
	defer func() { _ = channel.Close() }()
	info, err := channel.Stat()
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return nil, domain.ErrForbidden
	}
	uid, pid, err := unixPeer(int(channel.Fd()))
	if err != nil || uid != os.Geteuid() || pid != os.Getppid() {
		return nil, domain.ErrForbidden
	}
	raw, err := net.FileConn(channel)
	if err != nil {
		return nil, domain.ErrForbidden
	}
	conn, ok := raw.(*net.UnixConn)
	if !ok {
		_ = raw.Close()
		return nil, domain.ErrForbidden
	}
	success := false
	defer func() {
		if !success {
			_ = conn.Close()
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	digest, err := hostRequestDigest(r)
	if err != nil {
		return nil, err
	}
	var challenge executionFrame
	if readHostFrame(conn, &challenge) != nil || challenge.Version != 1 || challenge.Stage != "challenge" || len(challenge.Challenge) != 32 {
		return nil, domain.ErrForbidden
	}
	if writeHostFrame(conn, executionFrame{Version: 1, Stage: "hello", Challenge: challenge.Challenge, Digest: digest}) != nil {
		return nil, domain.ErrForbidden
	}
	var grant executionFrame
	if readHostFrame(conn, &grant) != nil || grant.Version != 1 || grant.Stage != "grant" || grant.Challenge != challenge.Challenge || grant.Digest != digest {
		return nil, domain.ErrForbidden
	}
	_ = conn.SetDeadline(time.Time{})
	success = true
	return &HostAdminChildSession{connection: conn, challenge: challenge.Challenge, digest: digest}, nil
}
func (s *HostAdminChildSession) Complete(ctx context.Context) error {
	return s.CompleteWithCode(ctx, "ok")
}
func (s *HostAdminChildSession) CompleteWithCode(ctx context.Context, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !safeHostCompletion(code) {
		return domain.ErrForbidden
	}
	s.closed = true
	defer func() { _ = s.connection.Close() }()
	deadline := time.Now().Add(5 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = s.connection.SetDeadline(deadline)
	if writeHostFrame(s.connection, executionFrame{Version: 1, Stage: "complete", Challenge: s.challenge, Digest: s.digest, Code: code}) != nil {
		return domain.ErrUnknownOutcome
	}
	var ack executionFrame
	if readHostFrame(s.connection, &ack) != nil || ack.Version != 1 || ack.Stage != "completed" || ack.Challenge != s.challenge || ack.Digest != s.digest || ack.Code != code {
		return domain.ErrUnknownOutcome
	}
	return nil
}
func (s *HostAdminChildSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	err := s.connection.Close()
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}
