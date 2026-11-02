package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeHostLifecycle struct {
	recoveryErr                         error
	selection                           domain.DeleteSelection
	project, expectedRevision, statusID string
	policy                              domain.Policy
	calls                               []string
	err                                 error
}

func (f *fakeHostLifecycle) PreviewHost(_ context.Context, input domain.DeleteSelection) (domain.DeletionPreview, error) {
	f.calls = append(f.calls, "preview")
	f.selection = input
	return domain.DeletionPreview{Manifest: domain.DeletionManifest{ProjectID: input.ProjectID, IDs: []string{"original", "identical-copy", "derived-excerpt"}}, SelectionDigest: "exact-expanded-digest"}, f.err
}
func (f *fakeHostLifecycle) DeleteHost(_ context.Context, input domain.DeleteSelection) (domain.DeletionReceipt, error) {
	f.calls = append(f.calls, "delete")
	f.selection = input
	return domain.DeletionReceipt{ID: "operation", State: "purge_pending", ManifestDigest: input.ExpectedSelectionDigest, BackupState: "expiry_pending"}, f.err
}
func (f *fakeHostLifecycle) DeletionStatusHost(_ context.Context, id string) (domain.DeletionReceipt, error) {
	f.calls = append(f.calls, "status")
	f.statusID = id
	return domain.DeletionReceipt{ID: id, State: "pending_journal", BackupState: "expiry_pending"}, f.err
}
func (f *fakeHostLifecycle) PolicyHost(_ context.Context, project string) (domain.Policy, error) {
	f.calls = append(f.calls, "policy")
	f.project = project
	return domain.Policy{Revision: "current-revision"}, f.err
}
func (f *fakeHostLifecycle) UpdatePolicyHost(_ context.Context, project, expected string, policy domain.Policy) (domain.Policy, error) {
	f.calls = append(f.calls, "policy-update")
	f.project, f.expectedRevision, f.policy = project, expected, policy
	return policy, f.err
}
func (f *fakeHostLifecycle) InitializeJournalHost(context.Context) (domain.JournalBarrier, error) {
	f.calls = append(f.calls, "journal-init")
	return domain.JournalBarrier{Namespace: "opaque-namespace", Digest: "authenticated-head"}, f.err
}
func (f *fakeHostLifecycle) CheckJournalHost(context.Context) (domain.JournalBarrier, error) {
	f.calls = append(f.calls, "journal-check")
	return domain.JournalBarrier{Namespace: "opaque-namespace", Sequence: 2, Digest: "authenticated-head"}, f.err
}
func lifecycleFactory(host hostLifecycle) adminFactory {
	return func(context.Context, bool) (administration, func(), error) {
		recovery, _ := host.(hostRecovery)
		return administration{lifecycle: host, recovery: recovery}, func() {}, nil
	}
}

func TestHostLifecyclePreviewReturnsEntireExpandedConfirmation(t *testing.T) {
	f := &fakeHostLifecycle{}
	var out bytes.Buffer
	err := runAdmin(t.Context(), []string{"delete", "--preview"}, &out, strings.NewReader(`{"ProjectID":"project","IDs":["original"],"SessionIDs":["session"],"TaskIDs":["task"]}`), lifecycleFactory(f))
	require.NoError(t, err)
	assert.Equal(t, []string{"recover", "preview"}, f.calls)
	assert.Equal(t, domain.DeleteSelection{ProjectID: "project", IDs: []string{"original"}, SessionIDs: []string{"session"}, TaskIDs: []string{"task"}}, f.selection)
	var preview domain.DeletionPreview
	require.NoError(t, json.Unmarshal(out.Bytes(), &preview))
	assert.Equal(t, []string{"original", "identical-copy", "derived-excerpt"}, preview.Manifest.IDs)
	assert.Equal(t, "exact-expanded-digest", preview.SelectionDigest)
}
func TestHostLifecycleExecutionPreservesConfirmationAndIdempotency(t *testing.T) {
	f := &fakeHostLifecycle{}
	var out bytes.Buffer
	err := runAdmin(t.Context(), []string{"delete"}, &out, strings.NewReader(`{"ProjectID":"project","IDs":["original"],"ExpectedSelectionDigest":"exact-expanded-digest","IdempotencyKey":"operation-key"}`), lifecycleFactory(f))
	require.NoError(t, err)
	assert.Equal(t, []string{"delete"}, f.calls)
	assert.Equal(t, "exact-expanded-digest", f.selection.ExpectedSelectionDigest)
	assert.Equal(t, "operation-key", f.selection.IdempotencyKey)
	assert.Contains(t, out.String(), `"State":"purge_pending"`)
	assert.Contains(t, out.String(), `"BackupState":"expiry_pending"`)
}
func TestHostLifecycleStatusReadsOpaqueOperation(t *testing.T) {
	f := &fakeHostLifecycle{}
	var out bytes.Buffer
	err := runAdmin(t.Context(), []string{"deletion-status"}, &out, strings.NewReader(`{"ID":"operation"}`), lifecycleFactory(f))
	require.NoError(t, err)
	assert.Equal(t, []string{"status"}, f.calls)
	assert.Equal(t, "operation", f.statusID)
	assert.Contains(t, out.String(), `"State":"pending_journal"`)
}
func TestHostLifecyclePolicyInspectionAndCASUseExactInputs(t *testing.T) {
	t.Run("inspection", func(t *testing.T) {
		f := &fakeHostLifecycle{}
		var out bytes.Buffer
		err := runAdmin(t.Context(), []string{"policy"}, &out, strings.NewReader(`{"ProjectID":"project"}`), lifecycleFactory(f))
		require.NoError(t, err)
		assert.Equal(t, []string{"recover", "policy"}, f.calls)
		assert.Equal(t, "project", f.project)
		assert.Contains(t, out.String(), `"Revision":"current-revision"`)
	})
	t.Run("CAS update", func(t *testing.T) {
		f := &fakeHostLifecycle{}
		var out bytes.Buffer
		err := runAdmin(t.Context(), []string{"policy-update"}, &out, strings.NewReader(`{"ProjectID":"project","ExpectedRevision":"current-revision","Policy":{"Revision":"new-revision","Paused":true,"RawHistoryRetention":7776000000000000}}`), lifecycleFactory(f))
		require.NoError(t, err)
		assert.Equal(t, []string{"recover", "policy-update"}, f.calls)
		assert.Equal(t, "project", f.project)
		assert.Equal(t, "current-revision", f.expectedRevision)
		assert.Equal(t, "new-revision", f.policy.Revision)
		assert.True(t, f.policy.Paused)
		assert.EqualValues(t, 7776000000000000, f.policy.RawHistoryRetention)
	})
}
func TestHostLifecycleJournalCheckNeverInitializes(t *testing.T) {
	for _, command := range []string{"journal-check", "journal-init"} {
		t.Run(command, func(t *testing.T) {
			f := &fakeHostLifecycle{}
			var out bytes.Buffer
			err := runAdmin(t.Context(), []string{command}, &out, strings.NewReader(""), lifecycleFactory(f))
			require.NoError(t, err)
			assert.Equal(t, []string{command}, f.calls)
			assert.Contains(t, out.String(), `"Digest":"authenticated-head"`)
		})
	}
}
func TestHostLifecycleStrictJSONRejectsBeforeComposition(t *testing.T) {
	for _, command := range []string{"delete", "deletion-status", "policy", "policy-update"} {
		for i, input := range []string{`{"OwnerToken":"fixture-secret"}`, `{} {}`, strings.Repeat(" ", maxAdminInput+1)} {
			t.Run(fmt.Sprintf("%s-%d", command, i), func(t *testing.T) {
				called := false
				var out bytes.Buffer
				err := runAdmin(t.Context(), []string{command}, &out, strings.NewReader(input), func(context.Context, bool) (administration, func(), error) {
					called = true
					return administration{}, func() {}, nil
				})
				require.ErrorIs(t, err, domain.ErrInvalidInput)
				assert.False(t, called)
				assert.Empty(t, out.String())
			})
		}
	}
}
func TestHostLifecycleRejectsCredentialsAndJournalInputFlags(t *testing.T) {
	for _, args := range [][]string{{"delete", "--token", "fixture-secret"}, {"policy-update", "--owner-id", "forged"}, {"journal-init", "--input", "-"}, {"journal-check", "--preview"}} {
		called := false
		var out bytes.Buffer
		err := runAdmin(t.Context(), args, &out, strings.NewReader("{}"), func(context.Context, bool) (administration, func(), error) {
			called = true
			return administration{}, func() {}, nil
		})
		require.ErrorIs(t, err, domain.ErrInvalidInput)
		assert.False(t, called)
		assert.Empty(t, out.String())
		assert.NotContains(t, safeError(err), "fixture-secret")
	}
}
func TestHostLifecycleReadsInputFileWithoutEchoingPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operator-selection.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"ProjectID":"project","IDs":["original"]}`), 0600))
	f := &fakeHostLifecycle{}
	var out bytes.Buffer
	err := runAdmin(t.Context(), []string{"delete", "--preview", "--input", path}, &out, strings.NewReader("not used"), lifecycleFactory(f))
	require.NoError(t, err)
	assert.Equal(t, "project", f.selection.ProjectID)
	assert.NotContains(t, out.String(), path)
}
func TestHostLifecycleFailedDurabilityDoesNotPrintSuccessfulReceipt(t *testing.T) {
	for _, cause := range []error{domain.ErrUnknownOutcome, domain.ErrStale, domain.ErrConflict, domain.ErrUnavailable} {
		f := &fakeHostLifecycle{err: fmt.Errorf("fixture-secret: %w", cause)}
		var out bytes.Buffer
		err := runAdmin(t.Context(), []string{"delete"}, &out, strings.NewReader(`{"ProjectID":"project","ExpectedSelectionDigest":"confirmed","IdempotencyKey":"retry-same-key","IDs":["original"]}`), lifecycleFactory(f))
		require.ErrorIs(t, err, cause)
		assert.Empty(t, out.String())
		assert.NotContains(t, safeError(commandError{command: "delete", cause: err}), "fixture-secret")
	}
}
func TestHostLifecycleNilServiceFailsClosed(t *testing.T) {
	err := runAdmin(t.Context(), []string{"journal-check"}, io.Discard, strings.NewReader(""), lifecycleFactory(nil))
	assert.ErrorIs(t, err, domain.ErrUnavailable)
}

func TestHostLifecycleSafeErrorsExplainConfirmationAndUnknownDurability(t *testing.T) {
	cases := []struct {
		cause error
		want  string
	}{
		{domain.ErrStale, "stale: expected state changed; preview or inspect current state before retry"},
		{domain.ErrUnknownOutcome, "unknown_outcome: retry the exact request with the same idempotency key or inspect status"},
	}
	for _, tc := range cases {
		assert.Equal(t, "delete: "+tc.want, safeError(commandError{command: "delete", cause: fmt.Errorf("fixture-secret: %w", tc.cause)}))
	}
}

func (f *fakeHostLifecycle) Recover(context.Context) error {
	f.calls = append(f.calls, "recover")
	return f.recoveryErr
}

func TestHostLifecycleRecoveryBarrierFailsClosedBeforeSourceInspectionAndPolicyChanges(t *testing.T) {
	for _, args := range [][]string{{"delete", "--preview"}, {"policy"}, {"policy-update"}} {
		t.Run(args[0], func(t *testing.T) {
			f := &fakeHostLifecycle{recoveryErr: domain.ErrUnavailable}
			var out bytes.Buffer
			err := runAdmin(t.Context(), args, &out, strings.NewReader(`{}`), lifecycleFactory(f))
			assert.ErrorIs(t, err, domain.ErrUnavailable)
			assert.Equal(t, []string{"recover"}, f.calls)
			assert.Empty(t, out.String())
		})
	}
}

func TestHostLifecycleRepairAndDiagnosisRemainAvailableWithoutReadiness(t *testing.T) {
	for _, args := range [][]string{{"delete"}, {"deletion-status"}, {"journal-init"}, {"journal-check"}} {
		t.Run(args[0], func(t *testing.T) {
			f := &fakeHostLifecycle{recoveryErr: domain.ErrUnavailable}
			var out bytes.Buffer
			err := runAdmin(t.Context(), args, &out, strings.NewReader(`{}`), lifecycleFactory(f))
			require.NoError(t, err)
			assert.NotContains(t, f.calls, "recover")
			assert.NotEmpty(t, out.String())
		})
	}
}
