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

type backupStateDocument struct {
	Version   int                       `json:"version"`
	Operation domain.BackupOperation    `json:"operation"`
	Schedule  domain.BackupSchedule     `json:"schedule"`
	Process   domain.BackupProcessState `json:"process"`
}
type BackupStateStore struct {
	mu   sync.Mutex
	dir  *os.File
	name string
}

func NewBackupStateStore(path string, allowCreate bool) (*BackupStateStore, error) {
	if !filepath.IsAbs(path) {
		return nil, domain.ErrInvalidInput
	}
	dir, e := openPrivateDirectory(filepath.Dir(path))
	if e != nil {
		return nil, e
	}
	s := &BackupStateStore{dir: dir, name: filepath.Base(path)}
	_, e = s.read()
	if e == nil {
		return s, nil
	}
	if !errors.Is(e, os.ErrNotExist) || !allowCreate {
		_ = dir.Close()
		return nil, domain.ErrUnavailable
	}
	if e = s.write(backupStateDocument{Version: 1}, true); e != nil {
		_ = dir.Close()
		return nil, e
	}
	return s, nil
}
func (s *BackupStateStore) Close() error { return s.dir.Close() }
func (s *BackupStateStore) read() (backupStateDocument, error) {
	var doc backupStateDocument
	fd, e := unix.Openat(int(s.dir.Fd()), s.name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(e, unix.ENOENT) {
		return doc, os.ErrNotExist
	}
	if e != nil {
		return doc, domain.ErrUnavailable
	}
	f := os.NewFile(uintptr(fd), s.name)
	defer func() { _ = f.Close() }()
	info, e := f.Stat()
	if e != nil {
		return doc, domain.ErrUnavailable
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 1<<20 {
		return doc, domain.ErrUnavailable
	}
	b, e := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if e != nil || len(b) > 1<<20 {
		return doc, domain.ErrUnavailable
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if dec.Decode(&doc) != nil || doc.Version != 1 || dec.Decode(new(any)) != io.EOF {
		return doc, domain.ErrUnavailable
	}
	return doc, nil
}
func (s *BackupStateStore) write(doc backupStateDocument, create bool) error {
	data, e := json.Marshal(doc)
	if e != nil || len(data) > 1<<20 {
		return domain.ErrUnavailable
	}
	name := s.name
	if !create {
		id, e := randomHex()
		if e != nil {
			return e
		}
		name = "." + s.name + "-" + id
		defer func() { _ = unix.Unlinkat(int(s.dir.Fd()), name, 0) }()
	}
	fd, e := unix.Openat(int(s.dir.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if e != nil {
		return domain.ErrUnavailable
	}
	f := os.NewFile(uintptr(fd), name)
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return domain.ErrUnavailable
	}
	if !create && unix.Renameat(int(s.dir.Fd()), name, int(s.dir.Fd()), s.name) != nil {
		return domain.ErrUnavailable
	}
	if s.dir.Sync() != nil {
		return domain.ErrUnavailable
	}
	return nil
}
func (s *BackupStateStore) update(ctx context.Context, fn func(*backupStateDocument)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return domain.ErrUnavailable
	}
	doc, e := s.read()
	if e != nil {
		return e
	}
	fn(&doc)
	return s.write(doc, false)
}
func (s *BackupStateStore) LoadOperation(ctx context.Context) (domain.BackupOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return domain.BackupOperation{}, domain.ErrUnavailable
	}
	doc, e := s.read()
	return doc.Operation, e
}
func (s *BackupStateStore) SaveOperation(ctx context.Context, o domain.BackupOperation) error {
	return s.update(ctx, func(d *backupStateDocument) { d.Operation = o })
}
func (s *BackupStateStore) LoadSchedule(ctx context.Context) (domain.BackupSchedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return domain.BackupSchedule{}, domain.ErrUnavailable
	}
	doc, e := s.read()
	return doc.Schedule, e
}
func (s *BackupStateStore) SaveSchedule(ctx context.Context, o domain.BackupSchedule) error {
	return s.update(ctx, func(d *backupStateDocument) { d.Schedule = o })
}
func (s *BackupStateStore) LoadProcess(ctx context.Context) (domain.BackupProcessState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return domain.BackupProcessState{}, domain.ErrUnavailable
	}
	doc, e := s.read()
	return doc.Process, e
}
func (s *BackupStateStore) SaveProcess(ctx context.Context, o domain.BackupProcessState) error {
	return s.update(ctx, func(d *backupStateDocument) { d.Process = o })
}
