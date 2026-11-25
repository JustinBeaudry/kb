package gateway

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

const (
	buzzDefaultTimeout   = 30 * time.Second
	buzzDefaultMaxOutput = 4 << 20
	buzzMaxBody          = 16 << 10
	buzzQueryLimit       = "200"
	// The first poll looks back past the decision TTL so no reply is missed.
	buzzInitialLookback = domain.DefaultDecisionTTL + time.Hour
	buzzOpMarker        = "kb-op: "
)

var (
	buzzUUID   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	buzzOpKey  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	buzzSecret = regexp.MustCompile(`(?i)\b(nsec1[02-9ac-hj-np-z]+|[0-9a-f]{64})\b`)
)

// BuzzRunner executes the Buzz CLI. Implementations must not use a shell and
// must apply exactly the given environment.
type BuzzRunner interface {
	Run(ctx context.Context, binary string, args, env []string, stdin []byte, maxOutput int) (stdout, stderr []byte, exitCode int, err error)
}

// BuzzConfig holds KB's own Buzz bot identity. PrivateKey is a 64-hex secret
// passed to the child only through its environment, never argv or errors.
type BuzzConfig struct {
	Binary, RelayURL, ChannelID, PrivateKey string
	Timeout                                 time.Duration
	MaxOutput                               int
	Runner                                  BuzzRunner
	Now                                     func() time.Time
}

// BuzzGateway posts decision requests and polls owner replies via the Buzz CLI.
type BuzzGateway struct {
	cfg       BuzzConfig
	botPubKey string
}

func NewBuzzGateway(cfg BuzzConfig) (*BuzzGateway, error) {
	if !filepath.IsAbs(cfg.Binary) || !buzzUUID.MatchString(cfg.ChannelID) {
		return nil, domain.ErrInvalidInput
	}
	if !strings.HasPrefix(cfg.RelayURL, "https://") && !strings.HasPrefix(cfg.RelayURL, "http://localhost") && !strings.HasPrefix(cfg.RelayURL, "http://127.0.0.1") {
		return nil, domain.ErrInvalidInput
	}
	raw, ok := lowerHex(cfg.PrivateKey, 32)
	if !ok {
		return nil, domain.ErrInvalidInput
	}
	priv, _ := btcec.PrivKeyFromBytes(raw)
	if priv.Key.IsZero() {
		return nil, domain.ErrInvalidInput
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = buzzDefaultTimeout
	}
	if cfg.MaxOutput <= 0 {
		cfg.MaxOutput = buzzDefaultMaxOutput
	}
	if cfg.Runner == nil {
		cfg.Runner = ExecBuzzRunner{}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &BuzzGateway{cfg: cfg, botPubKey: hex.EncodeToString(schnorr.SerializePubKey(priv.PubKey()))}, nil
}

// BotPublicKey is KB's public Buzz identity (safe to log and display).
func (g *BuzzGateway) BotPublicKey() string { return g.botPubKey }

// environment is an explicit allowlist. An explicit key also prevents the CLI
// from falling back to any desktop-paired identity on the host.
func (g *BuzzGateway) environment() []string {
	return []string{"PATH=/usr/bin:/bin", "LANG=C", "BUZZ_OUTPUT_FORMAT=json", "BUZZ_RELAY_URL=" + g.cfg.RelayURL, "BUZZ_PRIVATE_KEY=" + g.cfg.PrivateKey}
}

type buzzExitError struct {
	op     string
	code   int
	detail string
}

func (e buzzExitError) Error() string {
	return fmt.Sprintf("buzz %s failed: exit=%d %s", e.op, e.code, e.detail)
}

// redact removes secrets and key-shaped values from bounded diagnostic text.
func (g *BuzzGateway) redact(b []byte) string {
	s := string(b)
	if len(s) > 256 {
		s = s[:256]
	}
	s = strings.ReplaceAll(s, g.cfg.PrivateKey, "[redacted]")
	s = buzzSecret.ReplaceAllString(s, "[redacted]")
	return strings.ToValidUTF8(strings.Join(strings.Fields(s), " "), "?")
}

func (g *BuzzGateway) run(ctx context.Context, op string, args []string, stdin []byte) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, g.cfg.Timeout)
	defer cancel()
	stdout, stderr, code, err := g.cfg.Runner.Run(ctx, g.cfg.Binary, append([]string{"--format", "json"}, args...), g.environment(), stdin, g.cfg.MaxOutput)
	if err != nil || code != 0 {
		if code == 0 {
			code = -1
		}
		return nil, code, buzzExitError{op: op, code: code, detail: g.redact(stderr)}
	}
	return stdout, 0, nil
}

func (g *BuzzGateway) query(ctx context.Context, since int64) ([]NostrEvent, error) {
	args := []string{"messages", "get", "--channel", g.cfg.ChannelID, "--limit", buzzQueryLimit}
	if since > 0 {
		args = append(args, "--since", strconv.FormatInt(since, 10))
	}
	out, _, err := g.run(ctx, "query", args, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", domain.ErrUnavailable, err)
	}
	var events []NostrEvent
	if json.Unmarshal(out, &events) != nil {
		return nil, fmt.Errorf("%w: buzz query returned malformed output", domain.ErrUnavailable)
	}
	verified := events[:0]
	for _, e := range events {
		if VerifyNostrEvent(e) == nil && g.inChannel(e) {
			verified = append(verified, e)
		}
	}
	return verified, nil
}

func (g *BuzzGateway) inChannel(e NostrEvent) bool {
	for _, t := range e.Tags {
		if len(t) >= 2 && t[0] == "h" {
			return t[1] == g.cfg.ChannelID
		}
	}
	return false
}

// threadRefs follows NIP-10 markers: root wins, reply falls back as root.
func threadRefs(e NostrEvent) (root, parent string) {
	for _, t := range e.Tags {
		if len(t) < 4 || t[0] != "e" {
			continue
		}
		if _, ok := lowerHex(t[1], 32); !ok {
			continue
		}
		switch t[3] {
		case "root":
			root = t[1]
		case "reply":
			parent = t[1]
		}
	}
	if root == "" {
		root = parent
	}
	if parent == "" {
		parent = root
	}
	return root, parent
}

func (g *BuzzGateway) reconcile(ctx context.Context, key string) (domain.MessageReceipt, bool, error) {
	events, err := g.query(ctx, g.cfg.Now().Add(-buzzInitialLookback).Unix())
	if err != nil {
		return domain.MessageReceipt{}, false, err
	}
	marker := "\n" + buzzOpMarker + key
	for _, e := range events {
		if e.PubKey == g.botPubKey && strings.HasSuffix(e.Content, marker) {
			root, _ := threadRefs(e)
			if root == "" {
				root = e.ID
			}
			return domain.MessageReceipt{OperationKey: key, EventID: e.ID, ThreadID: root, ChannelID: g.cfg.ChannelID, Reconciled: true}, true, nil
		}
	}
	return domain.MessageReceipt{}, false, nil
}

// Send posts body once per operation key. It reconciles by operation identity
// before posting and after any ambiguous failure, so a lost acknowledgement or
// a caller retry never duplicates the post.
func (g *BuzzGateway) Send(ctx context.Context, operationKey, threadID, body string) (domain.MessageReceipt, error) {
	if !buzzOpKey.MatchString(operationKey) || len(body) == 0 || len(body) > buzzMaxBody || !utf8.ValidString(body) || strings.Contains(body, buzzOpMarker) {
		return domain.MessageReceipt{}, domain.ErrInvalidInput
	}
	if _, ok := lowerHex(threadID, 32); threadID != "" && !ok {
		return domain.MessageReceipt{}, domain.ErrInvalidInput
	}
	if receipt, found, err := g.reconcile(ctx, operationKey); err != nil || found {
		return receipt, err
	}
	args := []string{"messages", "send", "--channel", g.cfg.ChannelID, "--content", "-"}
	if threadID != "" {
		args = append(args, "--reply-to", threadID)
	}
	out, code, err := g.run(ctx, "send", args, []byte(body+"\n"+buzzOpMarker+operationKey))
	if err == nil {
		var ack struct {
			EventID  string `json:"event_id"`
			Accepted bool   `json:"accepted"`
		}
		if json.Unmarshal(out, &ack) == nil {
			if _, ok := lowerHex(ack.EventID, 32); ok && ack.Accepted {
				root := ack.EventID
				if threadID != "" {
					root = threadID
				}
				return domain.MessageReceipt{OperationKey: operationKey, EventID: ack.EventID, ThreadID: root, ChannelID: g.cfg.ChannelID}, nil
			}
			if !ack.Accepted && ack.EventID != "" {
				return domain.MessageReceipt{}, fmt.Errorf("%w: buzz relay rejected the post", domain.ErrUnavailable)
			}
		}
	}
	// Usage (1) and auth (3) failures are definitive: nothing was published.
	if code == 1 || code == 3 {
		return domain.MessageReceipt{}, fmt.Errorf("%w: %w", domain.ErrUnavailable, err)
	}
	receipt, found, rerr := g.reconcile(context.WithoutCancel(ctx), operationKey)
	if rerr == nil && found {
		return receipt, nil
	}
	return domain.MessageReceipt{}, fmt.Errorf("%w: buzz send outcome unknown; retry reconciles by operation key", domain.ErrUnknownOutcome)
}

// Poll returns verified channel messages at or after cursor (unix seconds).
// Duplicates across polls are expected; the service deduplicates by event ID.
func (g *BuzzGateway) Poll(ctx context.Context, cursor string) ([]domain.SignedMessage, string, error) {
	since := g.cfg.Now().Add(-buzzInitialLookback).Unix()
	if cursor != "" {
		v, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil || v <= 0 {
			return nil, "", domain.ErrInvalidInput
		}
		since = v
	}
	events, err := g.query(ctx, since)
	if err != nil {
		return nil, "", err
	}
	next := since
	out := make([]domain.SignedMessage, 0, len(events))
	for _, e := range events {
		sig, _ := hex.DecodeString(e.Sig)
		root, parent := threadRefs(e)
		out = append(out, domain.SignedMessage{EventID: e.ID, PubKey: e.PubKey, ChannelID: g.cfg.ChannelID, ThreadRootID: root, ParentID: parent, Content: e.Content, Kind: e.Kind, CreatedAt: time.Unix(e.CreatedAt, 0).UTC(), Signature: sig})
		if e.CreatedAt > next {
			next = e.CreatedAt
		}
	}
	return out, strconv.FormatInt(next, 10), nil
}

// ExecBuzzRunner runs the CLI directly (no shell) with bounded output.
type ExecBuzzRunner struct{}

// boundedBuffer deliberately does not embed bytes.Buffer: a promoted ReadFrom
// would let io.Copy bypass the limit.
type boundedBuffer struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.limit {
		b.overflow = true
		return 0, errors.New("output limit exceeded")
	}
	return b.buf.Write(p)
}
func (b *boundedBuffer) Bytes() []byte { return b.buf.Bytes() }

func (ExecBuzzRunner) Run(ctx context.Context, binary string, args, env []string, stdin []byte, maxOutput int) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(stdin)
	stdout := &boundedBuffer{limit: maxOutput}
	stderr := &boundedBuffer{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if stdout.overflow {
		return nil, stderr.Bytes(), -1, errors.New("buzz output exceeded limit")
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return stdout.Bytes(), stderr.Bytes(), exit.ExitCode(), nil
	}
	if err != nil {
		return nil, stderr.Bytes(), -1, err
	}
	return stdout.Bytes(), stderr.Bytes(), 0, nil
}
