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
Host backup/restore uses kb-supervisor; recovery_unavailable reports an unavailable administration transport.

kb-admin enroll --project PROJECT --credential-fd FD [--client-id ID] [--role agent] [--capabilities LIST] [--ttl DURATION]
kb-admin rotate --client-id ID --credential-fd FD [--ttl DURATION]
kb-admin revoke --client-id ID
--ttl (setup, enroll, rotate) sets when the bearer stops authenticating, as a Go
duration between 1h and 8784h (366 days), for example 2160h for 90 days. Without
it the bearer does not expire. rotate issues a fresh bearer for an active client
with the same grants; the old bearer stops working at once. list shows ExpiresAt.
kb-admin list [--limit 50] [--after ID]
kb-admin access [--client-id ID] [--project PROJECT] [--since YYYY-MM-DD] [--limit 100] [--after CURSOR]
Prints the access audit trail: per client, project, capability and UTC day, how
many requests were allowed and denied, and when the first and last came. A
project of "*" means one the client was never granted. The default window is
the last 30 days; rollups are kept for 400 days. Pass next as --after to page.
kb-admin verify [--preview] [--input PATH|-]
kb-admin reject [--preview] [--input PATH|-]
kb-admin invalidate [--preview] [--input PATH|-]
Invalidation preview accepts {"Revision":"support revision"}; set a new IdempotencyKey
in its returned confirmation JSON before execution.
Adjudication input is strict owner-controlled JSON, read from stdin by default.
Preview is read-only; execution requires exact expected HEAD and digest.
Never put target text, evidence content or credentials in argv.

kb-admin verify-check [--input PATH|-]
Applies the deterministic evidence policy to {"Revision":"candidate revision"} as the
persisted host owner. Git evidence is read only from KB_GIT_REPOSITORIES, an
owner-approved list of id=/absolute/repository entries separated by commas.
kb-admin lesson-publish --preview [--input PATH|-]
kb-admin lesson-publish [--input PATH|-]
Lesson preview accepts the lesson JSON (ProjectID, LessonID, ExpectedHead, Text,
SharingScope, Applicability, Counterexamples, SupportRevisions) and returns its policy
State, Reasons and Digest without writing. Publish the same lesson JSON with
ExpectedDigest set to Digest; a changed lesson, head or support state fails stale.
kb-admin decisions [--input PATH|-]           {"Limit":50}
kb-admin decision-request --preview [--input PATH|-]  {"CandidateRevision":"REV","RequestedAction":"publish"}
kb-admin decision-request [--input PATH|-]    {"CandidateRevision":"REV","RequestedAction":"publish","ExpectedEvidenceDigest":"64_HEX"}
kb-admin decision-sync [--input PATH|-]       {"Limit":50}
kb-admin decision-challenge [--input PATH|-]  {"DecisionID":"ID","Action":"approve|reject"}
kb-admin decision-approve [--input PATH|-]    {"ChallengeID":"ID","DecisionID":"ID","Action":"approve|reject","Signature":"128_HEX"}
Decisions bind one candidate revision and evidence digest and expire after 24h.
decision-request --preview is read-only and prints the confirmation JSON with the
current ExpectedEvidenceDigest; review it, then run decision-request with that JSON.
Changed evidence fails stale. A repeat request returns the same decision; after
expiry it opens a fresh 24h decision. decision-sync is the polling entry point;
run it on a schedule you control (no built-in scheduler).
decision-sync delivers pending decision posts and ingests owner replies via KB's
own Buzz identity (KB_BUZZ_BINARY, KB_BUZZ_RELAY_URL, KB_BUZZ_CHANNEL,
KB_BUZZ_PRIVATE_KEY_FILE); a Buzz outage leaves decisions pending.
Recovery without Buzz: decision-challenge prints SigningDigest; the owner signs it
(BIP-340) on their own device; decision-approve verifies that signature and redeems
a one-use grant for exactly that action. No owner key or grant token is printed.

kb-admin delete --preview [--input PATH|-]
kb-admin delete [--input PATH|-]
kb-admin deletion-status [--input PATH|-]
kb-admin policy [--input PATH|-]
kb-admin policy-update [--input PATH|-]
kb-admin journal-init
kb-admin journal-check
Lifecycle input is strict bounded JSON, read from stdin by default.
Deletion preview accepts {"ProjectID":"project","IDs":["exact ID"]}, also SessionIDs/TaskIDs.
It returns the bounded expanded Manifest, including same-project identical-byte copies
and derivatives, and SelectionDigest. Review that complete manifest, then execute with
the same selection, ExpectedSelectionDigest set to SelectionDigest, and a new
IdempotencyKey. Changed expansion fails stale; retry an unknown outcome with the same
selection and idempotency key. Status accepts {"ID":"operation ID"}.
Policy inspection accepts {"ProjectID":"project"}; policy-update accepts
{"ProjectID":"project","ExpectedRevision":"current revision","Policy":{...}}.
Inspect before CAS update; policy durations use integer nanoseconds in JSON.
Journal commands require independent protected journal configuration and persisted host
owner authority. journal-init is only for a fresh installation;
journal-check never initializes a missing journal. Startup/restore never initializes it.
Source operations require journal recovery first. Exact deletion execution and receipt
status remain privileged repair/diagnosis paths while readiness is blocked.
Journal keys/config are never output.
Successful deletion excludes active data; BackupState expiry_pending records retained backup copies subject to retention and deletion replay.
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
	var host hostOperationError
	if errors.As(err, &host) {
		return host.Error()
	}
	var command commandError
	prefix := ""
	if errors.As(err, &command) {
		prefix = command.command + ": "
	}
	switch {
	case errors.Is(err, domain.ErrMaintenance):
		return prefix + "maintenance: admission refused; retry after the current maintenance operation completes"
	case errors.Is(err, domain.ErrStale):
		return prefix + "stale: expected state changed; preview or inspect current state before retry"
	case errors.Is(err, domain.ErrUnknownOutcome):
		return prefix + "unknown_outcome: retry the exact request with the same idempotency key or inspect status"
	case errors.Is(err, domain.ErrConflict):
		return prefix + "conflict: setup already initialized or expected state changed; check controlled service state"
	case errors.Is(err, domain.ErrInvalidInput):
		return prefix + "invalid_input: check kb-admin help and protected descriptor prerequisites"
	default:
		return prefix + "administration_unavailable: command or credential transfer failed; backup/restore recovery_unavailable"
	}
}

type hostAccess interface {
	ListHost(context.Context, domain.AccessQuery) (domain.AccessPage, error)
}
type hostIdentity interface {
	Setup(context.Context, domain.OwnerIdentity, service.EnrollInput) (service.ClientCredential, error)
	EnrollHost(context.Context, service.EnrollInput) (service.ClientCredential, error)
	RevokeHost(context.Context, string) error
	RotateHost(context.Context, string, time.Duration) (service.ClientCredential, error)
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
	checks       hostChecks
	lessons      hostLessons
	lifecycle    hostLifecycle
	recovery     hostRecovery
	decisions    hostDecisions
	access       hostAccess
}
type adminFactory func(context.Context, bool) (administration, func(), error)

const maxAdminInput = 1 << 20

func run(ctx context.Context, args []string, output io.Writer) error {
	ctx = domain.WithOperationOutcome(ctx)
	factory := composeAdministration
	if len(args) > 0 && lifecycleCommand(args[0]) {
		factory = composeLifecycleAdministration
	}
	if len(args) > 0 && decisionCommand(args[0]) {
		factory = composeDecisionAdministration
	}
	route, complete := productionAdminRoute(ctx, output)
	err := runAdminRouted(ctx, args, output, os.Stdin, factory, route)
	if completionErr := complete(err); completionErr != nil {
		err = completionErr
	}
	if err != nil && len(args) > 0 {
		switch args[0] {
		case "setup", "enroll", "rotate", "revoke", "list", "access", "verify", "reject", "invalidate", "verify-check", "lesson-publish", "recover", "delete", "deletion-status", "policy", "policy-update", "journal-init", "journal-check", "decisions", "decision-request", "decision-sync", "decision-challenge", "decision-approve":
			return commandError{command: args[0], cause: err}
		}
	}
	return err
}

type adminRoute func(domain.HostAdminRequest, *os.File) (bool, error)

func runAdmin(ctx context.Context, args []string, output io.Writer, stdin io.Reader, factory adminFactory) error {
	return runAdminRouted(ctx, args, output, stdin, factory, nil)
}
func runAdminRouted(ctx context.Context, args []string, output io.Writer, stdin io.Reader, factory adminFactory, route adminRoute) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		_, err := io.WriteString(output, help)
		return err
	}
	command := args[0]
	if command == "recover" {
		return domain.ErrUnavailable
	}
	switch command {
	case "setup", "enroll", "rotate", "revoke", "list", "access", "verify", "reject", "invalidate", "verify-check", "lesson-publish", "delete", "deletion-status", "policy", "policy-update", "journal-init", "journal-check", "decisions", "decision-request", "decision-sync", "decision-challenge", "decision-approve":
	default:
		return domain.ErrInvalidInput
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var ownerID, key, project, role, capabilities, clientID, after, inputPath, ttlText, since string
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
		flags.StringVar(&ttlText, "ttl", "", "credential lifetime")
	case "rotate":
		flags.StringVar(&clientID, "client-id", "", "client ID")
		flags.StringVar(&ttlText, "ttl", "", "credential lifetime")
		flags.IntVar(&fd, "credential-fd", 0, "protected output descriptor")
	case "revoke":
		flags.StringVar(&clientID, "client-id", "", "client ID")
	case "list":
		flags.IntVar(&limit, "limit", 50, "page size")
		flags.StringVar(&after, "after", "", "cursor")
	case "access":
		flags.StringVar(&clientID, "client-id", "", "only this client")
		flags.StringVar(&project, "project", "", "only this project")
		flags.StringVar(&since, "since", "", "first UTC day, YYYY-MM-DD (default 30 days ago)")
		flags.IntVar(&limit, "limit", 100, "page size")
		flags.StringVar(&after, "after", "", "cursor")
	case "journal-init", "journal-check":
	default:
		flags.StringVar(&inputPath, "input", "-", "JSON file or stdin")
		if command == "verify" || command == "reject" || command == "invalidate" || command == "delete" || command == "lesson-publish" || command == "decision-request" {
			flags.BoolVar(&preview, "preview", false, "preview only")
		}
	}
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return domain.ErrInvalidInput
	}
	var ttl time.Duration
	if ttlText != "" {
		var err error
		if ttl, err = time.ParseDuration(ttlText); err != nil || ttl <= 0 {
			return domain.ErrInvalidInput
		}
	}
	var credentialFile *os.File
	if domain.HostAdminIssuesCredential(command) {
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
	if command == "access" && (limit < 1 || limit > 500 || (since != "" && !domain.ValidAccessDay(since))) {
		return domain.ErrInvalidInput
	}
	if (command == "revoke" || command == "rotate") && domain.ValidateIDs(clientID) != nil {
		return domain.ErrInvalidInput
	}
	var draft service.AdjudicationDraft
	var decision service.AdjudicationInput
	var invalidation service.InvalidationInput
	var invalidationPreview struct{ Revision string }
	var lifecycleInput lifecycleInput
	var decisionArgs decisionInput
	if decisionCommand(command) {
		if err := readAdminInput(inputPath, stdin, &decisionArgs); err != nil {
			return err
		}
	}
	if lifecycleCommand(command) && command != "journal-init" && command != "journal-check" {
		if err := readAdminInput(inputPath, stdin, lifecycleInput.target(command)); err != nil {
			return err
		}
	}
	var checkInput verificationInput
	if verificationCommand(command) {
		if err := readAdminInput(inputPath, stdin, checkInput.target(command, preview)); err != nil {
			return err
		}
	}
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
	if route != nil {
		request := domain.HostAdminRequest{Version: 1, Action: command, Options: domain.HostAdminOptions{OwnerID: ownerID, PublicKey: key, ProjectID: project, Role: role, Capabilities: capabilities, ClientID: clientID, After: after, Limit: limit, Preview: preview, TTL: ttlText, Since: since}}
		var input any
		switch {
		case lifecycleCommand(command) && command != "journal-init" && command != "journal-check":
			input = lifecycleInput.target(command)
		case verificationCommand(command):
			input = checkInput.target(command, preview)
		case decisionCommand(command):
			input = decisionArgs
		case command == "invalidate" && preview:
			input = invalidationPreview
		case command == "invalidate":
			input = invalidation
		case command == "verify" || command == "reject":
			if preview {
				input = draft
			} else {
				input = decision
			}
		}
		if input != nil {
			data, err := json.Marshal(input)
			if err != nil {
				return domain.ErrInvalidInput
			}
			request.InputJSON = data
		}
		handled, err := route(request, credentialFile)
		if handled || err != nil {
			return err
		}
	}
	admin, closeAdmin, err := factory(ctx, command != "setup")
	if err != nil {
		return err
	}
	defer closeAdmin()
	if requiresRecovery(command, preview) {
		if admin.recovery == nil {
			return domain.ErrUnavailable
		}
		if err := admin.recovery.Recover(ctx); err != nil {
			return err
		}
	}
	if lifecycleCommand(command) {
		return runLifecycle(ctx, command, preview, lifecycleInput, admin.lifecycle, output)
	}
	if verificationCommand(command) {
		return runVerification(ctx, command, preview, checkInput, admin, output)
	}
	if decisionCommand(command) {
		return runDecisions(ctx, command, preview, decisionArgs, admin.decisions, output)
	}
	switch command {
	case "setup", "enroll":
		enrollment := service.EnrollInput{ClientID: clientID, Role: domain.Role(role), ProjectIDs: []string{project}, Capabilities: strings.Split(capabilities, ","), TTL: ttl}
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
	case "rotate":
		credential, err := admin.identity.RotateHost(ctx, clientID, ttl)
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
	case "access":
		if admin.access == nil {
			return domain.ErrUnavailable
		}
		page, err := admin.access.ListHost(ctx, domain.AccessQuery{ClientID: clientID, ProjectID: project, Since: since, Limit: limit, After: after})
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
	store, err := graph.Connect(ctx, cfg.Graph)
	if err != nil {
		return administration{}, nil, err
	}
	closeStore := func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = store.Close(closeCtx)
	}
	identities := repository.NewIdentityRepository(store)
	admin := administration{identity: service.NewIdentityService(store, identities, gateway.NostrIdentity{}), access: service.NewAccessService(store, repository.NewAccessRepository(store), identities, time.Now, nil)}
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
		lifecycle, err := composeLifecycleService(store, objects)
		if err != nil {
			closeStore()
			return administration{}, nil, err
		}
		admin.recovery = lifecycle
		sources, err := composeSources()
		if err != nil {
			closeStore()
			return administration{}, nil, err
		}
		evidence := repository.NewEvidenceRepository(store)
		verifications := repository.NewVerificationRepository(store)
		publications := repository.NewPublicationRepository(store)
		principals := repository.NewRetrievalRepository(store)
		capture := service.NewHostCaptureService(store, evidence, repository.NewJobsRepository(store), objects, identities)
		verification := service.NewVerificationService(store, verifications, publications, identities, evidence, capture).WithEvidenceChecks(service.EvidenceChecks{Principals: principals, Evidence: evidence, Attestations: verifications, Sources: sources, Objects: objects})
		admin.adjudication, admin.checks = verification, verification
		admin.lessons = service.NewLessonService(store, struct {
			*repository.VerificationRepository
			*repository.PublicationRepository
		}{verifications, publications}, principals, identities)
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
