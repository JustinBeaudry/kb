//go:build acceptance

package acceptance

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFixtureDiagnosticsRetainClosedStartupEvidence(t *testing.T) {
	script := fixtureScriptSummary([]byte("raw fixture-secret\nfixture_stage=build\nfixture_build_seconds=81\nfixture_stage=host_setup\nfixture_host_operation_failed_no_retry\nfixture_bucket_status=403\n"))
	assert.Equal(t, "host_setup", script["stage"])
	assert.Equal(t, "fixture_host_operation_failed_no_retry", script["failure"])
	assert.Equal(t, 81, script["build_seconds"])
	assert.Equal(t, 403, script["bucket_status"])
	host := fixtureHostSummary([]byte(`{"Version":1,"Code":"ok","NextAction":"fixture-secret","Status":{"HostReady":false,"Maintenance":{"phase":"bootstrap","active":1,"unknown":0,"fence":{"operation_id":"fixture-secret"}}}}`))
	assert.Equal(t, "ok", host["code"])
	assert.Equal(t, false, host["ready"])
	assert.Equal(t, "bootstrap", host["phase"])
	assert.Equal(t, 1, host["active"])
	assert.Equal(t, 0, host["unknown"])
	code, containers := fixtureContainerSummary([]byte(`[{"Service":"graph","State":"exited","ExitCode":137,"OOMKilled":true,"Health":"","Command":"fixture-secret","Name":"fixture-secret"}]`))
	assert.Equal(t, "ok", code)
	require.Len(t, containers, 1)
	assert.Equal(t, "graph", containers[0]["service"])
	assert.Equal(t, 137, containers[0]["exit_code"])
	assert.Equal(t, true, containers[0]["oom_killed"])
	assert.Equal(t, "exited", containers[0]["state"])
	for _, value := range []any{script, host, containers} {
		raw, err := json.Marshal(value)
		require.NoError(t, err)
		assert.NotContains(t, string(raw), "fixture-secret")
	}
}

func TestFixtureDiagnosticsRejectUnknownMalformedAndOversizedInput(t *testing.T) {
	for i, raw := range [][]byte{nil, []byte("fixture-secret"), []byte(`{"Code":"fixture-secret","Status":{"HostReady":"fixture-secret"}}`), []byte(strings.Repeat("fixture-secret", fixtureDiagnosticLimit))} {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			for _, value := range []any{fixtureScriptSummary(raw), fixtureHostSummary(raw), fixtureAdminCode(raw)} {
				encoded, err := json.Marshal(value)
				require.NoError(t, err)
				assert.NotContains(t, string(encoded), "fixture-secret")
				assert.Less(t, len(encoded), 512)
			}
			code, containers := fixtureContainerSummary(raw)
			assert.NotEqual(t, "ok", code)
			assert.Empty(t, containers)
		})
	}
	assert.Equal(t, "oversized", fixtureHostSummary(make([]byte, fixtureDiagnosticLimit+1))["result"])
	assert.Equal(t, "oversized", fixtureScriptSummary(make([]byte, fixtureDiagnosticLimit+1))["result"])
	assert.Equal(t, "oversized", fixtureAdminCode(make([]byte, fixtureDiagnosticLimit+1)))
	assert.Equal(t, "unrecognized", fixtureAdminCode([]byte("setup: fixture-secret: leaked text")))
	assert.NotContains(t, fixtureScriptSummary([]byte("fixture_stage=fixture-secret\nfixture_build_seconds=fixture-secret\nfixture_secret=fixture-secret")), "stage")
	host := fixtureHostSummary([]byte(`{"Version":1,"Code":"fixture-secret","Status":{"HostReady":true,"Maintenance":{"phase":"fixture-secret","active":-1,"unknown":0}}}`))
	assert.Equal(t, "invalid", host["result"])
}

func TestFixtureDiagnosticsBoundChildOutputThroughIOCopy(t *testing.T) {
	// LimitReader has no WriterTo: exercise the path os/exec uses for child pipes.
	input := io.LimitReader(strings.NewReader(strings.Repeat("x", fixtureDiagnosticLimit*3)), fixtureDiagnosticLimit*3)
	var output diagnosticBuffer
	n, err := io.Copy(&output, input)
	require.NoError(t, err)
	assert.Equal(t, int64(fixtureDiagnosticLimit*3), n, "overflow must be drained")
	assert.Equal(t, fixtureDiagnosticLimit, output.buffer.Len())
	assert.True(t, output.oversized)
}

func TestFixtureDiagnosticsReadOnlyBoundedCompleteLogTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "synthetic.log")
	require.NoError(t, os.WriteFile(path, []byte(strings.Repeat("fixture-secret", fixtureDiagnosticLimit)+"\nfixture_stage=api_readiness\nfixture_readiness_failed_inspect_private_status\n"), 0600))
	raw, code := readFixtureDiagnostic(path, true)
	assert.Equal(t, "tail", code)
	assert.LessOrEqual(t, len(raw), fixtureDiagnosticLimit)
	assert.NotContains(t, string(raw), "fixture-secret")
	assert.Equal(t, "api_readiness", fixtureScriptSummary(raw)["stage"])
	raw, code = readFixtureDiagnostic(path, false)
	assert.Equal(t, "oversized", code)
	assert.Empty(t, raw)
}

func TestFixtureDiagnosticsOnlyInspectBoundedContainerIdentities(t *testing.T) {
	ids, ok := fixtureContainerIDs([]byte(strings.Repeat("a", 64) + "\n" + strings.Repeat("b", 12) + "\n"))
	require.True(t, ok)
	assert.Len(t, ids, 2)
	for _, raw := range []string{"--format fixture-secret", "fixture-secret", strings.Repeat("a", 65), strings.Repeat(strings.Repeat("a", 64)+"\n", 6), strings.Repeat("a", fixtureDiagnosticLimit+1)} {
		ids, ok := fixtureContainerIDs([]byte(raw))
		assert.False(t, ok)
		assert.Empty(t, ids)
	}
}

func TestFixtureDiagnosticsClassifyAdminErrorsWithoutPrivateIdentity(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"setup: maintenance: admission refused; retry after the current maintenance operation completes\n", "maintenance"},
		{"journal-init: administration_unavailable: command or credential transfer failed; backup/restore recovery_unavailable\n", "administration_unavailable"},
		{"policy-update: invalid_input: check kb-admin help and protected descriptor prerequisites\n", "invalid_input"},
		{"unknown_outcome: operation=fixture-secret stage=finish; reconcile this operation before retry\n", "unknown_outcome_finish"},
		{"unknown_outcome: operation=fixture-secret stage=reconcile_operation; reconcile this operation before retry\n", "unknown_outcome_reconcile_operation"},
		{"fixture_command_timeout_unknown\n", "command_timeout_unknown"},
		{"setup: maintenance: fixture-secret\n", "maintenance"},
	} {
		assert.Equal(t, tc.want, fixtureAdminCode([]byte(tc.raw)))
	}
}

func TestFixtureDiagnosticsAcceptBothComposeJSONFormats(t *testing.T) {
	code, rows := fixtureContainerSummary([]byte("{\"Service\":\"api\",\"State\":\"running\",\"ExitCode\":0,\"Health\":\"\"}\n{\"Service\":\"objects\",\"State\":\"exited\",\"ExitCode\":1,\"Health\":\"unhealthy\"}\n"))
	assert.Equal(t, "ok", code)
	assert.Len(t, rows, 2)
	for _, raw := range []string{`[{"Service":"fixture-secret","State":"running","ExitCode":0}]`, `[{"Service":"graph","State":"fixture-secret","ExitCode":0}]`, `[{"Service":"graph","State":"running","ExitCode":999}]`, `[{"Service":"graph","State":"running","ExitCode":0,"Health":"fixture-secret"}]`} {
		code, rows := fixtureContainerSummary([]byte(raw))
		assert.Equal(t, "invalid", code)
		assert.Empty(t, rows)
	}
}
