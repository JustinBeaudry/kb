package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"golang.org/x/sys/unix"
)

type maintenanceDocument struct {
	Version int                      `json:"version"`
	State   domain.MaintenanceState  `json:"state"`
	Permits []domain.OperationPermit `json:"permits"`
}
type MaintenanceStateStore struct {
	mu   sync.Mutex
	dir  *os.File
	name string
}

// NewMaintenanceStateStore never treats a missing historical state as fresh.
// allowCreate is only for composition which has established an empty graph volume.
func NewMaintenanceStateStore(path string, allowCreate bool) (*MaintenanceStateStore, error) {
	if !filepath.IsAbs(path) || filepath.Base(path) == "." {
		return nil, domain.ErrInvalidInput
	}
	dir, err := openPrivateDirectory(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	s := &MaintenanceStateStore{dir: dir, name: filepath.Base(path)}
	_, _, err = s.Load(context.Background())
	if err == nil {
		return s, nil
	}
	if !errors.Is(err, os.ErrNotExist) || !allowCreate {
		_ = dir.Close()
		return nil, domain.ErrUnavailable
	}
	initial := domain.MaintenanceState{Phase: "bootstrap", Fence: domain.MaintenanceFence{OperationID: "bootstrap", Generation: 1}}
	// O_EXCL prevents two initializers from replacing an existing historical state.
	fd, err := unix.Openat(int(dir.Fd()), s.name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		_ = dir.Close()
		return nil, domain.ErrUnavailable
	}
	f := os.NewFile(uintptr(fd), s.name)
	data, _ := json.Marshal(maintenanceDocument{Version: 1, State: initial, Permits: []domain.OperationPermit{}})
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil || dir.Sync() != nil {
		_ = dir.Close()
		return nil, domain.ErrUnavailable
	}
	return s, nil
}
func openPrivateDirectory(path string) (*os.File, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != filepath.Clean(path) {
		return nil, domain.ErrUnavailable
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, domain.ErrUnavailable
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, domain.ErrUnavailable
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm() != 0700 {
		_ = f.Close()
		return nil, domain.ErrForbidden
	}
	return f, nil
}
func (s *MaintenanceStateStore) Close() error { return s.dir.Close() }
func (s *MaintenanceStateStore) Load(ctx context.Context) (domain.MaintenanceState, []domain.OperationPermit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return domain.MaintenanceState{}, nil, ctx.Err()
	}
	fd, err := unix.Openat(int(s.dir.Fd()), s.name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return domain.MaintenanceState{}, nil, os.ErrNotExist
		}
		return domain.MaintenanceState{}, nil, domain.ErrUnavailable
	}
	f := os.NewFile(uintptr(fd), s.name)
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return domain.MaintenanceState{}, nil, domain.ErrUnavailable
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || stat.Uid != uint32(os.Geteuid()) || info.Size() > 4<<20 {
		return domain.MaintenanceState{}, nil, domain.ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	if err != nil || len(data) > 4<<20 {
		return domain.MaintenanceState{}, nil, domain.ErrUnavailable
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var doc maintenanceDocument
	if dec.Decode(&doc) != nil || doc.Version != 1 || dec.Decode(&struct{}{}) != io.EOF {
		return domain.MaintenanceState{}, nil, domain.ErrUnavailable
	}
	return doc.State, doc.Permits, nil
}
func (s *MaintenanceStateStore) Save(ctx context.Context, state domain.MaintenanceState, permits []domain.OperationPermit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	data, err := json.Marshal(maintenanceDocument{Version: 1, State: state, Permits: permits})
	if err != nil || len(data) > 4<<20 {
		return domain.ErrUnavailable
	}
	id, err := randomHex()
	if err != nil {
		return err
	}
	tmp := "." + s.name + "-" + id
	fd, err := unix.Openat(int(s.dir.Fd()), tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return domain.ErrUnavailable
	}
	defer func() { _ = unix.Unlinkat(int(s.dir.Fd()), tmp, 0) }()
	f := os.NewFile(uintptr(fd), tmp)
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return domain.ErrUnavailable
	}
	if unix.Renameat(int(s.dir.Fd()), tmp, int(s.dir.Fd()), s.name) != nil || s.dir.Sync() != nil {
		return domain.ErrUnavailable
	}
	return nil
}
