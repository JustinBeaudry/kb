// kb-admin runs on the controlled service host. It has no remote owner token.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/platform"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/JustinBeaudry/kb/server/internal/repository"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"golang.org/x/sys/unix"
)

const help = `kb-admin setup --owner-id PUBLIC_ID --owner-public-key BASE64_ED25519_PUBLIC_KEY --project PROJECT --credential-fd FD

Run once as the operator on the controlled service host with KB_GRAPH_URI,
KB_GRAPH_USER and KB_GRAPH_SECRET supplied by protected service configuration.
Setup enrolls the owner's Buzz public signing identity and the first scoped client;
Buzz relay availability is not required. A second setup is refused.
--credential-fd must name an already-open protected pipe or owner-only file descriptor
(number >=3). Transfer the one-time scoped bearer directly to protected client
credential import; never pass it in argv, print it, or save it in an example.
No owner bearer is minted. Normal clients cannot administer clients or claim trust.
Recovery: recovery_unavailable until signed Buzz recovery is implemented (I2).
`

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, safeError(err))
		os.Exit(1)
	}
}
func safeError(err error) string {
	switch {
	case errors.Is(err, domain.ErrConflict):
		return "setup_already_initialized: second setup refused; recovery_unavailable"
	case errors.Is(err, domain.ErrInvalidInput):
		return "invalid_input: check kb-admin help and protected descriptor prerequisites"
	default:
		return "recovery_unavailable: setup or credential transfer failed; inspect controlled service state"
	}
}
func run(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		_, err := io.WriteString(output, help)
		return err
	}
	if args[0] == "recover" {
		return domain.ErrUnavailable
	}
	if args[0] != "setup" {
		return domain.ErrInvalidInput
	}
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var ownerID, key, project, role, capabilities string
	var fd int
	flags.StringVar(&ownerID, "owner-id", "", "public owner ID")
	flags.StringVar(&key, "owner-public-key", "", "public Ed25519 signing key")
	flags.StringVar(&project, "project", "", "registered project ID")
	flags.StringVar(&role, "role", "agent", "restricted client role")
	flags.StringVar(&capabilities, "capabilities", "status:read,retrieval:read,evidence:read,candidate:write,checkpoint:write", "comma separated scoped capabilities")
	flags.IntVar(&fd, "credential-fd", 0, "protected credential output descriptor")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return domain.ErrInvalidInput
	}
	file, err := protectedDescriptor(ctx, fd)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	cfg, err := platform.LoadConfig()
	if err != nil {
		return err
	}
	store, err := graph.Open(ctx, cfg.Graph)
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = store.Close(closeCtx)
	}()
	credential, err := service.NewIdentityService(store, repository.NewIdentityRepository(store)).Setup(ctx, domain.OwnerIdentity{ID: ownerID, PublicKey: key}, service.EnrollInput{Role: domain.Role(role), ProjectIDs: []string{project}, Capabilities: strings.Split(capabilities, ",")})
	if err != nil {
		return err
	}
	// Write only to our duplicate of the inherited protected descriptor.
	_, err = io.WriteString(file, credential.Bearer+"\n")
	return err
}
func protectedDescriptor(ctx context.Context, fd int) (*os.File, error) {
	if ctx.Err() != nil {
		return nil, domain.ErrUnavailable
	}
	if fd < 3 {
		return nil, domain.ErrInvalidInput
	}
	duplicate, err := syscall.Dup(fd)
	if err != nil {
		return nil, domain.ErrInvalidInput
	}
	file := os.NewFile(uintptr(duplicate), "protected-credential-transfer")
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE == unix.O_RDONLY {
		_ = file.Close()
		return nil, domain.ErrInvalidInput
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, domain.ErrInvalidInput
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		_ = file.Close()
		return nil, domain.ErrInvalidInput
	}
	// macOS anonymous pipes have mode 0660 but no filesystem links. Their
	// inherited descriptors are the access boundary; named FIFOs need private
	// permissions because other processes can open their filesystem path.
	anonymousPipe := info.Mode()&os.ModeNamedPipe != 0 && stat.Nlink == 0
	if !anonymousPipe && info.Mode().Perm()&0077 != 0 {
		_ = file.Close()
		return nil, domain.ErrInvalidInput
	}
	if !info.Mode().IsRegular() && info.Mode()&os.ModeNamedPipe == 0 {
		_ = file.Close()
		return nil, domain.ErrInvalidInput
	}
	return file, nil
}
