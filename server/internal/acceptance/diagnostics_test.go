//go:build acceptance

package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const fixtureDiagnosticLimit = 64 << 10

func diagnosticInput(raw []byte) string {
	if len(raw) == 0 {
		return "missing"
	}
	if len(raw) > fixtureDiagnosticLimit {
		return "oversized"
	}
	return "ok"
}
func fixtureScriptSummary(raw []byte) map[string]any {
	out := map[string]any{"result": diagnosticInput(raw)}
	if out["result"] != "ok" {
		return out
	}
	stages := []string{"configure", "build", "compose_up", "bindings", "api_liveness", "proxy_liveness", "object_readiness", "bucket_create", "bucket_verify", "host_setup", "host_journal_init", "host_policy_update", "credential_transfer", "api_readiness", "complete"}
	failures := []string{"fixture_configuration_failed", "fixture_loopback_binding_invalid", "fixture_liveness_failed", "fixture_proxy_readiness_failed", "fixture_object_readiness_failed", "fixture_bucket_init_unknown", "fixture_bucket_init_failed", "fixture_host_bootstrap_wait_failed", "fixture_host_result_unknown", "fixture_host_operation_failed_no_retry", "fixture_host_admission_wait_failed", "fixture_readiness_failed_inspect_private_status", "fixture_command_timeout_unknown"}
	for _, line := range strings.Split(string(raw), "\n") {
		if slices.Contains(failures, line) {
			out["failure"] = line
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if key == "fixture_stage" && slices.Contains(stages, value) {
			out["stage"] = value
		}
		for _, field := range []string{"build_seconds", "startup_seconds", "bucket_status"} {
			if key == "fixture_"+field {
				n, e := strconv.Atoi(value)
				if e == nil && n >= 0 && n <= 86400 {
					out[field] = n
				}
			}
		}
	}
	return out
}
func fixtureHostSummary(raw []byte) map[string]any {
	out := map[string]any{"result": diagnosticInput(raw)}
	if out["result"] != "ok" {
		return out
	}
	var v struct {
		Version int
		Code    string
		Status  struct {
			HostReady   *bool
			Maintenance struct {
				Phase           string
				Active, Unknown *int
			}
		}
	}
	if json.Unmarshal(raw, &v) != nil || v.Version != 1 || !slices.Contains([]string{"ok", "invalid_input", "maintenance", "conflict", "unknown_outcome", "unavailable"}, v.Code) {
		out["result"] = "invalid"
		return out
	}
	out["code"] = v.Code
	m := v.Status.Maintenance
	if v.Status.HostReady == nil || m.Active == nil || m.Unknown == nil || *m.Active < 0 || *m.Active > 1000000 || *m.Unknown < 0 || *m.Unknown > 1000000 || !slices.Contains([]string{"bootstrap", "open", "draining", "offline", "recovering", "failed"}, m.Phase) {
		out["result"] = "invalid"
		return out
	}
	out["ready"], out["phase"], out["active"], out["unknown"] = *v.Status.HostReady, m.Phase, *m.Active, *m.Unknown
	return out
}
func fixtureAdminCode(raw []byte) string {
	if result := diagnosticInput(raw); result != "ok" {
		return result
	}
	line := strings.TrimSpace(string(raw))
	if line == "fixture_command_timeout_unknown" {
		return "command_timeout_unknown"
	}
	for _, operation := range []string{"setup", "journal-init", "policy-update"} {
		line = strings.TrimPrefix(line, operation+": ")
	}
	code, _, ok := strings.Cut(line, ": ")
	if !ok {
		return "unrecognized"
	}
	if !slices.Contains([]string{"maintenance", "stale", "unknown_outcome", "conflict", "invalid_input", "administration_unavailable"}, code) {
		return "unrecognized"
	}
	if code == "unknown_outcome" {
		for _, stage := range []string{"reconcile_operation", "finish"} {
			if strings.Contains(line, " stage="+stage+"; reconcile this operation before retry") {
				return code + "_" + stage
			}
		}
	}
	return code
}
func fixtureContainerSummary(raw []byte) (string, []map[string]any) {
	if result := diagnosticInput(raw); result != "ok" {
		return result, nil
	}
	type item struct {
		Service, State, Health string
		ExitCode               *int
		OOMKilled              *bool
	}
	var items []item
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
		if json.Unmarshal(raw, &items) != nil {
			return "invalid", nil
		}
	} else {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		for {
			var v item
			e := decoder.Decode(&v)
			if e == io.EOF {
				break
			}
			if e != nil {
				return "invalid", nil
			}
			items = append(items, v)
			if len(items) > 5 {
				return "invalid", nil
			}
		}
	}
	if len(items) > 5 {
		return "invalid", nil
	}
	out := make([]map[string]any, 0, len(items))
	seen := map[string]bool{}
	for _, v := range items {
		if !slices.Contains([]string{"graph", "api", "worker", "objects", "proxy"}, v.Service) || seen[v.Service] || !slices.Contains([]string{"created", "running", "paused", "restarting", "removing", "exited", "dead"}, v.State) || !slices.Contains([]string{"", "starting", "healthy", "unhealthy"}, v.Health) || v.ExitCode == nil || *v.ExitCode < 0 || *v.ExitCode > 255 {
			return "invalid", nil
		}
		seen[v.Service] = true
		row := map[string]any{"service": v.Service, "state": v.State, "health": v.Health, "exit_code": *v.ExitCode}
		if v.OOMKilled != nil {
			row["oom_killed"] = *v.OOMKilled
		}
		out = append(out, row)
	}
	return "ok", out
}

// Reads only named diagnostic files. A bounded log tail retains post-build
// startup failures; JSON/stderr inputs reject oversize rather than parse fragments.
func readFixtureDiagnostic(path string, tail bool) ([]byte, string) {
	f, e := os.Open(path)
	if e != nil {
		return nil, "missing"
	}
	defer func() { _ = f.Close() }()
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() {
		return nil, "unavailable"
	}
	result := "ok"
	if st.Size() > fixtureDiagnosticLimit {
		if !tail {
			return nil, "oversized"
		}
		if _, e = f.Seek(-fixtureDiagnosticLimit, io.SeekEnd); e != nil {
			return nil, "unavailable"
		}
		result = "tail"
	}
	raw, e := io.ReadAll(io.LimitReader(f, fixtureDiagnosticLimit+1))
	if e != nil {
		return nil, "unavailable"
	}
	if len(raw) > fixtureDiagnosticLimit {
		return nil, "oversized"
	}
	if result == "tail" {
		if i := bytes.IndexByte(raw, '\n'); i >= 0 {
			raw = raw[i+1:]
		} else {
			raw = nil
		}
	}
	return raw, result
}

// Discard overflow while draining the child, so diagnostics cannot grow memory
// without bound or change the child's exit status through an induced broken pipe.
type diagnosticBuffer struct {
	buffer    bytes.Buffer
	oversized bool
}

func (b *diagnosticBuffer) Write(p []byte) (int, error) {
	n := len(p)
	room := fixtureDiagnosticLimit - b.buffer.Len()
	if len(p) > room {
		b.oversized = true
		p = p[:room]
	}
	_, _ = b.buffer.Write(p)
	return n, nil
}

func (f *compiledFixture) logStartupFailure(scriptPath string, runErr error) {
	summary := map[string]any{"kind": "compiled_fixture_failure", "command": "failed"}
	var filesystem syscall.Statfs_t
	if syscall.Statfs(f.dir, &filesystem) == nil {
		summary["disk_available_bytes"] = uint64(filesystem.Bavail) * uint64(filesystem.Bsize)
	}
	var exit *exec.ExitError
	if errors.As(runErr, &exit) {
		summary["exit_code"] = exit.ExitCode()
	}
	if errors.Is(f.ctx.Err(), context.DeadlineExceeded) {
		summary["command"] = "deadline"
	} else if errors.Is(f.ctx.Err(), context.Canceled) {
		summary["command"] = "canceled"
	}
	raw, result := readFixtureDiagnostic(scriptPath, true)
	summary["script_input"] = result
	summary["script"] = fixtureScriptSummary(raw)
	raw, result = readFixtureDiagnostic(filepath.Join(f.dir, "host-status.json"), false)
	summary["host_input"] = result
	summary["host"] = fixtureHostSummary(raw)
	operations := map[string]string{}
	for _, name := range []string{"host-status", "setup", "journal-init", "policy-update"} {
		raw, result = readFixtureDiagnostic(filepath.Join(f.dir, name+".stderr"), false)
		if result == "ok" {
			operations[name] = fixtureAdminCode(raw)
		} else {
			operations[name] = result
		}
	}
	summary["operation_errors"] = operations
	// Only a strict fixture identity is used as a Docker argument. Never emit the
	// runtime document, config, object payloads, credentials, or arbitrary log text.
	raw, result = readFixtureDiagnostic(filepath.Join(f.dir, "runtime.json"), false)
	var runtime map[string]string
	summary["containers_result"] = "not_configured"
	if result == "ok" && json.Unmarshal(raw, &runtime) == nil && regexp.MustCompile(`^kbq-[0-9a-f]{12}$`).MatchString(runtime["compose_project"]) {
		code, containers := f.collectContainerDiagnostics(runtime["compose_project"])
		summary["containers_result"], summary["containers"] = code, containers
	}
	encoded, e := json.Marshal(summary)
	if e != nil {
		f.t.Log("compiled_fixture_failure_summary_unavailable")
		return
	}
	f.t.Logf("compiled_fixture_diagnostic=%s", encoded)
}

func fixtureContainerIDs(raw []byte) ([]string, bool) {
	if len(raw) > fixtureDiagnosticLimit {
		return nil, false
	}
	ids := strings.Fields(string(raw))
	if len(ids) > 5 {
		return nil, false
	}
	for _, id := range ids {
		if !regexp.MustCompile(`^[a-f0-9]{12,64}$`).MatchString(id) {
			return nil, false
		}
	}
	return ids, true
}
func (f *compiledFixture) collectContainerDiagnostics(project string) (string, []map[string]any) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "compose", "--env-file", filepath.Join(f.dir, "compose.env"), "-p", project, "-f", filepath.Join(f.repo, "deploy/compose.test.yaml"), "ps", "--all", "--quiet")
	var output diagnosticBuffer
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if e := cmd.Run(); e != nil {
		if ctx.Err() != nil {
			return "deadline", nil
		}
		return "list_failed", nil
	}
	if output.oversized {
		return "oversized", nil
	}
	ids, ok := fixtureContainerIDs(output.buffer.Bytes())
	if !ok {
		return "invalid_ids", nil
	}
	if len(ids) == 0 {
		return "ok", []map[string]any{}
	}
	// Docker emits only these explicit fields, never Config.Env, command arguments,
	// healthcheck output, error strings, container names, or IDs.
	format := `{"Service":{{json (index .Config.Labels "com.docker.compose.service")}},"State":{{json .State.Status}},"ExitCode":{{json .State.ExitCode}},"OOMKilled":{{json .State.OOMKilled}},"Health":{{if .State.Health}}{{json .State.Health.Status}}{{else}}""{{end}}}`
	args := append([]string{"inspect", "--format", format}, ids...)
	cmd = exec.CommandContext(ctx, "docker", args...)
	var state diagnosticBuffer
	cmd.Stdout = &state
	cmd.Stderr = io.Discard
	if e := cmd.Run(); e != nil {
		if ctx.Err() != nil {
			return "deadline", nil
		}
		return "inspect_failed", nil
	}
	if state.oversized {
		return "oversized", nil
	}
	return fixtureContainerSummary(state.buffer.Bytes())
}
