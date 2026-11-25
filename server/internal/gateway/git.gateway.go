package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

var (
	gitRepoID    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	gitObjectID  = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
	gitSafeEnv   = []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C"}
	errGitAbsent = errors.New("git object absent")
)

// GitSourceReader reads exact blobs from an owner-approved mapping of local
// repositories. It never accepts URLs or caller paths, never fetches, and runs
// only fixed read-only plumbing commands.
type GitSourceReader struct {
	repos map[string]string
	git   string
}

func NewGitSourceReader(mapping map[string]string) (*GitSourceReader, error) {
	repos := make(map[string]string, len(mapping))
	for id, dir := range mapping {
		if !gitRepoID.MatchString(id) || !filepath.IsAbs(dir) || strings.Contains(dir, "://") {
			return nil, domain.ErrInvalidInput
		}
		repos[id] = filepath.Clean(dir)
	}
	bin, err := exec.LookPath("git")
	if err != nil {
		return nil, domain.ErrUnavailable
	}
	return &GitSourceReader{repos: repos, git: bin}, nil
}

func validGitPath(p string) bool {
	return p != "" && len(p) <= 4096 && !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "-") && path.Clean(p) == p && p != "." && !strings.HasPrefix(p, "../") && p != ".." && !strings.ContainsAny(p, "\x00\n\\")
}

func (g *GitSourceReader) run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, g.git, append([]string{"-C", dir, "--no-pager"}, args...)...)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir}, gitSafeEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, domain.ErrUnavailable
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, errGitAbsent
		}
		return nil, domain.ErrUnavailable
	}
	return stdout.Bytes(), nil
}

// ReadSnapshot returns the exact blob at a full commit ID. Only the approved
// repository mapping is consulted; symbolic refs are rejected so the observed
// revision is exactly the requested one.
func (g *GitSourceReader) ReadSnapshot(ctx context.Context, repoID, revision, p string) (domain.SourceSnapshot, error) {
	dir, ok := g.repos[repoID]
	if !ok {
		return domain.SourceSnapshot{}, domain.ErrForbidden
	}
	if !gitObjectID.MatchString(revision) || !validGitPath(p) {
		return domain.SourceSnapshot{}, domain.ErrInvalidInput
	}
	object := revision + ":" + p
	size, err := g.run(ctx, dir, "cat-file", "-s", object)
	if err != nil {
		return domain.SourceSnapshot{}, absent(err)
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(size)), 10, 64)
	if err != nil || n < 0 {
		return domain.SourceSnapshot{}, domain.ErrUnavailable
	}
	if n > domain.MaxArtifactBytes {
		return domain.SourceSnapshot{}, domain.ErrBudget
	}
	kind, err := g.run(ctx, dir, "cat-file", "-t", object)
	if err != nil {
		return domain.SourceSnapshot{}, absent(err)
	}
	if strings.TrimSpace(string(kind)) != "blob" {
		return domain.SourceSnapshot{}, domain.ErrIncomplete
	}
	data, err := g.run(ctx, dir, "cat-file", "blob", object)
	if err != nil {
		return domain.SourceSnapshot{}, absent(err)
	}
	if int64(len(data)) != n {
		return domain.SourceSnapshot{}, domain.ErrUnavailable
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	locator := domain.GitLocator(repoID, p)
	attestation := sha256.Sum256([]byte(strings.Join([]string{repoID, revision, p, digest}, "\x00")))
	return domain.SourceSnapshot{Bytes: data, Digest: digest, Locator: locator, Revision: revision, RepositoryID: repoID, Path: p, Origin: domain.Origin{ClientID: "gateway:git", Trust: domain.TrustGatewayObserved, AttestationID: hex.EncodeToString(attestation[:])}}, nil
}

func absent(err error) error {
	if errors.Is(err, errGitAbsent) {
		return domain.ErrIncomplete
	}
	return err
}
