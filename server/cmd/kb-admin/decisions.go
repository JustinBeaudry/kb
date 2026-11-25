package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/platform"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/JustinBeaudry/kb/server/internal/repository"
	"github.com/JustinBeaudry/kb/server/internal/service"
)

// hostDecisions is the host-owner surface for Buzz decisions and recovery. It
// never receives or prints an owner private key or grant token.
type hostDecisions interface {
	PrepareHost(context.Context, string, string) (domain.Decision, error)
	RequestHost(context.Context, domain.Decision) (domain.Decision, error)
	Pending(context.Context, int) ([]domain.Decision, error)
	Deliver(context.Context, int) (int, error)
	Ingest(context.Context) (service.IngestResult, error)
	CreateDecisionChallenge(context.Context, string, string) (domain.OwnerChallenge, error)
	ApproveOwnerChallenge(context.Context, string, []byte) (domain.ActionGrant, string, error)
	RespondWithGrant(context.Context, string, string, string) (domain.Decision, error)
}

type decisionInput struct {
	Limit                                      int    `json:",omitempty"`
	DecisionID, Action, ChallengeID, Signature string `json:",omitempty"`
	// decision-request: preview takes the revision and action; apply also
	// takes the previewed evidence digest.
	CandidateRevision, RequestedAction, ExpectedEvidenceDigest string `json:",omitempty"`
}

func decisionCommand(command string) bool {
	switch command {
	case "decisions", "decision-request", "decision-sync", "decision-challenge", "decision-approve":
		return true
	}
	return false
}

func runDecisions(ctx context.Context, command string, preview bool, input decisionInput, host hostDecisions, output io.Writer) error {
	if host == nil {
		return domain.ErrUnavailable
	}
	encode := json.NewEncoder(output).Encode
	switch command {
	case "decisions":
		pending, err := host.Pending(ctx, input.Limit)
		if err != nil {
			return err
		}
		return encode(pending)
	case "decision-request":
		if input.CandidateRevision == "" || input.RequestedAction == "" {
			return domain.ErrInvalidInput
		}
		if preview {
			draft, err := host.PrepareHost(ctx, input.CandidateRevision, input.RequestedAction)
			if err != nil {
				return err
			}
			return encode(decisionInput{CandidateRevision: draft.CandidateRevision, RequestedAction: draft.RequestedAction, ExpectedEvidenceDigest: draft.EvidenceDigest})
		}
		if input.ExpectedEvidenceDigest == "" {
			return domain.ErrInvalidInput
		}
		d, err := host.RequestHost(ctx, domain.Decision{CandidateRevision: input.CandidateRevision, RequestedAction: input.RequestedAction, EvidenceDigest: input.ExpectedEvidenceDigest})
		if err != nil {
			return err
		}
		return encode(d)
	case "decision-sync":
		// Delivery and polling are independent: an outage in one leaves
		// decisions pending and is reported, never silently dropped.
		delivered, deliverErr := host.Deliver(ctx, input.Limit)
		ingested, ingestErr := host.Ingest(ctx)
		if err := encode(struct {
			Delivered int
			Ingested  service.IngestResult
		}{delivered, ingested}); err != nil {
			return err
		}
		if deliverErr != nil {
			return deliverErr
		}
		return ingestErr
	case "decision-challenge":
		c, err := host.CreateDecisionChallenge(ctx, input.DecisionID, input.Action)
		if err != nil {
			return err
		}
		digest := c.SigningDigest()
		return encode(struct {
			Challenge     domain.OwnerChallenge
			SigningDigest string
		}{c, hex.EncodeToString(digest[:])})
	default:
		sig, err := hex.DecodeString(input.Signature)
		if err != nil || len(sig) != 64 {
			return domain.ErrInvalidInput
		}
		_, token, err := host.ApproveOwnerChallenge(ctx, input.ChallengeID, sig)
		if err != nil {
			return err
		}
		d, err := host.RespondWithGrant(ctx, token, input.DecisionID, input.Action)
		if err != nil {
			return err
		}
		return encode(d)
	}
}

// loadBuzzGateway reads KB's own Buzz bot identity from protected service
// configuration. The private key comes from an owner-only file, never argv.
// Missing configuration yields nil: decisions stay pending for recovery.
func loadBuzzGateway() (service.BuzzGateway, error) {
	binary, relay, channel, keyFile := os.Getenv("KB_BUZZ_BINARY"), os.Getenv("KB_BUZZ_RELAY_URL"), os.Getenv("KB_BUZZ_CHANNEL"), os.Getenv("KB_BUZZ_PRIVATE_KEY_FILE")
	if binary == "" && relay == "" && channel == "" && keyFile == "" {
		return nil, nil
	}
	info, err := os.Stat(keyFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 256 {
		return nil, domain.ErrInvalidInput
	}
	raw, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, domain.ErrInvalidInput
	}
	g, err := gateway.NewBuzzGateway(gateway.BuzzConfig{Binary: binary, RelayURL: relay, ChannelID: channel, PrivateKey: strings.TrimSpace(string(raw))})
	if err != nil {
		return nil, err
	}
	return g, nil
}

func composeDecisionAdministration(ctx context.Context, _ bool) (administration, func(), error) {
	cfg, err := platform.LoadConfig()
	if err != nil {
		return administration{}, nil, err
	}
	buzz, err := loadBuzzGateway()
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
	decisions := service.NewDecisionService(store, repository.NewDecisionRepository(store), repository.NewVerificationRepository(store), repository.NewEvidenceRepository(store), identities, buzz, gateway.NostrVerifier{}, time.Now)
	return administration{identity: service.NewIdentityService(store, identities, gateway.NostrIdentity{}), decisions: decisions}, closeStore, nil
}
