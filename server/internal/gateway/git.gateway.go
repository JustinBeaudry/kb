package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	gitRepoID   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	gitObjectID = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
	gitSafeEnv  = []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C"}
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

func (g *GitSourceReader) run(ctx context.Context, dir string, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, g.git, append([]string{"-C", dir, "--no-pager"}, args...)...)
	// The ceiling stops repository discovery at the mapped directory, so a
	// mapping that is not itself a repository never opens an enclosing one.
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GIT_CEILING_DIRECTORIES=" + filepath.Dir(dir)}, gitSafeEnv...)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		// Any process failure is a repository or host fault, never missing
		// evidence; absent objects are reported in batch output instead.
		return nil, domain.ErrUnavailable
	}
	return stdout.Bytes(), nil
}

type gitObject struct {
	id, kind string
	size     int64
}

// describe resolves each object name with one batch-check. A missing object is
// reported in output (kind "missing") rather than as a process failure, so it
// maps to ErrIncomplete while repository failures stay ErrUnavailable.
func (g *GitSourceReader) describe(ctx context.Context, dir string, names ...string) ([]gitObject, error) {
	out, err := g.run(ctx, dir, []byte(strings.Join(names, "\n")+"\n"), "cat-file", "--batch-check")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) != len(names) {
		return nil, domain.ErrUnavailable
	}
	objects := make([]gitObject, len(names))
	for i, line := range lines {
		if line == names[i]+" missing" || line == names[i]+" ambiguous" {
			objects[i] = gitObject{kind: "missing"}
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 || !gitObjectID.MatchString(fields[0]) {
			return nil, domain.ErrUnavailable
		}
		n, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || n < 0 {
			return nil, domain.ErrUnavailable
		}
		objects[i] = gitObject{id: fields[0], kind: fields[1], size: n}
	}
	return objects, nil
}

// ReadSnapshot returns the exact blob at a full commit ID. Only the approved
// repository mapping is consulted; symbolic refs and non-commit objects are
// rejected so the observed revision is exactly the requested commit.
func (g *GitSourceReader) ReadSnapshot(ctx context.Context, repoID, revision, p string) (domain.SourceSnapshot, error) {
	dir, ok := g.repos[repoID]
	if !ok {
		return domain.SourceSnapshot{}, domain.ErrForbidden
	}
	if !gitObjectID.MatchString(revision) || !validGitPath(p) {
		return domain.SourceSnapshot{}, domain.ErrInvalidInput
	}
	objects, err := g.describe(ctx, dir, revision, revision+":"+p)
	if err != nil {
		return domain.SourceSnapshot{}, err
	}
	commit, blob := objects[0], objects[1]
	if commit.kind == "missing" {
		return domain.SourceSnapshot{}, domain.ErrIncomplete
	}
	if commit.kind != "commit" || commit.id != revision {
		return domain.SourceSnapshot{}, domain.ErrInvalidInput
	}
	if blob.kind != "blob" {
		return domain.SourceSnapshot{}, domain.ErrIncomplete
	}
	if blob.size > domain.MaxArtifactBytes {
		return domain.SourceSnapshot{}, domain.ErrBudget
	}
	data, err := g.run(ctx, dir, nil, "cat-file", "blob", blob.id)
	if err != nil {
		return domain.SourceSnapshot{}, err
	}
	if int64(len(data)) != blob.size {
		return domain.SourceSnapshot{}, domain.ErrUnavailable
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	locator := domain.GitLocator(repoID, p)
	attestation := sha256.Sum256([]byte(strings.Join([]string{repoID, revision, p, digest}, "\x00")))
	return domain.SourceSnapshot{Bytes: data, Digest: digest, Locator: locator, Revision: revision, RepositoryID: repoID, Path: p, Origin: domain.Origin{ClientID: "gateway:git", Trust: domain.TrustGatewayObserved, AttestationID: hex.EncodeToString(attestation[:])}}, nil
}
