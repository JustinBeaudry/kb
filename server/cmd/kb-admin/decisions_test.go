package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeHostDecisions struct {
	deliverErr, ingestErr error
	token, usedToken      string
	signature             []byte
	requested             []domain.Decision
	judgmentQuery         domain.JudgmentQuery
}

const fakeEvidenceDigest = "1111111111111111111111111111111111111111111111111111111111111111"

func (f *fakeHostDecisions) PrepareHost(_ context.Context, revision, action string) (domain.Decision, error) {
	return domain.Decision{CandidateRevision: revision, RequestedAction: action, EvidenceDigest: fakeEvidenceDigest, ProjectID: "demo"}, nil
}
func (f *fakeHostDecisions) RequestHost(_ context.Context, d domain.Decision) (domain.Decision, error) {
	if d.EvidenceDigest != fakeEvidenceDigest {
		return domain.Decision{}, domain.ErrStale
	}
	f.requested = append(f.requested, d)
	d.ID, d.State = "dec-new", domain.DecisionPending
	return d, nil
}

func (f *fakeHostDecisions) Pending(context.Context, int) ([]domain.Decision, error) {
	return []domain.Decision{{ID: "dec-1", State: domain.DecisionPending}}, nil
}
func (f *fakeHostDecisions) Deliver(context.Context, int) (int, error) { return 0, f.deliverErr }
func (f *fakeHostDecisions) Ingest(context.Context) (service.IngestResult, error) {
	return service.IngestResult{Resolved: 1}, f.ingestErr
}
func (f *fakeHostDecisions) CreateDecisionChallenge(_ context.Context, id, action string) (domain.OwnerChallenge, error) {
	return domain.OwnerChallenge{ID: "ch-1", Nonce: "n", RequestedAction: service.DecisionRespondAction, ActionDigest: id + action, ExpiresAt: time.Unix(0, 0)}, nil
}
func (f *fakeHostDecisions) ApproveOwnerChallenge(_ context.Context, _ string, sig []byte) (domain.ActionGrant, string, error) {
	f.signature = sig
	return domain.ActionGrant{ID: "grant"}, f.token, nil
}
func (f *fakeHostDecisions) RespondWithGrant(_ context.Context, token, id, _ string) (domain.Decision, error) {
	f.usedToken = token
	return domain.Decision{ID: id, State: domain.DecisionAccepted}, nil
}

func (f *fakeHostDecisions) JudgmentsHost(_ context.Context, q domain.JudgmentQuery) (domain.JudgmentPage, error) {
	f.judgmentQuery = q
	return domain.JudgmentPage{Judgments: []domain.JudgmentRecord{{ID: "j-1", SignerID: q.Signer, Action: "approve", ProjectID: "demo"}}, Next: "cursor-2"}, nil
}

func TestDecisionCommandsNeverPrintGrantToken(t *testing.T) {
	host := &fakeHostDecisions{token: "secret-grant-token"}
	factory := func(context.Context, bool) (administration, func(), error) {
		return administration{decisions: host}, func() {}, nil
	}
	for _, tc := range []struct {
		name, command, input, want string
		wantErr                    error
	}{
		{"list pending", "decisions", `{"Limit":10}`, `"dec-1"`, nil},
		{"challenge prints signing digest", "decision-challenge", `{"DecisionID":"dec-1","Action":"approve"}`, `"SigningDigest"`, nil},
		{"approve redeems grant internally", "decision-approve", `{"ChallengeID":"ch-1","DecisionID":"dec-1","Action":"approve","Signature":"` + strings.Repeat("ab", 64) + `"}`, `"accepted"`, nil},
		{"approve rejects malformed signature", "decision-approve", `{"ChallengeID":"ch-1","DecisionID":"dec-1","Action":"approve","Signature":"zz"}`, "", domain.ErrInvalidInput},
		{"unknown field rejected", "decisions", `{"Limit":1,"Token":"x"}`, "", domain.ErrInvalidInput},
		{"sync reports outage after output", "decision-sync", `{"Limit":10}`, `"Resolved":1`, domain.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host.deliverErr = nil
			if tc.command == "decision-sync" {
				host.deliverErr = domain.ErrUnavailable
			}
			var out bytes.Buffer

			err := runAdmin(t.Context(), []string{tc.command}, &out, strings.NewReader(tc.input), factory)

			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Contains(t, out.String(), tc.want)
			assert.NotContains(t, out.String(), host.token)
		})
	}
	assert.Equal(t, "secret-grant-token", host.usedToken)
	assert.Len(t, host.signature, 64)
}

func TestLoadBuzzGatewayRequiresProtectedKeyFile(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	require.NoError(t, os.WriteFile(key, []byte(strings.Repeat("1", 64)+"\n"), 0o644))
	t.Setenv("KB_BUZZ_BINARY", "/opt/buzz/bin/buzz")
	t.Setenv("KB_BUZZ_RELAY_URL", "https://relay.invalid")
	t.Setenv("KB_BUZZ_CHANNEL", "6f1c7c1e-1d5b-4c55-9d43-0c1f0b6a2f10")
	t.Setenv("KB_BUZZ_PRIVATE_KEY_FILE", key)

	_, err := loadBuzzGateway()
	assert.ErrorIs(t, err, domain.ErrInvalidInput)

	require.NoError(t, os.Chmod(key, 0o600))
	g, err := loadBuzzGateway()
	require.NoError(t, err)
	assert.NotNil(t, g)
	for _, name := range []string{"KB_BUZZ_BINARY", "KB_BUZZ_RELAY_URL", "KB_BUZZ_CHANNEL", "KB_BUZZ_PRIVATE_KEY_FILE"} {
		t.Setenv(name, "")
	}
	g, err = loadBuzzGateway()
	require.NoError(t, err)
	assert.Nil(t, g)
}

func TestDecisionRequestPreviewThenApply(t *testing.T) {
	host := &fakeHostDecisions{}
	factory := func(context.Context, bool) (administration, func(), error) {
		return administration{decisions: host}, func() {}, nil
	}
	var preview bytes.Buffer

	err := runAdmin(t.Context(), []string{"decision-request", "--preview"}, &preview, strings.NewReader(`{"CandidateRevision":"rev-1","RequestedAction":"publish"}`), factory)

	require.NoError(t, err)
	assert.Contains(t, preview.String(), `"ExpectedEvidenceDigest":"`+fakeEvidenceDigest+`"`)
	assert.Empty(t, host.requested, "preview must not record a decision")

	for _, tc := range []struct {
		name, input string
		wantErr     error
	}{
		{"apply requires the previewed digest", `{"CandidateRevision":"rev-1","RequestedAction":"publish"}`, domain.ErrInvalidInput},
		{"changed evidence is stale", `{"CandidateRevision":"rev-1","RequestedAction":"publish","ExpectedEvidenceDigest":"` + strings.Repeat("2", 64) + `"}`, domain.ErrStale},
		{"preview fields are required", `{"RequestedAction":"publish","ExpectedEvidenceDigest":"` + fakeEvidenceDigest + `"}`, domain.ErrInvalidInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := runAdmin(t.Context(), []string{"decision-request"}, io.Discard, strings.NewReader(tc.input), factory)

			assert.ErrorIs(t, err, tc.wantErr)
		})
	}

	var applied bytes.Buffer
	err = runAdmin(t.Context(), []string{"decision-request"}, &applied, strings.NewReader(`{"CandidateRevision":"rev-1","RequestedAction":"publish","ExpectedEvidenceDigest":"`+fakeEvidenceDigest+`"}`), factory)

	require.NoError(t, err)
	require.Len(t, host.requested, 1)
	assert.Equal(t, domain.Decision{CandidateRevision: "rev-1", RequestedAction: "publish", EvidenceDigest: fakeEvidenceDigest}, host.requested[0])
	assert.Contains(t, applied.String(), `"dec-new"`)
}

func TestJudgmentsCommandPrintsTheReviewPage(t *testing.T) {
	host := &fakeHostDecisions{}
	factory := func(context.Context, bool) (administration, func(), error) {
		return administration{decisions: host}, func() {}, nil
	}
	key := strings.Repeat("ab", 32)
	var out bytes.Buffer

	err := runAdmin(t.Context(), []string{"judgments", "--signer", key, "--since", "2026-11-01", "--limit", "20", "--after", "c"}, &out, strings.NewReader(""), factory)

	require.NoError(t, err)
	assert.Equal(t, domain.JudgmentQuery{Signer: key, Since: "2026-11-01", Limit: 20, After: "c"}, host.judgmentQuery)
	assert.Contains(t, out.String(), `"signer":"`+key+`"`)
	assert.Contains(t, out.String(), `"next":"cursor-2"`)

	require.NoError(t, runAdmin(t.Context(), []string{"judgments"}, &out, strings.NewReader(""), factory))
	assert.Equal(t, domain.JudgmentQuery{Limit: 100}, host.judgmentQuery, "defaults to 100 from the first judgment")

	for _, bad := range [][]string{
		{"judgments", "--limit", "0"},
		{"judgments", "--limit", "501"},
		{"judgments", "--since", "yesterday"},
		{"judgments", "--input", "-"},
		{"judgments", "extra"},
	} {
		assert.ErrorIs(t, runAdmin(t.Context(), bad, io.Discard, strings.NewReader(""), factory), domain.ErrInvalidInput, "%v", bad)
	}
	assert.False(t, requiresRecovery("judgments", false), "the review works while kb is in maintenance")
}
