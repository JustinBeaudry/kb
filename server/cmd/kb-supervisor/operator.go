package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
)

type operatorRunner interface {
	Run(context.Context, string) (domain.BackupResult, error)
	Restore(context.Context, string, string) (domain.BackupResult, error)
	Recover(context.Context, string) error
	Pause(context.Context, string) error
}
type operatorRequest struct {
	Version              int
	Action, ID, BackupID string
}
type operatorStatus struct {
	HostReady                          bool
	Maintenance                        domain.MaintenanceState
	Operation                          domain.BackupOperation
	Schedule                           domain.BackupSchedule
	JournalSequence, JournalCapacity   uint64
	JournalCapacityWarning             bool
	BackupAgeSeconds                   int64
	DiskAvailableBytes, DiskTotalBytes uint64
	DiskUsageWarning                   bool
}
type operatorResponse struct {
	Version          int
	Code, NextAction string
	Result           domain.BackupResult
	Status           operatorStatus
}

type privateStatusAdmission interface {
	Admit(context.Context, string, string) (domain.OperationPermit, error)
	Finish(context.Context, domain.OperationPermit) error
	MarkUnknown(context.Context, domain.OperationPermit) error
}
type privateStatusJournal interface {
	CheckHealth(context.Context) (uint64, uint64, error)
}

func privateJournalStatus(ctx context.Context, admission privateStatusAdmission, journal privateStatusJournal, instance string) (uint64, uint64, error) {
	ctx = domain.WithOperationOutcome(ctx)
	permit, err := admission.Admit(ctx, instance, "private-status")
	if err != nil {
		return 0, 0, err
	}
	sequence, capacity, err := journal.CheckHealth(ctx)
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if !domain.OperationSettled(ctx) {
		_ = admission.MarkUnknown(cleanup, permit)
		return sequence, capacity, domain.ErrUnknownOutcome
	}
	if admission.Finish(cleanup, permit) != nil {
		return sequence, capacity, domain.ErrUnavailable
	}
	return sequence, capacity, err
}

func operatorCode(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, domain.ErrInvalidInput):
		return "invalid_input"
	case errors.Is(err, domain.ErrMaintenance):
		return "maintenance"
	case errors.Is(err, domain.ErrConflict):
		return "conflict"
	case errors.Is(err, domain.ErrUnknownOutcome):
		return "unknown_outcome"
	default:
		return "unavailable"
	}
}
func dispatchOperator(ctx context.Context, runner operatorRunner, status func(context.Context) (operatorStatus, error), r operatorRequest) operatorResponse {
	out := operatorResponse{Version: 1, NextAction: "inspect_status_and_recovery_runbook"}
	if r.Version != 1 || domain.ValidateIDs(r.ID) != nil && r.Action != "status" {
		out.Code = "invalid_input"
		return out
	}
	var err error
	switch r.Action {
	case "backup":
		out.Result, err = runner.Run(ctx, r.ID)
	case "restore":
		out.Result, err = runner.Restore(ctx, r.BackupID, r.ID)
	case "recover":
		err = runner.Recover(ctx, r.ID)
	case "pause":
		err = runner.Pause(ctx, r.ID)
	case "status":
		out.Status, err = status(ctx)
	default:
		err = domain.ErrInvalidInput
	}
	out.Code = operatorCode(err)
	if err == nil {
		out.NextAction = "none"
	}
	return out
}
func serveOperator(ctx context.Context, listener *net.UnixListener, runner operatorRunner, status func(context.Context) (operatorStatus, error)) error {
	var workers sync.WaitGroup
	defer workers.Wait()
	go func() { <-ctx.Done(); _ = listener.Close() }()
	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return domain.ErrUnavailable
		}
		if gateway.ValidateUnixPeer(conn, os.Geteuid()) != nil {
			_ = conn.Close()
			continue
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() { _ = conn.Close() }()
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			var request operatorRequest
			dec := json.NewDecoder(io.LimitReader(conn, 16385))
			dec.DisallowUnknownFields()
			if dec.Decode(&request) != nil || dec.Decode(new(any)) != io.EOF {
				_ = json.NewEncoder(conn).Encode(operatorResponse{Version: 1, Code: "invalid_input"})
				return
			}
			work, cancel := context.WithTimeout(ctx, 4*time.Hour)
			defer cancel()
			response := dispatchOperator(work, runner, status, request)
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			_ = json.NewEncoder(conn).Encode(response)
		}()
	}
}
func callOperator(ctx context.Context, path string, r operatorRequest, stdout io.Writer) error {
	// Check the named socket before dialing, then authenticate the connected peer.
	resolved, e := filepath.EvalSymlinks(filepath.Dir(path))
	if e != nil || resolved != filepath.Dir(path) {
		return domain.ErrForbidden
	}
	info, e := os.Lstat(path)
	if e != nil {
		return domain.ErrUnavailable
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 {
		return domain.ErrForbidden
	}
	dialer := net.Dialer{}
	connection, e := dialer.DialContext(ctx, "unix", path)
	if e != nil {
		return domain.ErrUnavailable
	}
	defer func() { _ = connection.Close() }()
	conn, ok := connection.(*net.UnixConn)
	if !ok || gateway.ValidateUnixPeer(conn, os.Geteuid()) != nil {
		return domain.ErrForbidden
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if json.NewEncoder(conn).Encode(r) != nil || conn.CloseWrite() != nil {
		return domain.ErrUnavailable
	}
	var response operatorResponse
	dec := json.NewDecoder(io.LimitReader(conn, 65537))
	dec.DisallowUnknownFields()
	if dec.Decode(&response) != nil || dec.Decode(new(any)) != io.EOF || response.Version != 1 {
		return domain.ErrUnknownOutcome
	}
	if json.NewEncoder(stdout).Encode(response) != nil {
		return domain.ErrUnavailable
	}
	if response.Code != "ok" {
		return domain.ErrUnavailable
	}
	return nil
}
