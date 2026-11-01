// kb-admin runs on the controlled service host. It has no remote owner token.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/platform"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/JustinBeaudry/kb/server/internal/repository"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"golang.org/x/sys/unix"
)

const help = `kb-admin setup --owner-id PUBLIC_ID --owner-public-key 64_HEX_NOSTR_PUBLIC_KEY --project PROJECT --credential-fd FD

Run once as the operator on the controlled service host with KB_GRAPH_URI,
KB_GRAPH_USER and KB_GRAPH_SECRET supplied by protected service configuration.
Setup enrolls the owner's Buzz public signing identity and the first scoped client;
Buzz relay availability is not required. A second setup is refused.
--credential-fd must name an already-open protected pipe or owner-only file descriptor
(number >=3). Transfer the one-time scoped bearer directly to protected client
credential import; never pass it in argv, print it, or save it in an example.
The public key must be a valid 64-hex Nostr/BIP-340 secp256k1 x-only point;
its canonical lowercase form is stored separately from the owner ID.
No private key is accepted. No owner bearer is minted. Normal clients cannot administer clients or claim trust.
Host recovery: enroll, revoke and list use the persisted owner identity.
Backup/restore recovery remains recovery_unavailable until O2.

kb-admin enroll --project PROJECT --credential-fd FD [--client-id ID] [--role agent] [--capabilities LIST]
kb-admin revoke --client-id ID
kb-admin list [--limit 50] [--after ID]
kb-admin verify [--preview] [--input PATH|-]
kb-admin reject [--preview] [--input PATH|-]
kb-admin invalidate [--preview] [--input PATH|-]
Invalidation preview accepts {"Revision":"support revision"}; set a new IdempotencyKey
in its returned confirmation JSON before execution.
Adjudication input is strict owner-controlled JSON, read from stdin by default.
Preview is read-only; execution requires exact expected HEAD and digest.
Never put target text, evidence content or credentials in argv.
`

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, safeError(err))
		os.Exit(1)
	}
}

type commandError struct {
	command string
	cause   error
}

func (e commandError) Error() string { return e.command + ": administration failed" }
func (e commandError) Unwrap() error { return e.cause }
func safeError(err error) string {
	var command commandError
	prefix := ""
	if errors.As(err, &command) {
		prefix = command.command + ": "
	}
	switch {
	case errors.Is(err, domain.ErrConflict):
		return prefix + "conflict: setup already initialized or expected state changed; check controlled service state"
	case errors.Is(err, domain.ErrInvalidInput):
		return prefix + "invalid_input: check kb-admin help and protected descriptor prerequisites"
	default:
		return prefix + "administration_unavailable: command or credential transfer failed; backup/restore recovery_unavailable"
	}
}

type hostIdentity interface {
	Setup(context.Context, domain.OwnerIdentity, service.EnrollInput) (service.ClientCredential, error)
	EnrollHost(context.Context, service.EnrollInput) (service.ClientCredential, error)
	RevokeHost(context.Context, string) error
	ListHost(context.Context, int, string) (service.ClientPage, error)
}
type hostAdjudication interface {
	PreviewHost(context.Context, service.AdjudicationDraft) (service.AdjudicationPreview, error)
	AdjudicateHost(context.Context, service.AdjudicationInput) (service.AdjudicationResult, error)
	PreviewInvalidationHost(context.Context, string) (service.InvalidationInput, error)
	InvalidateHost(context.Context, service.InvalidationInput) (domain.Receipt, error)
}
type administration struct {
	identity     hostIdentity
	adjudication hostAdjudication
}
type adminFactory func(context.Context, bool) (administration, func(), error)

const maxAdminInput = 1 << 20

func run(ctx context.Context, args []string, output io.Writer) error {
	err := runAdmin(ctx, args, output, os.Stdin, composeAdministration)
	if err != nil && len(args) > 0 {
		switch args[0] {
		case "setup", "enroll", "revoke", "list", "verify", "reject", "invalidate", "recover":
			return commandError{command: args[0], cause: err}
		}
	}
	return err
}
func runAdmin(ctx context.Context, args []string, output io.Writer, stdin io.Reader, factory adminFactory) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		_, err := io.WriteString(output, help)
		return err
	}
	command := args[0]
	if command == "recover" {
		return domain.ErrUnavailable
	}
	switch command {
	case "setup", "enroll", "revoke", "list", "verify", "reject", "invalidate":
	default:
		return domain.ErrInvalidInput
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var ownerID, key, project, role, capabilities, clientID, after, inputPath string
	var fd, limit int
	var preview bool
	switch command {
	case "setup", "enroll":
		if command == "setup" {
			flags.StringVar(&ownerID, "owner-id", "", "public owner ID")
			flags.StringVar(&key, "owner-public-key", "", "public signing key")
		}
		flags.StringVar(&project, "project", "", "project ID")
		flags.StringVar(&role, "role", "agent", "scoped role")
		flags.StringVar(&capabilities, "capabilities", "status:read,retrieval:read,evidence:read,candidate:write,checkpoint:write", "scoped capabilities")
		flags.IntVar(&fd, "credential-fd", 0, "protected output descriptor")
		if command == "enroll" {
			flags.StringVar(&clientID, "client-id", "", "client ID")
		}
	case "revoke":
		flags.StringVar(&clientID, "client-id", "", "client ID")
	case "list":
		flags.IntVar(&limit, "limit", 50, "page size")
		flags.StringVar(&after, "after", "", "cursor")
	default:
		flags.StringVar(&inputPath, "input", "-", "JSON file or stdin")
		if command == "verify" || command == "reject" || command == "invalidate" {
			flags.BoolVar(&preview, "preview", false, "preview only")
		}
	}
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return domain.ErrInvalidInput
	}
	var credentialFile *os.File
	if command == "setup" || command == "enroll" {
		var err error
		credentialFile, err = protectedDescriptor(ctx, fd)
		if err != nil {
			return err
		}
		defer func() { _ = credentialFile.Close() }()
	}
	if command == "list" && (limit < 1 || limit > 100) {
		return domain.ErrInvalidInput
	}
	if command == "revoke" && domain.ValidateIDs(clientID) != nil {
		return domain.ErrInvalidInput
	}
	var draft service.AdjudicationDraft
	var decision service.AdjudicationInput
	var invalidation service.InvalidationInput
	var invalidationPreview struct{ Revision string }
	if command == "verify" || command == "reject" || command == "invalidate" {
		reader := stdin
		if inputPath != "-" {
			file, err := os.Open(inputPath)
			if err != nil {
				return domain.ErrInvalidInput
			}
			defer func() { _ = file.Close() }()
			reader = file
		}
		var target any = &decision
		if preview {
			target = &draft
		}
		if command == "invalidate" {
			target = &invalidation
			if preview {
				target = &invalidationPreview
			}
		}
		if err := readAdminJSON(reader, target); err != nil {
			return err
		}
		if command != "invalidate" {
			action := decision.Action
			if preview {
				action = draft.Action
			}
			if action != "" && action != command {
				return domain.ErrInvalidInput
			}
			if preview {
				draft.Action = command
			} else {
				decision.Action = command
			}
		}
	}
	admin, closeAdmin, err := factory(ctx, command == "verify" || command == "reject" || command == "invalidate")
	if err != nil {
		return err
	}
	defer closeAdmin()
	switch command {
	case "setup", "enroll":
		enrollment := service.EnrollInput{ClientID: clientID, Role: domain.Role(role), ProjectIDs: []string{project}, Capabilities: strings.Split(capabilities, ",")}
		var credential service.ClientCredential
		if command == "setup" {
			credential, err = admin.identity.Setup(ctx, domain.OwnerIdentity{ID: ownerID, PublicKey: key}, enrollment)
		} else {
			credential, err = admin.identity.EnrollHost(ctx, enrollment)
		}
		if err != nil {
			return err
		}
		_, err = io.WriteString(credentialFile, credential.Bearer+"\n")
		return err
	case "revoke":
		return admin.identity.RevokeHost(ctx, clientID)
	case "list":
		page, err := admin.identity.ListHost(ctx, limit, after)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(page)
	case "invalidate":
		if preview {
			confirmation, err := admin.adjudication.PreviewInvalidationHost(ctx, invalidationPreview.Revision)
			if err != nil {
				return err
			}
			return json.NewEncoder(output).Encode(confirmation)
		}
		receipt, err := admin.adjudication.InvalidateHost(ctx, invalidation)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(receipt)
	default:
		if preview {
			result, err := admin.adjudication.PreviewHost(ctx, draft)
			if err != nil {
				return err
			}
			return json.NewEncoder(output).Encode(result)
		}
		result, err := admin.adjudication.AdjudicateHost(ctx, decision)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(result)
	}
}
func readAdminJSON(reader io.Reader, target any) error {
	raw, err := io.ReadAll(io.LimitReader(reader, maxAdminInput+1))
	if err != nil || len(raw) > maxAdminInput {
		return domain.ErrInvalidInput
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return domain.ErrInvalidInput
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return domain.ErrInvalidInput
	}
	return nil
}
func composeAdministration(ctx context.Context, withObjects bool) (administration, func(), error) {
	cfg, err := platform.LoadConfig()
	if err != nil {
		return administration{}, nil, err
	}
	store, err := graph.Open(ctx, cfg.Graph)
	if err != nil {
		return administration{}, nil, err
	}
	closeStore := func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = store.Close(closeCtx)
	}
	identities := repository.NewIdentityRepository(store)
	admin := administration{identity: service.NewIdentityService(store, identities, gateway.NostrIdentity{})}
	if withObjects {
		objectConfig, err := platform.LoadObjectConfig()
		if err != nil {
			closeStore()
			return administration{}, nil, err
		}
		objects, err := gateway.NewS3(objectConfig)
		if err != nil {
			closeStore()
			return administration{}, nil, err
		}
		evidence := repository.NewEvidenceRepository(store)
		capture := service.NewHostCaptureService(store, evidence, repository.NewJobsRepository(store), objects, identities)
		admin.adjudication = service.NewVerificationService(store, repository.NewVerificationRepository(store), repository.NewPublicationRepository(store), identities, evidence, capture)
	}
	return admin, closeStore, nil
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
