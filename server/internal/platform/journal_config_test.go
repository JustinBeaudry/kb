package platform

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setJournalEnvironment(t *testing.T) string {
	t.Helper()
	keyring := filepath.Join(t.TempDir(), "keys.json")
	key := base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901"))
	require.NoError(t, os.WriteFile(keyring, []byte(`{"key-1":"`+key+`"}`), 0600))
	for name, value := range map[string]string{"KB_JOURNAL_ENDPOINT": "https://journal.example.test", "KB_JOURNAL_REGION": "local", "KB_JOURNAL_BUCKET": "journal", "KB_JOURNAL_PREFIX": "deletions", "KB_JOURNAL_NAMESPACE": "namespace-1", "KB_JOURNAL_ACCESS_KEY": "journal-access", "KB_JOURNAL_SECRET_KEY": "journal-secret", "KB_JOURNAL_ADDRESSING_STYLE": "path", "KB_JOURNAL_KEY_ID": "key-1", "KB_JOURNAL_KEYRING_FILE": keyring} {
		t.Setenv(name, value)
	}
	return keyring
}
func TestJournalConfigLoadsIndependentOwnerOnlyKeyring(t *testing.T) {
	setJournalEnvironment(t)
	t.Setenv("KB_S3_SECRET_KEY", "evidence-secret")
	cfg, err := LoadJournalConfig()
	require.NoError(t, err)
	assert.Equal(t, "journal", cfg.Bucket)
	assert.Equal(t, "journal-secret", cfg.SecretKey)
	assert.Len(t, cfg.Keys, 1)
	assert.NotContains(t, fmt.Sprintf("%+v %#v", cfg, cfg), "journal-secret")
	assert.NotContains(t, fmt.Sprintf("%+v %#v", cfg, cfg), "0123456789")
}
func TestJournalConfigRejectsUnsafeOrMissingKeys(t *testing.T) {
	for _, tc := range []struct {
		name, contents string
		mode           os.FileMode
	}{
		{"world readable", `{"key-1":"MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE="}`, 0644},
		{"short", `{"key-1":"c2hvcnQ="}`, 0600},
		{"duplicate", `{"key-1":"MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=","key-1":"MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE="}`, 0600},
		{"unknown active", `{"other":"MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE="}`, 0600},
		{"trailing", `{} {}`, 0600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := setJournalEnvironment(t)
			require.NoError(t, os.WriteFile(path, []byte(tc.contents), tc.mode))
			require.NoError(t, os.Chmod(path, tc.mode))
			_, err := LoadJournalConfig()
			assert.ErrorIs(t, err, domain.ErrInvalidInput)
		})
	}
}
func TestJournalConfigNeverFallsBackToEvidenceConfig(t *testing.T) {
	setJournalEnvironment(t)
	t.Setenv("KB_JOURNAL_SECRET_KEY", "")
	t.Setenv("KB_S3_SECRET_KEY", "evidence-secret")
	_, err := LoadJournalConfig()
	assert.ErrorIs(t, err, domain.ErrInvalidInput)
}
func TestJournalConfigRejectsSymlinkKeyring(t *testing.T) {
	path := setJournalEnvironment(t)
	link := filepath.Join(t.TempDir(), "link.json")
	require.NoError(t, os.Symlink(path, link))
	t.Setenv("KB_JOURNAL_KEYRING_FILE", link)
	_, err := LoadJournalConfig()
	assert.ErrorIs(t, err, domain.ErrInvalidInput)
}
