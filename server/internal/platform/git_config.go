package platform

import (
	"os"
	"strings"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

// LoadGitRepositories reads the owner-approved repository mapping used by
// automatic verification, as comma-separated id=/absolute/dir entries in
// KB_GIT_REPOSITORIES. Unset means no repository may be read. The Git gateway
// validates IDs and directories; this only parses the setting.
func LoadGitRepositories() (map[string]string, error) {
	raw := os.Getenv("KB_GIT_REPOSITORIES")
	out := map[string]string{}
	if raw == "" {
		return out, nil
	}
	for _, entry := range strings.Split(raw, ",") {
		id, dir, ok := strings.Cut(entry, "=")
		if !ok || id == "" || dir == "" {
			return nil, domain.ErrInvalidInput
		}
		if _, dup := out[id]; dup {
			return nil, domain.ErrInvalidInput
		}
		out[id] = dir
	}
	return out, nil
}
