package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHelpDescribesProtectedOneTimeSetup(t *testing.T) {
	var output bytes.Buffer
	require.NoError(t, run(t.Context(), []string{"help"}, &output))
	for _, want := range []string{"controlled service host", "public signing identity", "second setup", "credential-fd", "recovery_unavailable", "No owner bearer", "64_HEX_NOSTR_PUBLIC_KEY", "BIP-340 secp256k1", "No private key"} {
		assert.Contains(t, output.String(), want)
	}
	assert.NotContains(t, output.String(), "--token")
	assert.NotContains(t, output.String(), "ED25519")
}
func TestProtectedCredentialDescriptor(t *testing.T) {
	for _, fd := range []int{0, 1, 2, -1, 999999} {
		t.Run(fmt.Sprintf("fd-%d", fd), func(t *testing.T) {
			_, err := protectedDescriptor(t.Context(), fd)
			assert.ErrorIs(t, err, domain.ErrInvalidInput)
		})
	}
	f, err := os.OpenFile(filepath.Join(t.TempDir(), "credential"), os.O_CREATE|os.O_RDWR, 0600)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, f.Close()) })
	protected, err := protectedDescriptor(t.Context(), int(f.Fd()))
	require.NoError(t, err)
	require.NoError(t, protected.Close())
	require.NoError(t, f.Chmod(0644))
	_, err = protectedDescriptor(t.Context(), int(f.Fd()))
	assert.ErrorIs(t, err, domain.ErrInvalidInput)
}
func TestAdminRejectsUnsupportedAndSecretArguments(t *testing.T) {
	for _, args := range [][]string{{"recover"}, {"remote"}, {"setup", "--token", "fixture-secret"}, {"setup", "--credential-fd", "1"}} {
		var out bytes.Buffer
		err := run(t.Context(), args, &out)
		require.Error(t, err)
		assert.Empty(t, out.String())
		assert.NotContains(t, safeError(err), "fixture-secret")
	}
}

func TestReadOnlyCredentialDestinationRejectedBeforeSetup(t *testing.T) {
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	file, err := protectedDescriptor(t.Context(), int(reader.Fd()))
	if file != nil {
		_ = file.Close()
	}
	assert.ErrorIs(t, err, domain.ErrInvalidInput)
}

func TestPublicNamedPipeRejectedBeforeSetup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential-pipe")
	require.NoError(t, syscall.Mkfifo(path, 0600))
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = file.Close() })
	require.NoError(t, file.Chmod(0666))
	protected, err := protectedDescriptor(t.Context(), int(file.Fd()))
	if protected != nil {
		_ = protected.Close()
	}
	assert.ErrorIs(t, err, domain.ErrInvalidInput)
}

func TestPrivateInheritedPipeAccepted(t *testing.T) {
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	protected, err := protectedDescriptor(t.Context(), int(writer.Fd()))
	require.NoError(t, err)
	require.NoError(t, protected.Close())
}

func TestHostAdminProtectedDescriptorOnly(t *testing.T) {
	for _, args := range [][]string{{"enroll", "--project", "p", "--credential-fd", "1"}, {"enroll", "--token", "secret"}, {"verify", "--text", "secret"}, {"reject", "--owner-id", "forged"}, {"list", "--limit", "101"}} {
		called := false
		var out bytes.Buffer
		err := runAdmin(t.Context(), args, &out, bytes.NewBufferString("{}"), func(context.Context, bool) (administration, func(), error) {
			called = true
			return administration{}, func() {}, nil
		})
		require.Error(t, err)
		assert.False(t, called)
		assert.Empty(t, out.String())
		assert.NotContains(t, safeError(err), "secret")
	}
}
func TestHostAdminRejectsUnknownAndTrailingJSONBeforeComposition(t *testing.T) {
	for _, input := range []string{`{"Bearer":"secret"}`, `{} {}`, strings.Repeat(" ", maxAdminInput+1)} {
		called := false
		err := runAdmin(t.Context(), []string{"verify", "--preview"}, io.Discard, strings.NewReader(input), func(context.Context, bool) (administration, func(), error) {
			called = true
			return administration{}, func() {}, nil
		})
		require.ErrorIs(t, err, domain.ErrInvalidInput)
		assert.False(t, called)
	}
}

type fakeHostIdentity struct {
	input      service.EnrollInput
	credential service.ClientCredential
	page       service.ClientPage
	revoked    string
}

func (f *fakeHostIdentity) Setup(context.Context, domain.OwnerIdentity, service.EnrollInput) (service.ClientCredential, error) {
	return f.credential, nil
}
func (f *fakeHostIdentity) EnrollHost(_ context.Context, input service.EnrollInput) (service.ClientCredential, error) {
	f.input = input
	return f.credential, nil
}
func (f *fakeHostIdentity) RevokeHost(_ context.Context, id string) error { f.revoked = id; return nil }
func (f *fakeHostIdentity) ListHost(context.Context, int, string) (service.ClientPage, error) {
	return f.page, nil
}
func TestHostAdminEnrollRevokeListUsesPersistedOwner(t *testing.T) {
	f := &fakeHostIdentity{credential: service.ClientCredential{Bearer: "fixture-scoped-secret"}}
	factory := func(context.Context, bool) (administration, func(), error) {
		return administration{identity: f}, func() {}, nil
	}
	destination, err := os.CreateTemp(t.TempDir(), "credential")
	require.NoError(t, err)
	t.Cleanup(func() { _ = destination.Close() })
	var out bytes.Buffer
	require.NoError(t, runAdmin(t.Context(), []string{"enroll", "--project", "p", "--client-id", "client", "--credential-fd", fmt.Sprint(destination.Fd())}, &out, strings.NewReader(""), factory))
	assert.Empty(t, out.String())
	assert.Equal(t, "client", f.input.ClientID)
	assert.Equal(t, []string{"p"}, f.input.ProjectIDs)
	_, err = destination.Seek(0, 0)
	require.NoError(t, err)
	raw, err := io.ReadAll(destination)
	require.NoError(t, err)
	assert.Equal(t, "fixture-scoped-secret\n", string(raw))
	require.NoError(t, runAdmin(t.Context(), []string{"revoke", "--client-id", "client"}, &out, strings.NewReader(""), factory))
	assert.Equal(t, "client", f.revoked)
}
func TestHostAdminListNeverReturnsCredentials(t *testing.T) {
	f := &fakeHostIdentity{credential: service.ClientCredential{Bearer: "fixture-scoped-secret"}}
	var out bytes.Buffer
	require.NoError(t, runAdmin(t.Context(), []string{"list"}, &out, strings.NewReader(""), func(context.Context, bool) (administration, func(), error) {
		return administration{identity: f}, func() {}, nil
	}))
	assert.JSONEq(t, `{"Clients":null}`, out.String())
	assert.NotContains(t, out.String(), "Bearer")
	assert.NotContains(t, out.String(), "fixture-scoped-secret")
}

type fakeHostAdjudication struct {
	draft                                          service.AdjudicationDraft
	decision                                       service.AdjudicationInput
	invalidation                                   service.InvalidationInput
	previewCalls, decisionCalls, invalidationCalls int
}

func (f *fakeHostAdjudication) PreviewHost(_ context.Context, d service.AdjudicationDraft) (service.AdjudicationPreview, error) {
	f.draft = d
	f.previewCalls++
	return service.AdjudicationPreview{Digest: "digest", CandidateHead: "head"}, nil
}
func (f *fakeHostAdjudication) AdjudicateHost(_ context.Context, d service.AdjudicationInput) (service.AdjudicationResult, error) {
	f.decision = d
	f.decisionCalls++
	return service.AdjudicationResult{Receipt: domain.Receipt{ID: "receipt"}}, nil
}
func (f *fakeHostAdjudication) InvalidateHost(_ context.Context, d service.InvalidationInput) (domain.Receipt, error) {
	f.invalidation = d
	f.invalidationCalls++
	return domain.Receipt{ID: "invalidation"}, nil
}
func TestHostAdminJSONDispatchPreservesExactConfirmation(t *testing.T) {
	f := &fakeHostAdjudication{}
	factory := func(_ context.Context, objects bool) (administration, func(), error) {
		require.True(t, objects)
		return administration{adjudication: f}, func() {}, nil
	}
	var out bytes.Buffer
	require.NoError(t, runAdmin(t.Context(), []string{"verify", "--preview"}, &out, strings.NewReader(`{"CandidateRevision":"revision","ExpectedCandidateHead":"head","TargetText":"selected text"}`), factory))
	assert.Equal(t, 1, f.previewCalls)
	assert.Zero(t, f.decisionCalls)
	assert.Equal(t, "verify", f.draft.Action)
	assert.NotContains(t, out.String(), "selected text")
	out.Reset()
	require.NoError(t, runAdmin(t.Context(), []string{"verify"}, &out, strings.NewReader(`{"ExpectedDigest":"confirmed","ExpectedCandidateHead":"head","TargetText":"selected text"}`), factory))
	assert.Equal(t, "confirmed", f.decision.ExpectedDigest)
	assert.Equal(t, "head", f.decision.ExpectedCandidateHead)
	assert.Equal(t, "selected text", f.decision.TargetText)
	assert.NotContains(t, out.String(), "selected text")
	require.NoError(t, runAdmin(t.Context(), []string{"reject"}, io.Discard, strings.NewReader(`{"ExpectedDigest":"reject-digest"}`), factory))
	assert.Equal(t, "reject", f.decision.Action)
	require.NoError(t, runAdmin(t.Context(), []string{"invalidate"}, io.Discard, strings.NewReader(`{"Revision":"support","ExpectedDigest":"support-digest"}`), factory))
	assert.Equal(t, "support-digest", f.invalidation.ExpectedDigest)
}
func TestHostAdminRejectsActionMismatchBeforeComposition(t *testing.T) {
	called := false
	err := runAdmin(t.Context(), []string{"reject"}, io.Discard, strings.NewReader(`{"Action":"verify"}`), func(context.Context, bool) (administration, func(), error) {
		called = true
		return administration{}, func() {}, nil
	})
	require.ErrorIs(t, err, domain.ErrInvalidInput)
	assert.False(t, called)
}

func TestHostAdminSafeErrorsNeverExposeUnderlyingDetails(t *testing.T) {
	for _, cause := range []error{fmt.Errorf("fixture-secret: %w", domain.ErrConflict), fmt.Errorf("fixture-secret: %w", domain.ErrInvalidInput), fmt.Errorf("fixture-secret")} {
		message := safeError(commandError{command: "verify", cause: cause})
		assert.Contains(t, message, "verify:")
		assert.NotContains(t, message, "fixture-secret")
	}
}

func (f *fakeHostAdjudication) PreviewInvalidationHost(_ context.Context, revision string) (service.InvalidationInput, error) {
	f.invalidationCalls++
	return service.InvalidationInput{Revision: revision, ExpectedHead: "exact-head", ExpectedDigest: "exact-digest", ExpectedStatus: "verified"}, nil
}
func TestHostAdminInvalidationPreviewReturnsConfirmationWithoutMutation(t *testing.T) {
	f := &fakeHostAdjudication{}
	var out bytes.Buffer
	factory := func(context.Context, bool) (administration, func(), error) {
		return administration{adjudication: f}, func() {}, nil
	}
	require.NoError(t, runAdmin(t.Context(), []string{"invalidate", "--preview"}, &out, strings.NewReader(`{"Revision":"support"}`), factory))
	assert.Equal(t, 1, f.invalidationCalls)
	assert.Empty(t, f.invalidation.Revision)
	assert.Contains(t, out.String(), `"ExpectedDigest":"exact-digest"`)
	assert.Contains(t, out.String(), `"ExpectedHead":"exact-head"`)
	assert.Contains(t, out.String(), `"IdempotencyKey":""`)
}

func TestHostAdminInvalidationPreviewAcceptsOnlyRevision(t *testing.T) {
	called := false
	err := runAdmin(t.Context(), []string{"invalidate", "--preview"}, io.Discard, strings.NewReader(`{"Revision":"support","ExpectedDigest":"stale"}`), func(context.Context, bool) (administration, func(), error) {
		called = true
		return administration{}, func() {}, nil
	})
	require.ErrorIs(t, err, domain.ErrInvalidInput)
	assert.False(t, called)
}

func TestHostAdminRejectPreviewIsReadOnlyAndBindsRejectAction(t *testing.T) {
	f := &fakeHostAdjudication{}
	var out bytes.Buffer
	factory := func(context.Context, bool) (administration, func(), error) {
		return administration{adjudication: f}, func() {}, nil
	}
	require.NoError(t, runAdmin(t.Context(), []string{"reject", "--preview"}, &out, strings.NewReader(`{"CandidateRevision":"candidate","ExpectedCandidateHead":"head"}`), factory))
	assert.Equal(t, 1, f.previewCalls)
	assert.Zero(t, f.decisionCalls)
	assert.Equal(t, "reject", f.draft.Action)
	assert.Equal(t, "candidate", f.draft.CandidateRevision)
	assert.Contains(t, out.String(), `"Digest":"digest"`)
}
