package gateway

import (
	"filippo.io/age"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestBackupReadinessRefusesMissingRetainedDecryptionKey(t *testing.T) {
	a, s, c, m, data, system := archiveSetup(t)
	require.NoError(t, a.Write(t.Context(), m, data, system))
	next, e := age.GenerateX25519Identity()
	require.NoError(t, e)
	c.Recipients = map[string]string{"new": next.Recipient().String()}
	c.Identities = map[string]string{"new": next.String()}
	rotated, e := NewBackupArchive(s, s, c)
	require.NoError(t, e)
	require.Error(t, rotated.CheckRecoveryKeys(t.Context()))
	require.NoError(t, a.CheckRecoveryKeys(t.Context()))
}
