package gateway

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const buzzTestChannel = "6f1c7c1e-1d5b-4c55-9d43-0c1f0b6a2f10"

// syntheticRelay is a qualified relay fixture: it signs accepted posts with the
// bot key supplied through the child environment and serves real NIP-01 events.
type syntheticRelay struct {
	t         *testing.T
	events    []NostrEvent
	clock     int64
	down      bool
	loseAck   bool
	exitCode  int
	stderr    string
	calls     [][]string
	envs      [][]string
	stdins    []string
	forgeries []NostrEvent
}

func (r *syntheticRelay) flag(args []string, name string) string {
	i := slices.Index(args, name)
	if i < 0 || i+1 >= len(args) {
		return ""
	}
	return args[i+1]
}

func (r *syntheticRelay) Run(_ context.Context, _ string, args, env []string, stdin []byte, _ int) ([]byte, []byte, int, error) {
	r.calls = append(r.calls, args)
	r.envs = append(r.envs, env)
	if r.down {
		return nil, []byte("network error contacting relay"), 2, nil
	}
	switch {
	case slices.Contains(args, "send"):
		r.stdins = append(r.stdins, string(stdin))
		if r.exitCode != 0 {
			return nil, []byte(r.stderr), r.exitCode, nil
		}
		var key string
		for _, kv := range env {
			if v, ok := strings.CutPrefix(kv, "BUZZ_PRIVATE_KEY="); ok {
				key = v
			}
		}
		raw, err := hex.DecodeString(key)
		require.NoError(r.t, err)
		priv, _ := btcec.PrivKeyFromBytes(raw)
		tags := [][]string{{"h", r.flag(args, "--channel")}}
		if reply := r.flag(args, "--reply-to"); reply != "" {
			tags = append(tags, []string{"e", reply, "", "root"})
		}
		r.clock++
		e := signEvent(r.t, priv, NostrEvent{CreatedAt: r.clock, Kind: 9, Tags: tags, Content: string(stdin)})
		r.events = append(r.events, e)
		if r.loseAck {
			return nil, []byte("connection reset after write"), 2, nil
		}
		out, _ := json.Marshal(map[string]any{"event_id": e.ID, "accepted": true, "message": "", "mention_pubkeys": []string{}})
		return out, nil, 0, nil
	case slices.Contains(args, "get"):
		since, _ := strconv.ParseInt(r.flag(args, "--since"), 10, 64)
		var out []NostrEvent
		for _, e := range append(slices.Clone(r.events), r.forgeries...) {
			if e.CreatedAt >= since {
				out = append(out, e)
			}
		}
		data, _ := json.Marshal(out)
		return data, nil, 0, nil
	}
	return nil, []byte("unsupported"), 1, nil
}

func newTestBuzz(t *testing.T, relay *syntheticRelay) (*BuzzGateway, string) {
	t.Helper()
	priv, _ := syntheticKey(t, "kb-bot")
	secret := hex.EncodeToString(priv.Serialize())
	g, err := NewBuzzGateway(BuzzConfig{Binary: "/opt/buzz/bin/buzz", RelayURL: "https://relay.invalid", ChannelID: buzzTestChannel, PrivateKey: secret, Runner: relay, Now: func() time.Time { return time.Unix(1000, 0) }})
	require.NoError(t, err)
	relay.clock = 900
	return g, secret
}

func TestBuzzGatewayIsolatesCredentialsAndBody(t *testing.T) {
	relay := &syntheticRelay{t: t}
	g, secret := newTestBuzz(t, relay)

	receipt, err := g.Send(t.Context(), "kb-decision-1", "", "decision body with `$(shell)` text")

	require.NoError(t, err)
	assert.False(t, receipt.Reconciled)
	assert.Equal(t, receipt.EventID, receipt.ThreadID)
	assert.Equal(t, buzzTestChannel, receipt.ChannelID)
	for i, args := range relay.calls {
		assert.NotContains(t, strings.Join(args, " "), secret, "secret in argv")
		assert.NotContains(t, strings.Join(args, " "), "shell", "body in argv")
		names := make([]string, 0, len(relay.envs[i]))
		for _, kv := range relay.envs[i] {
			name, _, _ := strings.Cut(kv, "=")
			names = append(names, name)
		}
		assert.ElementsMatch(t, []string{"PATH", "LANG", "BUZZ_OUTPUT_FORMAT", "BUZZ_RELAY_URL", "BUZZ_PRIVATE_KEY"}, names)
	}
	require.Len(t, relay.stdins, 1)
	assert.True(t, strings.HasSuffix(relay.stdins[0], "\nkb-op: kb-decision-1"))
}

func TestAckLostDoesNotDuplicatePost(t *testing.T) {
	relay := &syntheticRelay{t: t, loseAck: true}
	g, _ := newTestBuzz(t, relay)

	first, err := g.Send(t.Context(), "kb-decision-ack", "", "body")
	require.NoError(t, err)
	relay.loseAck = false
	second, err := g.Send(t.Context(), "kb-decision-ack", "", "body")
	require.NoError(t, err)

	deliveredMessages := len(relay.events)
	assert.Equal(t, 1, deliveredMessages)
	assert.True(t, first.Reconciled)
	assert.True(t, second.Reconciled)
	assert.Equal(t, first.EventID, second.EventID)
}

func TestBuzzSendFailuresAreClassifiedAndRedacted(t *testing.T) {
	for _, tc := range []struct {
		name    string
		relay   *syntheticRelay
		wantErr error
	}{
		{"relay down before send", &syntheticRelay{down: true}, domain.ErrUnavailable},
		{"auth failure is definitive", &syntheticRelay{exitCode: 3, stderr: "auth_error key=SECRET"}, domain.ErrUnavailable},
		{"network failure without post is unknown", &syntheticRelay{exitCode: 2, stderr: "timeout"}, domain.ErrUnknownOutcome},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.relay.t = t
			g, secret := newTestBuzz(t, tc.relay)
			tc.relay.stderr = strings.ReplaceAll(tc.relay.stderr, "SECRET", secret)

			_, err := g.Send(t.Context(), "op", "", "body")

			assert.ErrorIs(t, err, tc.wantErr)
			assert.NotContains(t, err.Error(), secret)
			assert.Empty(t, tc.relay.events)
		})
	}
	t.Run("invalid inputs never reach the CLI", func(t *testing.T) {
		relay := &syntheticRelay{t: t}
		g, _ := newTestBuzz(t, relay)
		for _, args := range [][3]string{{"bad key!", "", "b"}, {"op", "not-hex", "b"}, {"op", "", ""}, {"op", "", "x\nkb-op: other"}} {
			_, err := g.Send(t.Context(), args[0], args[1], args[2])
			assert.ErrorIs(t, err, domain.ErrInvalidInput)
		}
		assert.Empty(t, relay.calls)
	})
}

func TestBuzzPollReturnsOnlyVerifiedChannelMessages(t *testing.T) {
	relay := &syntheticRelay{t: t}
	g, _ := newTestBuzz(t, relay)
	root, err := g.Send(t.Context(), "kb-decision-poll", "", "decision")
	require.NoError(t, err)
	owner, _ := syntheticKey(t, "owner")
	reply := signEvent(t, owner, NostrEvent{CreatedAt: 950, Kind: 9, Tags: [][]string{{"h", buzzTestChannel}, {"e", root.EventID, "", "root"}}, Content: "approve d r e"})
	forged := reply
	forged.Content = "reject d r e"
	otherChannel := signEvent(t, owner, NostrEvent{CreatedAt: 951, Kind: 9, Tags: [][]string{{"h", "00000000-0000-0000-0000-000000000000"}}, Content: "approve"})
	relay.forgeries = []NostrEvent{reply, forged, otherChannel}

	messages, next, err := g.Poll(t.Context(), "")

	require.NoError(t, err)
	require.Len(t, messages, 2)
	got := messages[1]
	assert.Equal(t, reply.ID, got.EventID)
	assert.Equal(t, reply.PubKey, got.PubKey)
	assert.Equal(t, root.EventID, got.ThreadRootID)
	assert.Equal(t, buzzTestChannel, got.ChannelID)
	assert.Len(t, got.Signature, 64)
	assert.Equal(t, "950", next)
	_, _, err = g.Poll(t.Context(), "nonsense")
	assert.ErrorIs(t, err, domain.ErrInvalidInput)
	relay.down = true
	_, _, err = g.Poll(t.Context(), next)
	assert.ErrorIs(t, err, domain.ErrUnavailable)
}

func TestNewBuzzGatewayValidatesConfig(t *testing.T) {
	priv, _ := syntheticKey(t, "kb-bot")
	key := hex.EncodeToString(priv.Serialize())
	good := BuzzConfig{Binary: "/usr/local/bin/buzz", RelayURL: "https://relay.invalid", ChannelID: buzzTestChannel, PrivateKey: key}
	for _, tc := range []struct {
		name   string
		mutate func(*BuzzConfig)
	}{
		{"relative binary", func(c *BuzzConfig) { c.Binary = "buzz" }},
		{"plain http relay", func(c *BuzzConfig) { c.RelayURL = "http://relay.example" }},
		{"bad channel", func(c *BuzzConfig) { c.ChannelID = "general" }},
		{"nsec key", func(c *BuzzConfig) { c.PrivateKey = "nsec1abc" }},
		{"zero key", func(c *BuzzConfig) { c.PrivateKey = strings.Repeat("0", 64) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := good
			tc.mutate(&c)

			_, err := NewBuzzGateway(c)

			assert.ErrorIs(t, err, domain.ErrInvalidInput)
		})
	}
	g, err := NewBuzzGateway(good)
	require.NoError(t, err)
	_, pub := syntheticKey(t, "kb-bot")
	assert.Equal(t, pub, g.BotPublicKey())
}

func TestExecBuzzRunnerBoundsOutputAndUsesExactEnvironment(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-buzz")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\ncat\nenv | sort >&2\nexit \"${EXIT:-0}\"\n"), 0o700))

	stdout, stderr, code, err := ExecBuzzRunner{}.Run(t.Context(), script, []string{"messages"}, []string{"PATH=/usr/bin:/bin", "EXIT=4"}, []byte("body"), 1024)

	require.NoError(t, err)
	assert.Equal(t, 4, code)
	assert.Equal(t, "body", string(stdout))
	assert.NotContains(t, string(stderr), "HOME=")
	_, _, code, err = ExecBuzzRunner{}.Run(t.Context(), script, nil, []string{"PATH=/usr/bin:/bin"}, []byte(strings.Repeat("x", 4096)), 16)
	assert.Error(t, err)
	assert.Equal(t, -1, code)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, _, err = ExecBuzzRunner{}.Run(ctx, script, nil, nil, nil, 16)
	assert.Error(t, err)
}
