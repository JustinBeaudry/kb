package gateway

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"filippo.io/age"
	"github.com/JustinBeaudry/kb/server/internal/domain"
)

// BackupBlobStore is scoped to the backup namespace, never evidence or journal.
// Immutable create must refuse differing existing bytes and support full reads.
type BackupBlobStore interface {
	PutImmutable(context.Context, string, io.ReadSeeker, int64, string) error
	Open(context.Context, string) (io.ReadCloser, error)
	List(context.Context, string) ([]string, error)
	Delete(context.Context, string) error
}
type BackupSourceObjects interface {
	Open(context.Context, string) (io.ReadCloser, error)
}
type BackupArchiveConfig struct {
	Namespace, ScratchRoot, AuthorityKeyID string
	AuthorityKeys                          map[string][32]byte `json:"-"`
	Recipients, Identities                 map[string]string   `json:"-"`
	MaxArchiveBytes                        int64
}

func (BackupArchiveConfig) String() string   { return "BackupArchiveConfig([redacted])" }
func (BackupArchiveConfig) GoString() string { return "BackupArchiveConfig([redacted])" }

type BackupArchiveGateway struct {
	mu         sync.Mutex
	store      BackupBlobStore
	source     BackupSourceObjects
	config     BackupArchiveConfig
	recipients []age.Recipient
	identities []age.Identity
	stage      string
	staged     domain.BackupManifest
}
type backupCompletion struct {
	Version                             int
	BackupID, Namespace, AuthorityKeyID string
	CreatedAt                           time.Time
	CipherDigest                        string
	CipherBytes                         int64
	KeyIDs                              []string
	MAC                                 string
}

func NewBackupArchive(store BackupBlobStore, source BackupSourceObjects, c BackupArchiveConfig) (*BackupArchiveGateway, error) {
	if store == nil || source == nil || !backupSafeID(c.Namespace) || !backupSafeID(c.AuthorityKeyID) || !filepath.IsAbs(c.ScratchRoot) || len(c.AuthorityKeys) == 0 || len(c.AuthorityKeys) > 16 || len(c.Recipients) == 0 || len(c.Recipients) > 16 || len(c.Identities) == 0 || len(c.Identities) > 16 {
		return nil, domain.ErrInvalidInput
	}
	if c.MaxArchiveBytes == 0 {
		c.MaxArchiveBytes = 100 << 30
	}
	if c.MaxArchiveBytes < 1024 || c.MaxArchiveBytes > 1<<40 {
		return nil, domain.ErrInvalidInput
	}
	if _, ok := c.AuthorityKeys[c.AuthorityKeyID]; !ok {
		return nil, domain.ErrInvalidInput
	}
	g := &BackupArchiveGateway{store: store, source: source, config: c}
	for _, id := range sortedBackupKeys(c.Recipients) {
		if !backupSafeID(id) {
			return nil, domain.ErrInvalidInput
		}
		v, e := age.ParseX25519Recipient(c.Recipients[id])
		if e != nil {
			return nil, domain.ErrInvalidInput
		}
		g.recipients = append(g.recipients, v)
	}
	for _, id := range sortedBackupKeys(c.Identities) {
		if !backupSafeID(id) {
			return nil, domain.ErrInvalidInput
		}
		v, e := age.ParseX25519Identity(c.Identities[id])
		if e != nil {
			return nil, domain.ErrInvalidInput
		}
		g.identities = append(g.identities, v)
	}
	for id, recipient := range c.Recipients {
		identity, e := age.ParseX25519Identity(c.Identities[id])
		if e != nil || identity.Recipient().String() != recipient {
			return nil, domain.ErrInvalidInput
		}
	}
	return g, nil
}
func backupSafeID(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}
func sortedBackupKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
func (g *BackupArchiveGateway) prefix(id string) string {
	return "backups/" + g.config.Namespace + "/" + id + "/"
}
func backupDigest(v []byte) string { s := sha256.Sum256(v); return hex.EncodeToString(s[:]) }
func backupObjectName(o domain.BackupObject) string {
	return "objects/" + backupDigest([]byte(o.ArtifactID+"\x00"+o.Generation+"\x00"+o.ObjectKey))
}
func privateBackupDir(path string) error {
	if !filepath.IsAbs(path) {
		return domain.ErrInvalidInput
	}
	if e := os.MkdirAll(path, 0700); e != nil {
		return domain.ErrUnavailable
	}
	info, e := os.Lstat(path)
	if e != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 || info.Mode()&os.ModeSymlink != 0 {
		return domain.ErrInvalidInput
	}
	return nil
}
func backupFileDigest(path string) (string, int64, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", 0, domain.ErrUnavailable
	}
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return "", 0, domain.ErrInvalidInput
	}
	h := sha256.New()
	n, e := io.Copy(h, f)
	closeErr := f.Close()
	if e != nil || closeErr != nil {
		return "", 0, domain.ErrUnavailable
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
func (g *BackupArchiveGateway) Write(ctx context.Context, m domain.BackupManifest, dataPath, systemPath string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !backupSafeID(m.BackupID) || m.EvidenceNamespace == "" || m.SchemaDigest == "" || m.ToolVersionsDigest == "" || m.Journal.Namespace == "" || len(m.Objects) > domain.MaxBackupObjects {
		return domain.ErrInvalidInput
	}
	if e := privateBackupDir(g.config.ScratchRoot); e != nil {
		return e
	}
	if e := checkBackupSpace(g.config.ScratchRoot, g.config.MaxArchiveBytes*3+(256<<20)); e != nil {
		return e
	}
	dir, e := os.MkdirTemp(g.config.ScratchRoot, "archive-")
	if e != nil {
		return domain.ErrUnavailable
	}
	defer func() { _ = os.RemoveAll(dir) }()
	m.Version = 1
	m.ArchiveNamespace = g.config.Namespace
	m.KeyIDs = sortedBackupKeys(g.config.Recipients)
	m.Completed = true
	m.Neo4jDumpDigest, m.Neo4jDumpBytes, e = backupFileDigest(dataPath)
	if e != nil {
		return e
	}
	m.SystemDumpDigest, m.SystemDumpBytes, e = backupFileDigest(systemPath)
	if e != nil {
		return e
	}
	manifest, e := json.Marshal(m)
	if e != nil || len(manifest) > 32<<20 {
		return domain.ErrBudget
	}
	cipherPath := filepath.Join(dir, "archive.age")
	f, e := os.OpenFile(cipherPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return domain.ErrUnavailable
	}
	encrypted, e := age.Encrypt(f, g.recipients...)
	if e != nil {
		_ = f.Close()
		return domain.ErrUnavailable
	}
	tw := tar.NewWriter(encrypted)
	writeErr := g.writeTar(ctx, tw, m, manifest, dataPath, systemPath)
	tarErr := tw.Close()
	ageErr := encrypted.Close()
	syncErr := f.Sync()
	fileErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if tarErr != nil || ageErr != nil || syncErr != nil || fileErr != nil {
		return domain.ErrUnavailable
	}
	digest, n, e := backupFileDigest(cipherPath)
	if e != nil {
		return e
	}
	if n > g.config.MaxArchiveBytes {
		return domain.ErrBudget
	}
	completion := backupCompletion{Version: 1, BackupID: m.BackupID, Namespace: g.config.Namespace, AuthorityKeyID: g.config.AuthorityKeyID, CreatedAt: m.CreatedAt, CipherDigest: digest, CipherBytes: n, KeyIDs: slices.Clone(m.KeyIDs)}
	// Verification reads every authenticated plaintext chunk before publication.
	if _, e = g.readArchive(ctx, cipherPath, completion, ""); e != nil {
		return e
	}
	input, e := os.Open(cipherPath)
	if e != nil {
		return domain.ErrUnavailable
	}
	e = g.store.PutImmutable(ctx, g.prefix(m.BackupID)+"archive.age", input, n, digest)
	closeErr := input.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return domain.ErrUnavailable
	}
	remote, e := g.download(ctx, g.prefix(m.BackupID)+"archive.age", dir, completion)
	if e != nil {
		return e
	}
	if _, e = g.readArchive(ctx, remote, completion, ""); e != nil {
		return e
	}
	completion.MAC = g.signCompletion(completion)
	encoded, e := json.Marshal(completion)
	if e != nil {
		return domain.ErrUnavailable
	}
	return g.store.PutImmutable(ctx, g.prefix(m.BackupID)+"complete.json", bytes.NewReader(encoded), int64(len(encoded)), backupDigest(encoded))
}
func (g *BackupArchiveGateway) writeTar(ctx context.Context, tw *tar.Writer, m domain.BackupManifest, manifest []byte, data, system string) error {
	if e := writeBackupEntry(tw, "manifest.json", bytes.NewReader(manifest), int64(len(manifest)), ""); e != nil {
		return e
	}
	for _, entry := range []struct {
		name, path, digest string
		size               int64
	}{{"neo4j.dump", data, m.Neo4jDumpDigest, m.Neo4jDumpBytes}, {"system.dump", system, m.SystemDumpDigest, m.SystemDumpBytes}} {
		f, e := os.Open(entry.path)
		if e != nil {
			return domain.ErrUnavailable
		}
		e = writeBackupEntry(tw, entry.name, f, entry.size, entry.digest)
		closeErr := f.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return domain.ErrUnavailable
		}
	}
	seen := map[string]bool{}
	last := ""
	for _, o := range m.Objects {
		if ctx.Err() != nil {
			return domain.ErrUnavailable
		}
		if o.ArtifactID <= last {
			return domain.ErrInvalidInput
		}
		last = o.ArtifactID
		if o.Deleted || !o.Available {
			continue
		}
		if o.Bytes < 0 || o.Bytes > domain.MaxArtifactBytes {
			return domain.ErrBudget
		}
		name := backupObjectName(o)
		if seen[name] {
			return domain.ErrInvalidInput
		}
		seen[name] = true
		body, e := g.source.Open(ctx, o.ObjectKey)
		if e != nil {
			return e
		}
		e = writeBackupEntry(tw, name, body, o.Bytes, o.Digest)
		closeErr := body.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return domain.ErrUnavailable
		}
	}
	return nil
}
func writeBackupEntry(tw *tar.Writer, name string, r io.Reader, n int64, digest string) error {
	if n < 0 {
		return domain.ErrInvalidInput
	}
	if e := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: n, Typeflag: tar.TypeReg}); e != nil {
		return domain.ErrUnavailable
	}
	h := sha256.New()
	actual, e := io.Copy(io.MultiWriter(tw, h), io.LimitReader(r, n+1))
	if e != nil || actual != n {
		return domain.ErrIncomplete
	}
	if digest != "" && hex.EncodeToString(h.Sum(nil)) != digest {
		return domain.ErrIncomplete
	}
	return nil
}
func (g *BackupArchiveGateway) signCompletion(c backupCompletion) string {
	c.MAC = ""
	data, _ := json.Marshal(c)
	key := g.config.AuthorityKeys[c.AuthorityKeyID]
	mac := hmac.New(sha256.New, key[:])
	_, _ = mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil))
}
func (g *BackupArchiveGateway) completion(ctx context.Context, id string) (backupCompletion, error) {
	if !backupSafeID(id) {
		return backupCompletion{}, domain.ErrInvalidInput
	}
	body, e := g.store.Open(ctx, g.prefix(id)+"complete.json")
	if e != nil {
		return backupCompletion{}, e
	}
	b, e := io.ReadAll(io.LimitReader(body, 16385))
	closeErr := body.Close()
	if e != nil || closeErr != nil || len(b) > 16384 {
		return backupCompletion{}, domain.ErrUnavailable
	}
	var c backupCompletion
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if dec.Decode(&c) != nil || dec.Decode(new(any)) != io.EOF || c.Version != 1 || c.BackupID != id || c.Namespace != g.config.Namespace || c.CreatedAt.IsZero() || c.CipherBytes < 1 || c.CipherBytes > g.config.MaxArchiveBytes {
		return c, domain.ErrUnavailable
	}
	if _, ok := g.config.AuthorityKeys[c.AuthorityKeyID]; !ok {
		return c, domain.ErrUnavailable
	}
	expected, e := hex.DecodeString(g.signCompletion(c))
	if e != nil {
		return c, domain.ErrUnavailable
	}
	actual, e := hex.DecodeString(c.MAC)
	if e != nil || !hmac.Equal(expected, actual) {
		return c, domain.ErrUnavailable
	}
	return c, nil
}
func (g *BackupArchiveGateway) download(ctx context.Context, key, dir string, c backupCompletion) (string, error) {
	body, e := g.store.Open(ctx, key)
	if e != nil {
		return "", e
	}
	f, e := os.CreateTemp(dir, "cipher-")
	if e != nil {
		_ = body.Close()
		return "", domain.ErrUnavailable
	}
	path := f.Name()
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(f, h), io.LimitReader(body, c.CipherBytes+1))
	readClose := body.Close()
	syncErr := f.Sync()
	fileClose := f.Close()
	if e != nil || readClose != nil || syncErr != nil || fileClose != nil || n != c.CipherBytes || hex.EncodeToString(h.Sum(nil)) != c.CipherDigest {
		_ = os.Remove(path)
		return "", domain.ErrIncomplete
	}
	return path, nil
}
func (g *BackupArchiveGateway) Verify(ctx context.Context, id string) (domain.BackupManifest, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	c, e := g.completion(ctx, id)
	if e != nil {
		return domain.BackupManifest{}, e
	}
	if e = privateBackupDir(g.config.ScratchRoot); e != nil {
		return domain.BackupManifest{}, e
	}
	dir, e := os.MkdirTemp(g.config.ScratchRoot, "verify-")
	if e != nil {
		return domain.BackupManifest{}, domain.ErrUnavailable
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path, e := g.download(ctx, g.prefix(id)+"archive.age", dir, c)
	if e != nil {
		return domain.BackupManifest{}, e
	}
	return g.readArchive(ctx, path, c, "")
}
func (g *BackupArchiveGateway) Restore(ctx context.Context, id, target string) (domain.BackupManifest, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if e := g.prepareRestoreTarget(target, false); e != nil {
		return domain.BackupManifest{}, e
	}
	c, e := g.completion(ctx, id)
	if e != nil {
		return domain.BackupManifest{}, e
	}
	parent := filepath.Dir(target)
	if e = privateBackupDir(parent); e != nil {
		return domain.BackupManifest{}, e
	}
	if e = checkBackupSpace(parent, c.CipherBytes*6+(256<<20)); e != nil {
		return domain.BackupManifest{}, e
	}
	dir, e := os.MkdirTemp(parent, "restore-unverified-")
	if e != nil {
		return domain.BackupManifest{}, domain.ErrUnavailable
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path, e := g.download(ctx, g.prefix(id)+"archive.age", dir, c)
	if e != nil {
		return domain.BackupManifest{}, e
	}
	stage := filepath.Join(dir, "verified")
	if e = os.Mkdir(stage, 0700); e != nil {
		return domain.BackupManifest{}, domain.ErrUnavailable
	}
	m, e := g.readArchive(ctx, path, c, stage)
	if e != nil {
		return domain.BackupManifest{}, e
	}
	// A restarted adapter has no in-memory stage ownership. Reconstruct custody
	// from the exact persisted incarnation path, then discard only that bounded
	// private stage after its replacement has passed full authentication/EOF.
	if e = g.prepareRestoreTarget(target, true); e != nil {
		return domain.BackupManifest{}, e
	}
	if e = os.Rename(stage, target); e != nil {
		return domain.BackupManifest{}, domain.ErrUnavailable
	}
	g.stage = target
	g.staged = m
	return m, nil
}
func (g *BackupArchiveGateway) readArchive(ctx context.Context, path string, c backupCompletion, target string) (domain.BackupManifest, error) {
	f, e := os.Open(path)
	if e != nil {
		return domain.BackupManifest{}, domain.ErrUnavailable
	}
	defer func() { _ = f.Close() }()
	reader, e := age.Decrypt(f, g.identities...)
	if e != nil {
		return domain.BackupManifest{}, domain.ErrUnavailable
	}
	tr := tar.NewReader(reader)
	header, e := tr.Next()
	if e != nil || header.Name != "manifest.json" || header.Typeflag != tar.TypeReg || header.Size < 1 || header.Size > 32<<20 {
		return domain.BackupManifest{}, domain.ErrIncomplete
	}
	b, e := io.ReadAll(io.LimitReader(tr, header.Size+1))
	if e != nil || int64(len(b)) != header.Size {
		return domain.BackupManifest{}, domain.ErrIncomplete
	}
	var m domain.BackupManifest
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if dec.Decode(&m) != nil || dec.Decode(new(any)) != io.EOF || m.Version != 1 || m.BackupID != c.BackupID || m.ArchiveNamespace != c.Namespace || m.CreatedAt != c.CreatedAt || !m.Completed || !slices.Equal(m.KeyIDs, c.KeyIDs) || len(m.Objects) > domain.MaxBackupObjects {
		return m, domain.ErrIncomplete
	}
	if m.GraphStoreBytes < 0 || m.GraphStoreBytes > 1<<40 {
		return m, domain.ErrBudget
	}
	if target != "" {
		if e = checkBackupSpace(target, m.GraphStoreBytes*2+c.CipherBytes*2+(256<<20)); e != nil {
			return m, e
		}
	}
	type expectedFile struct {
		size   int64
		digest string
	}
	expected := map[string]expectedFile{"neo4j.dump": {m.Neo4jDumpBytes, m.Neo4jDumpDigest}, "system.dump": {m.SystemDumpBytes, m.SystemDumpDigest}}
	last := ""
	for _, o := range m.Objects {
		if o.ArtifactID <= last {
			return m, domain.ErrIncomplete
		}
		last = o.ArtifactID
		if o.Deleted || !o.Available {
			continue
		}
		if o.Bytes < 0 || o.Bytes > domain.MaxArtifactBytes {
			return m, domain.ErrBudget
		}
		name := backupObjectName(o)
		if _, ok := expected[name]; ok {
			return m, domain.ErrIncomplete
		}
		expected[name] = expectedFile{o.Bytes, o.Digest}
	}
	for {
		if ctx.Err() != nil {
			return m, domain.ErrUnavailable
		}
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return m, domain.ErrIncomplete
		}
		entry, ok := expected[h.Name]
		if !ok || h.Typeflag != tar.TypeReg || h.Size != entry.size || entry.size < 0 || len(entry.digest) != 64 {
			return m, domain.ErrIncomplete
		}
		delete(expected, h.Name)
		hasher := sha256.New()
		out := io.Writer(hasher)
		var file *os.File
		if target != "" {
			dest := filepath.Join(target, filepath.FromSlash(h.Name))
			if !strings.HasPrefix(dest, target+string(filepath.Separator)) {
				return m, domain.ErrIncomplete
			}
			if e = os.MkdirAll(filepath.Dir(dest), 0700); e != nil {
				return m, domain.ErrUnavailable
			}
			file, e = os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if e != nil {
				return m, domain.ErrUnavailable
			}
			out = io.MultiWriter(hasher, file)
		}
		n, e := io.Copy(out, tr)
		var closeErr error
		if file != nil {
			if syncErr := file.Sync(); syncErr != nil {
				closeErr = syncErr
			}
			if file.Close() != nil {
				closeErr = domain.ErrUnavailable
			}
		}
		if e != nil || closeErr != nil || n != entry.size || hex.EncodeToString(hasher.Sum(nil)) != entry.digest {
			return m, domain.ErrIncomplete
		}
	}
	// tar EOF alone does not authenticate age's final chunk. Consume all bytes.
	if _, e = io.Copy(io.Discard, reader); e != nil {
		return m, domain.ErrIncomplete
	}
	if len(expected) != 0 {
		return m, domain.ErrIncomplete
	}
	return m, nil
}
func (g *BackupArchiveGateway) OpenRestoreObject(ctx context.Context, o domain.BackupObject) (io.ReadCloser, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ctx.Err() != nil || g.stage == "" {
		return nil, domain.ErrUnavailable
	}
	found := false
	for _, entry := range g.staged.Objects {
		if entry == o && entry.Available && !entry.Deleted {
			found = true
			break
		}
	}
	if !found {
		return nil, domain.ErrIncomplete
	}
	f, e := os.Open(filepath.Join(g.stage, backupObjectName(o)))
	if e != nil {
		return nil, domain.ErrIncomplete
	}
	return f, nil
}
func (g *BackupArchiveGateway) Retain(ctx context.Context, before time.Time) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	keys, e := g.store.List(ctx, "backups/"+g.config.Namespace+"/")
	if e != nil {
		return e
	}
	if len(keys) > 100000 {
		return domain.ErrBudget
	}
	for _, key := range keys {
		if !strings.HasSuffix(key, "/complete.json") {
			continue
		}
		parts := strings.Split(key, "/")
		if len(parts) != 4 || parts[0] != "backups" || parts[1] != g.config.Namespace || !backupSafeID(parts[2]) {
			return domain.ErrUnavailable
		}
		c, e := g.completion(ctx, parts[2])
		if e != nil {
			return e
		}
		if !c.CreatedAt.Before(before) {
			continue
		}
		if e = g.store.Delete(ctx, g.prefix(c.BackupID)+"archive.age"); e != nil {
			return e
		}
		if e = g.store.Delete(ctx, key); e != nil {
			return e
		}
	}
	return nil
}

// CleanupRestore removes only this adapter's verified private stage. Evidence
// deletion fences and independent journal storage are never physical-deleted.
func (g *BackupArchiveGateway) CleanupRestore() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stage == "" {
		return nil
	}
	e := g.prepareRestoreTarget(g.stage, true)
	if e != nil {
		return e
	}
	g.stage = ""
	g.staged = domain.BackupManifest{}
	if e != nil {
		return domain.ErrUnavailable
	}
	return nil
}

func validateRestoreStageInfo(info os.FileInfo, uid uint32) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uid {
		return domain.ErrForbidden
	}
	if info.IsDir() {
		if info.Mode().Perm() != 0700 {
			return domain.ErrForbidden
		}
		return nil
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return domain.ErrForbidden
	}
	return nil
}
func (g *BackupArchiveGateway) prepareRestoreTarget(target string, remove bool) error {
	if !filepath.IsAbs(target) || filepath.Clean(target) != target || filepath.Dir(target) != g.config.ScratchRoot || !strings.HasPrefix(filepath.Base(target), "restore-") || !backupSafeID(filepath.Base(target)) {
		return domain.ErrInvalidInput
	}
	if e := privateBackupDir(g.config.ScratchRoot); e != nil {
		return e
	}
	directory, e := openPrivateDirectory(g.config.ScratchRoot)
	if e != nil {
		return e
	}
	_ = directory.Close()
	root, e := os.OpenRoot(g.config.ScratchRoot)
	if e != nil {
		return domain.ErrUnavailable
	}
	defer func() { _ = root.Close() }()
	name := filepath.Base(target)
	info, e := root.Lstat(name)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return domain.ErrUnavailable
	}
	if !info.IsDir() {
		return domain.ErrForbidden
	}
	if e = validateRestoreStageInfo(info, uint32(os.Geteuid())); e != nil {
		return e
	}
	stage, e := os.OpenRoot(target)
	if e != nil {
		return domain.ErrUnavailable
	}
	e = g.checkRestoreStage(stage)
	closeErr := stage.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return domain.ErrUnavailable
	}
	if remove && root.RemoveAll(name) != nil {
		return domain.ErrUnavailable
	}
	return nil
}
func (g *BackupArchiveGateway) checkRestoreStage(root *os.Root) error {
	var bytes int64
	count := 0
	for _, dir := range []string{".", "objects"} {
		info, e := root.Lstat(dir)
		if errors.Is(e, os.ErrNotExist) && dir == "objects" {
			continue
		}
		if e != nil {
			return domain.ErrUnavailable
		}
		if !info.IsDir() {
			return domain.ErrForbidden
		}
		if e = validateRestoreStageInfo(info, uint32(os.Geteuid())); e != nil {
			return e
		}
		entries, e := root.Open(dir)
		if e != nil {
			return domain.ErrUnavailable
		}
		scanErr := func() error {
			for {
				page, err := entries.ReadDir(100)
				if err != nil && !errors.Is(err, io.EOF) {
					return domain.ErrUnavailable
				}
				for _, entry := range page {
					count++
					if count > domain.MaxBackupObjects+3 {
						return domain.ErrBudget
					}
					relative := entry.Name()
					if dir == "objects" {
						sum, decodeErr := hex.DecodeString(relative)
						if decodeErr != nil || len(sum) != 32 || len(relative) != 64 {
							return domain.ErrForbidden
						}
						relative = filepath.Join(dir, relative)
					} else if relative != "neo4j.dump" && relative != "system.dump" && relative != "objects" {
						return domain.ErrForbidden
					}
					info, e := root.Lstat(relative)
					if e != nil {
						return domain.ErrUnavailable
					}
					if e = validateRestoreStageInfo(info, uint32(os.Geteuid())); e != nil {
						return e
					}
					if info.IsDir() {
						if dir != "." || entry.Name() != "objects" {
							return domain.ErrForbidden
						}
						continue
					}
					if dir == "." && entry.Name() == "objects" {
						return domain.ErrForbidden
					}
					if info.Size() < 0 || info.Size() > g.config.MaxArchiveBytes-bytes {
						return domain.ErrBudget
					}
					bytes += info.Size()
				}
				if errors.Is(err, io.EOF) {
					return nil
				}
			}
		}()
		closeErr := entries.Close()
		if scanErr != nil {
			return scanErr
		}
		if closeErr != nil {
			return domain.ErrUnavailable
		}
	}
	return nil
}

func (g *BackupArchiveGateway) CleanupBackupDirectory(ctx context.Context, path string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ctx.Err() != nil || filepath.Dir(path) != g.config.ScratchRoot || !strings.HasPrefix(filepath.Base(path), "backup-") {
		return domain.ErrInvalidInput
	}
	info, e := os.Lstat(path)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return domain.ErrInvalidInput
	}
	if os.RemoveAll(path) != nil {
		return domain.ErrUnavailable
	}
	return nil
}

// Authenticated completion key IDs are checked before readiness. Retirement of
// an identity is explicit only after every retained completed set has expired.
func (g *BackupArchiveGateway) CheckRecoveryKeys(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	keys, e := g.store.List(ctx, "backups/"+g.config.Namespace+"/")
	if e != nil {
		return e
	}
	for _, key := range keys {
		if !strings.HasSuffix(key, "/complete.json") {
			continue
		}
		parts := strings.Split(key, "/")
		if len(parts) != 4 || parts[0] != "backups" || parts[1] != g.config.Namespace || !backupSafeID(parts[2]) {
			return domain.ErrUnavailable
		}
		c, e := g.completion(ctx, parts[2])
		if e != nil {
			return e
		}
		if len(c.KeyIDs) == 0 || len(c.KeyIDs) > 16 {
			return domain.ErrUnavailable
		}
		for _, id := range c.KeyIDs {
			if _, ok := g.config.Identities[id]; !ok {
				return domain.ErrUnavailable
			}
		}
	}
	return nil
}
