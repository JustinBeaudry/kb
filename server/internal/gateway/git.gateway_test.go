package gateway_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func gitRepo(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	run("init", "-q")
	for name, body := range files {
		path := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	}
	run("add", "-A")
	run("commit", "-q", "-m", "fixture")
	return dir, run("rev-parse", "HEAD")
}

func TestGitSourceReaderReadsApprovedSnapshot(t *testing.T) {
	manifest := `{"dependencies":{"left-pad":"1.3.0"}}`
	dir, head := gitRepo(t, map[string]string{"package.json": manifest, "nested/doc.txt": "contract v2"})
	reader, err := gateway.NewGitSourceReader(map[string]string{"demo": dir})
	require.NoError(t, err)

	snap, err := reader.ReadSnapshot(t.Context(), "demo", head, "package.json")

	require.NoError(t, err)
	sum := sha256.Sum256([]byte(manifest))
	assert.Equal(t, manifest, string(snap.Bytes))
	assert.Equal(t, hex.EncodeToString(sum[:]), snap.Digest)
	assert.Equal(t, head, snap.Revision)
	assert.Equal(t, "git:demo:package.json", snap.Locator)
	assert.Equal(t, domain.TrustGatewayObserved, snap.Origin.Trust)
	assert.Equal(t, "gateway:git", snap.Origin.ClientID)
	assert.NotEmpty(t, snap.Origin.AttestationID)
}

func TestGitSourceReaderRejectsUnapprovedInputs(t *testing.T) {
	dir, head := gitRepo(t, map[string]string{"a.txt": "a"})
	reader, err := gateway.NewGitSourceReader(map[string]string{"demo": dir})
	require.NoError(t, err)
	cases := []struct {
		name, repo, revision, path string
		want                       error
	}{
		{"unmapped repository", "other", head, "a.txt", domain.ErrForbidden},
		{"url as repository", "https://example.invalid/x.git", head, "a.txt", domain.ErrForbidden},
		{"symbolic revision", "demo", "HEAD", "a.txt", domain.ErrInvalidInput},
		{"option injection revision", "demo", "--output=/tmp/x", "a.txt", domain.ErrInvalidInput},
		{"absolute path", "demo", head, "/etc/passwd", domain.ErrInvalidInput},
		{"parent traversal", "demo", head, "../a.txt", domain.ErrInvalidInput},
		{"option path", "demo", head, "-a", domain.ErrInvalidInput},
		{"missing file", "demo", head, "missing.txt", domain.ErrIncomplete},
		{"unknown commit", "demo", strings.Repeat("a", 40), "a.txt", domain.ErrIncomplete},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := reader.ReadSnapshot(t.Context(), tc.repo, tc.revision, tc.path)
			assert.ErrorIs(t, err, tc.want)
		})
	}
}

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return strings.TrimSpace(string(out))
}

func TestGitSourceReaderRequiresCommitRevision(t *testing.T) {
	dir, head := gitRepo(t, map[string]string{"a.txt": "a"})
	tree := gitCmd(t, dir, "rev-parse", head+"^{tree}")
	blob := gitCmd(t, dir, "rev-parse", head+":a.txt")
	reader, err := gateway.NewGitSourceReader(map[string]string{"demo": dir})
	require.NoError(t, err)
	cases := []struct {
		name, revision string
		want           error
	}{
		{"tree id", tree, domain.ErrInvalidInput},
		{"blob id", blob, domain.ErrInvalidInput},
		{"commit id", head, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap, err := reader.ReadSnapshot(t.Context(), "demo", tc.revision, "a.txt")

			if tc.want != nil {
				assert.ErrorIs(t, err, tc.want)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, head, snap.Revision)
		})
	}
}

func TestGitSourceReaderOpensOnlyTheMappedRepository(t *testing.T) {
	outer, head := gitRepo(t, map[string]string{"secret.txt": "outer", "sub/keep.txt": "k"})
	bare := t.TempDir()
	gitCmd(t, bare, "clone", "-q", "--bare", outer, ".")
	linked := filepath.Join(t.TempDir(), "linked")
	gitCmd(t, outer, "worktree", "add", "-q", "--detach", linked, head)
	cases := []struct {
		name, dir string
		want      error
	}{
		{"subdirectory of a repository", filepath.Join(outer, "sub"), domain.ErrUnavailable},
		{"not a repository", t.TempDir(), domain.ErrUnavailable},
		{"bare repository", bare, nil},
		{"linked worktree", linked, nil},
		{"top level", outer, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader, err := gateway.NewGitSourceReader(map[string]string{"demo": tc.dir})
			require.NoError(t, err)

			snap, err := reader.ReadSnapshot(t.Context(), "demo", head, "secret.txt")

			if tc.want != nil {
				assert.ErrorIs(t, err, tc.want)
				assert.Empty(t, snap.Bytes)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "outer", string(snap.Bytes))
		})
	}
}

func TestGitSourceReaderRejectsInvalidMapping(t *testing.T) {
	cases := map[string]map[string]string{
		"relative path": {"demo": "relative/path"},
		"bad id":        {"Bad/ID": t.TempDir()},
		"url":           {"demo": "https://example.invalid/repo.git"},
	}
	for name, mapping := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := gateway.NewGitSourceReader(mapping)
			assert.ErrorIs(t, err, domain.ErrInvalidInput)
		})
	}
}

func TestParseGitLocator(t *testing.T) {
	repo, path, ok := domain.ParseGitLocator("git:demo:dir/a:b.txt")
	assert.True(t, ok)
	assert.Equal(t, "demo", repo)
	assert.Equal(t, "dir/a:b.txt", path)
	_, _, ok = domain.ParseGitLocator("file.txt#bytes")
	assert.False(t, ok)
}
