package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeHostRecovery struct {
	calls int
	err   error
}

func (f *fakeHostRecovery) Recover(context.Context) error { f.calls++; return f.err }

func TestHostAdminI1AndIdentityCommandsRefuseUnreplayedJournal(t *testing.T) {
	for _, command := range []string{"verify", "reject", "invalidate", "enroll", "revoke", "list"} {
		for _, preview := range []bool{false, true} {
			if preview && (command == "enroll" || command == "revoke" || command == "list") {
				continue
			}
			t.Run(fmt.Sprintf("%s-preview-%t", command, preview), func(t *testing.T) {
				f := &fakeHostRecovery{err: domain.ErrUnavailable}
				adjudication := &fakeHostAdjudication{}
				identity := &fakeHostIdentity{}
				args := []string{command}
				if preview {
					args = append(args, "--preview")
				}
				if command == "revoke" {
					args = append(args, "--client-id", "client")
				}
				if command == "enroll" {
					fd, err := os.CreateTemp(t.TempDir(), "credential")
					require.NoError(t, err)
					t.Cleanup(func() { _ = fd.Close() })
					args = append(args, "--project", "project", "--credential-fd", fmt.Sprint(fd.Fd()))
				}
				var out bytes.Buffer
				err := runAdmin(t.Context(), args, &out, strings.NewReader(`{}`), func(context.Context, bool) (administration, func(), error) {
					return administration{recovery: f, adjudication: adjudication, identity: identity}, func() {}, nil
				})
				assert.ErrorIs(t, err, domain.ErrUnavailable)
				assert.Equal(t, 1, f.calls)
				assert.Zero(t, adjudication.previewCalls+adjudication.decisionCalls+adjudication.invalidationCalls)
				assert.Empty(t, identity.revoked)
				assert.Empty(t, identity.input.ProjectIDs)
				assert.Empty(t, out.String())
			})
		}
	}
}

func TestHostAdminSetupBootstrapsWithoutJournalAndKeepsProtectedFDTransfer(t *testing.T) {
	f := &fakeHostRecovery{err: domain.ErrUnavailable}
	identity := &fakeHostIdentity{credential: service.ClientCredential{Bearer: "fixture-scoped-secret"}}
	fd, err := os.CreateTemp(t.TempDir(), "credential")
	require.NoError(t, err)
	t.Cleanup(func() { _ = fd.Close() })
	var out bytes.Buffer
	err = runAdmin(t.Context(), []string{"setup", "--owner-id", "owner", "--owner-public-key", strings.Repeat("a", 64), "--project", "project", "--credential-fd", fmt.Sprint(fd.Fd())}, &out, strings.NewReader(""), func(_ context.Context, withObjects bool) (administration, func(), error) {
		assert.False(t, withObjects)
		return administration{identity: identity, recovery: f}, func() {}, nil
	})
	require.NoError(t, err)
	assert.Zero(t, f.calls)
	assert.Empty(t, out.String())
	_, err = fd.Seek(0, 0)
	require.NoError(t, err)
	raw, err := io.ReadAll(fd)
	require.NoError(t, err)
	assert.Equal(t, "fixture-scoped-secret\n", string(raw))
}
