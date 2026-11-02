package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

type Neo4jProcessStateStore interface {
	LoadProcess(context.Context) (domain.BackupProcessState, error)
	SaveProcess(context.Context, domain.BackupProcessState) error
}
type Neo4jProcessConfig struct {
	Root, Home, Neo4jBinary, AdminBinary, JavaBinary, AuthClassPath string
	GraphSecret                                                     string `json:"-"`
	HeapInitialMiB, HeapMaxMiB, PageCacheMiB                        int
}

func (Neo4jProcessConfig) String() string   { return "Neo4jProcessConfig([redacted])" }
func (Neo4jProcessConfig) GoString() string { return "Neo4jProcessConfig([redacted])" }

type Neo4jProcessGateway struct {
	mu       sync.Mutex
	config   Neo4jProcessConfig
	state    Neo4jProcessStateStore
	lifetime context.Context
	active   domain.BackupProcessState
	command  *exec.Cmd
	done     chan struct{}
	waitErr  error
	stopping bool
}

func NewNeo4jProcess(c Neo4jProcessConfig, state Neo4jProcessStateStore, lifetime context.Context) (*Neo4jProcessGateway, error) {
	for _, p := range []string{c.Root, c.Home, c.Neo4jBinary, c.AdminBinary, c.JavaBinary, c.AuthClassPath} {
		if !filepath.IsAbs(p) || strings.ContainsAny(p, "\n\r\x00") {
			return nil, domain.ErrInvalidInput
		}
	}
	if state == nil || lifetime == nil || len(c.GraphSecret) < 8 || len(c.GraphSecret) > 4096 || strings.ContainsAny(c.GraphSecret, "\r\n\x00") {
		return nil, domain.ErrInvalidInput
	}
	if c.HeapInitialMiB == 0 {
		c.HeapInitialMiB = 256
	}
	if c.HeapMaxMiB == 0 {
		c.HeapMaxMiB = 512
	}
	if c.PageCacheMiB == 0 {
		c.PageCacheMiB = 128
	}
	if c.HeapInitialMiB < 64 || c.HeapMaxMiB < c.HeapInitialMiB || c.HeapMaxMiB > 65536 || c.PageCacheMiB < 32 || c.PageCacheMiB > 65536 {
		return nil, domain.ErrInvalidInput
	}
	g := &Neo4jProcessGateway{config: c, state: state, lifetime: lifetime}
	active, e := state.LoadProcess(lifetime)
	if e != nil {
		return nil, e
	}
	if active.Version == 0 {
		active = domain.BackupProcessState{Version: 1, Generation: "initial", Root: filepath.Join(c.Root, "initial")}
		if e = state.SaveProcess(lifetime, active); e != nil {
			return nil, e
		}
	}
	if active.Version != 1 || !strings.HasPrefix(active.Root, c.Root+string(filepath.Separator)) || !backupSafeID(active.Generation) {
		return nil, domain.ErrUnavailable
	}
	g.active = active
	return g, nil
}
func (g *Neo4jProcessGateway) configFor(root string) (string, error) {
	for _, p := range []string{root, filepath.Join(root, "config"), filepath.Join(root, "data"), filepath.Join(root, "transactions"), filepath.Join(root, "logs"), filepath.Join(root, "run")} {
		if e := privateBackupDir(p); e != nil {
			return "", e
		}
	}
	c := g.config
	text := fmt.Sprintf("server.directories.data=%s\nserver.directories.transaction.logs.root=%s\nserver.directories.logs=%s\nserver.directories.run=%s\nserver.bolt.enabled=true\nserver.bolt.listen_address=0.0.0.0:7687\nserver.http.enabled=false\nserver.https.enabled=false\ndbms.security.auth_enabled=true\ndb.query.default_language=CYPHER_25\nserver.memory.heap.initial_size=%dm\nserver.memory.heap.max_size=%dm\nserver.memory.pagecache.size=%dm\n", filepath.Join(root, "data"), filepath.Join(root, "transactions"), filepath.Join(root, "logs"), filepath.Join(root, "run"), c.HeapInitialMiB, c.HeapMaxMiB, c.PageCacheMiB)
	path := filepath.Join(root, "config", "neo4j.conf")
	if e := os.WriteFile(path, []byte(text), 0600); e != nil {
		return "", domain.ErrUnavailable
	}
	return filepath.Dir(path), nil
}
func (g *Neo4jProcessGateway) environment(conf string) []string {
	env := []string{"NEO4J_HOME=" + g.config.Home, "NEO4J_CONF=" + conf, "HOME=" + g.config.Home, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8"}
	if java := os.Getenv("JAVA_HOME"); java != "" {
		env = append(env, "JAVA_HOME="+java)
	}
	return env
}
func (g *Neo4jProcessGateway) tool(ctx context.Context, conf string, args ...string) error {
	cmd := exec.CommandContext(ctx, g.config.AdminBinary, args...)
	cmd.Env = g.environment(conf)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if cmd.Run() != nil {
		if ctx.Err() != nil {
			domain.MarkEffectUncertain(ctx)
		}
		return domain.ErrUnavailable
	}
	return nil
}
func (g *Neo4jProcessGateway) Start(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ctx.Err() != nil {
		return domain.ErrUnavailable
	}
	if g.command != nil {
		select {
		case <-g.done:
			g.command = nil
		default:
			return nil
		}
	}
	conf, e := g.configFor(g.active.Root)
	if e != nil {
		return e
	}
	if _, e = os.Stat(filepath.Join(g.active.Root, "data", "databases", "system")); errors.Is(e, os.ErrNotExist) {
		// The pinned Java helper calls the pinned Neo4j password command in-process;
		// the password travels only through stdin, never OS argv or child output.
		auth := exec.CommandContext(ctx, g.config.JavaBinary, "-cp", g.config.AuthClassPath, "Neo4jInitialPassword", g.config.Home, conf)
		auth.Env = g.environment(conf)
		auth.Stdin = bytes.NewBufferString(g.config.GraphSecret)
		auth.Stdout = io.Discard
		auth.Stderr = io.Discard
		if auth.Run() != nil {
			return domain.ErrUnavailable
		}
	} else if e != nil {
		return domain.ErrUnavailable
	}
	cmd := exec.CommandContext(g.lifetime, g.config.Neo4jBinary, "console")
	cmd.Env = g.environment(conf)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 60 * time.Second
	if e = cmd.Start(); e != nil {
		return domain.ErrUnavailable
	}
	g.command = cmd
	g.done = make(chan struct{})
	g.waitErr = nil
	g.stopping = false
	done := g.done
	go func() { err := cmd.Wait(); g.mu.Lock(); g.waitErr = err; close(done); g.mu.Unlock() }()
	return nil
}
func (g *Neo4jProcessGateway) Stop(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ctx.Err() != nil {
		return domain.ErrUnavailable
	}
	if g.command == nil {
		return nil
	}
	select {
	case <-g.done:
		return nil
	default:
	}
	g.stopping = true
	if e := syscall.Kill(-g.command.Process.Pid, syscall.SIGTERM); e != nil && !errors.Is(e, syscall.ESRCH) {
		return domain.ErrUnavailable
	}
	return nil
}
func (g *Neo4jProcessGateway) WaitStopped(ctx context.Context) error {
	g.mu.Lock()
	if g.command == nil {
		g.mu.Unlock()
		return nil
	}
	done := g.done
	pid := g.command.Process.Pid
	g.mu.Unlock()
	select {
	case <-ctx.Done():
		domain.MarkEffectUncertain(ctx)
		return domain.ErrUnavailable
	case <-done:
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if syscall.Kill(-pid, 0) == nil {
		return domain.ErrUnavailable
	}
	if g.waitErr != nil && !g.stopping {
		return domain.ErrUnavailable
	}
	if exit := new(exec.ExitError); errors.As(g.waitErr, &exit) {
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() && status.Signal() == syscall.SIGKILL {
			return domain.ErrUnavailable
		}
	}
	g.command = nil
	return nil
}
func (g *Neo4jProcessGateway) offline() bool {
	if g.command == nil {
		return true
	}
	select {
	case <-g.done:
		return false
	default:
		return false
	}
}
func (g *Neo4jProcessGateway) Dump(ctx context.Context, dir string) (string, string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.offline() {
		return "", "", domain.ErrConflict
	}
	if e := privateBackupDir(dir); e != nil {
		return "", "", e
	}
	usage, e := g.diskBytes(ctx)
	if e != nil {
		return "", "", e
	}
	if usage > 1<<40 {
		return "", "", domain.ErrBudget
	}
	if e = checkBackupSpace(g.config.Root, usage*3+(256<<20)); e != nil {
		return "", "", e
	}
	conf, e := g.configFor(g.active.Root)
	if e != nil {
		return "", "", e
	}
	for _, db := range []string{"neo4j", "system"} {
		if e = g.tool(ctx, conf, "database", "dump", db, "--to-path="+dir); e != nil {
			return "", "", e
		}
		if e = os.Chmod(filepath.Join(dir, db+".dump"), 0600); e != nil {
			return "", "", domain.ErrUnavailable
		}
	}
	return filepath.Join(dir, "neo4j.dump"), filepath.Join(dir, "system.dump"), nil
}
func (g *Neo4jProcessGateway) Load(ctx context.Context, data, system string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.offline() {
		return domain.ErrConflict
	}
	if filepath.Base(data) != "neo4j.dump" || filepath.Base(system) != "system.dump" || filepath.Dir(data) != filepath.Dir(system) || !filepath.IsAbs(data) {
		return domain.ErrInvalidInput
	}
	for _, path := range []string{data, system} {
		info, e := os.Lstat(path)
		if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return domain.ErrInvalidInput
		}
	}
	dataInfo, e := os.Stat(data)
	if e != nil {
		return domain.ErrUnavailable
	}
	systemInfo, e := os.Stat(system)
	if e != nil {
		return domain.ErrUnavailable
	}
	required := dataInfo.Size() + systemInfo.Size()
	if required > 1<<40 {
		return domain.ErrBudget
	}
	if e = checkBackupSpace(g.config.Root, required*6+(256<<20)); e != nil {
		return e
	}
	generation := "restore-" + backupDigest([]byte(filepath.Dir(data)))[:24]
	root := filepath.Join(g.config.Root, generation)
	if g.active.Root == root && g.active.Generation == generation {
		return nil
	}
	if _, e := os.Lstat(root); !errors.Is(e, os.ErrNotExist) {
		if e != nil {
			return domain.ErrUnavailable
		}
		suffix, err := randomHex()
		if err != nil {
			return err
		}
		if err = os.Rename(root, root+".failed-"+suffix); err != nil {
			return domain.ErrUnavailable
		}
	}
	conf, e := g.configFor(root)
	if e != nil {
		return e
	}
	for _, db := range []string{"neo4j", "system"} {
		if e = g.tool(ctx, conf, "database", "load", db, "--from-path="+filepath.Dir(data), "--overwrite-destination=false"); e != nil {
			return e
		}
	}
	next := domain.BackupProcessState{Version: 1, Generation: generation, Root: root}
	if e = g.state.SaveProcess(ctx, next); e != nil {
		return e
	}
	g.active = next
	return nil
}

func (g *Neo4jProcessGateway) diskBytes(ctx context.Context) (int64, error) {
	var total int64
	err := filepath.WalkDir(g.active.Root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil {
			return domain.ErrUnavailable
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return domain.ErrInvalidInput
		}
		if !entry.IsDir() {
			info, e := entry.Info()
			if e != nil || !info.Mode().IsRegular() {
				return domain.ErrUnavailable
			}
			if info.Size() > 1<<40 || total > (1<<40)-info.Size() {
				return domain.ErrBudget
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}
func (g *Neo4jProcessGateway) DiskBytes(ctx context.Context) (int64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.diskBytes(ctx)
}
