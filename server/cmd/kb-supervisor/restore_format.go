package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

// backupFormat names what a backup's restore compatibility rests on. Bump it
// when kb changes how it writes a backup in a way an older build can't read.
const backupFormat = "kb-backup/1"

// restoreFormatDigest returns the digest restore compares between a backup and
// the running build. It covers only what changes the backup's format: kb's own
// format and the graph edition and version that write the database dump. The
// architecture and graph schema are compared separately. Go, library and
// base-image patch releases leave it
// unchanged, so a security patch doesn't stop older backups from restoring.
// The age archive format is versioned in its own header, so the encryption
// module's version is not part of it.
func restoreFormatDigest(tools []byte) (string, error) {
	var v struct {
		Graph struct {
			Edition string `json:"edition"`
			Version string `json:"version"`
		} `json:"graph"`
	}
	if err := json.Unmarshal(tools, &v); err != nil || v.Graph.Edition == "" || v.Graph.Version == "" {
		return "", domain.ErrInvalidInput
	}
	canonical, err := json.Marshal([]string{backupFormat, v.Graph.Edition, v.Graph.Version})
	if err != nil {
		return "", domain.ErrInvalidInput
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// sameRestoreFormat reports whether a backup made with digest can be restored
// by this build. Backups written before the format digest existed carry the
// digest of the whole tool manifest, and still restore while that manifest is
// unchanged.
func (r *runtimeSnapshot) sameRestoreFormat(digest string) bool {
	return digest != "" && (digest == r.metadata.ToolVersionsDigest || digest == r.legacyToolsDigest)
}
