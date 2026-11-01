// Command kb-mcp serves the remote memory tools over MCP stdio for one enrolled
// client. It holds only that client's API bearer, received on an inherited
// descriptor; it never holds graph or object-store credentials and never reads
// a local vault.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/handler"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"golang.org/x/sys/unix"
)

const help = `kb-mcp --endpoint ORIGIN --credential-fd FD

Serves memory_orient, memory_search, memory_fetch, memory_submit_candidate,
memory_checkpoint and memory_status over MCP stdio (newline-delimited JSON-RPC).

--endpoint       HTTPS origin of the memory server (HTTP only on 127.0.0.1 or [::1])
--credential-fd  inherited pipe or socket descriptor (>= 3) carrying this client's
                 64-hex bearer; it is read once and closed. Normally supplied by
                 "kb remote mcp --profile NAME", which reads the OS credential store.

Remote failures are returned as tool errors; there is no local fallback.
Remote (HTTP) MCP transport is not offered.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// diagnostic writes one JSON line to stderr; stdout carries MCP frames only.
func diagnostic(w io.Writer, reason string) {
	line, _ := json.Marshal(map[string]string{"program": "kb-mcp", "event": "stopped", "reason": reason})
	_, _ = w.Write(append(line, '\n'))
}

func run(ctx context.Context, args []string, in io.Reader, out, diagnostics io.Writer) int {
	flags := flag.NewFlagSet("kb-mcp", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	endpoint := flags.String("endpoint", "", "memory server origin")
	fd := flags.Int("credential-fd", 0, "inherited credential descriptor")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			_, _ = io.WriteString(diagnostics, help)
			return 0
		}
		_, _ = io.WriteString(diagnostics, help)
		diagnostic(diagnostics, "invalid_arguments")
		return 2
	}
	if flags.NArg() != 0 || *endpoint == "" || *fd < 3 {
		_, _ = io.WriteString(diagnostics, help)
		diagnostic(diagnostics, "invalid_arguments")
		return 2
	}
	bearer, err := readCredential(*fd)
	if err != nil {
		diagnostic(diagnostics, "credential_unavailable")
		return 1
	}
	api, err := gateway.NewMemoryAPI(*endpoint, bearer, 20*time.Second)
	if err != nil {
		diagnostic(diagnostics, "invalid_endpoint_or_credential")
		return 1
	}
	remote := service.NewRemoteMemoryService(api)
	if err = handler.NewMCPServer(remote, remote, remote, remote).ServeStdio(ctx, in, out); err != nil && ctx.Err() == nil {
		diagnostic(diagnostics, "stdio_unavailable")
		return 1
	}
	return 0
}

// readCredential reads one bearer from an inherited private descriptor and
// closes it. Anonymous pipes and unnamed sockets are bounded by descriptor
// inheritance; named FIFOs and files must be owner-only.
func readCredential(fd int) (string, error) {
	duplicate, err := unix.Dup(fd)
	if err != nil {
		return "", domain.ErrInvalidInput
	}
	unix.CloseOnExec(duplicate)
	file := os.NewFile(uintptr(duplicate), "credential")
	defer func() { _ = file.Close() }()
	_ = unix.Close(fd)
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE == unix.O_WRONLY {
		return "", domain.ErrInvalidInput
	}
	info, err := file.Stat()
	if err != nil {
		return "", domain.ErrInvalidInput
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return "", domain.ErrInvalidInput
	}
	if !credentialDescriptorAllowed(info.Mode(), uint64(stat.Nlink)) {
		return "", domain.ErrInvalidInput
	}
	data, err := io.ReadAll(io.LimitReader(file, 130))
	if err != nil || len(data) > 129 {
		return "", domain.ErrInvalidInput
	}
	return string(bytes.TrimSuffix(data, []byte("\n"))), nil
}

// credentialDescriptorAllowed accepts an owner's socket (the launcher passes a
// socketpair, which Linux reports with nlink 1 and mode 0777, so neither is a
// signal), an unnamed pipe, or an owner-only regular file or FIFO.
func credentialDescriptorAllowed(mode os.FileMode, nlink uint64) bool {
	if mode&os.ModeSocket != 0 {
		return true
	}
	if mode&os.ModeNamedPipe != 0 && nlink == 0 {
		return true
	}
	return (mode.IsRegular() || mode&os.ModeNamedPipe != 0) && mode.Perm()&0o077 == 0
}
