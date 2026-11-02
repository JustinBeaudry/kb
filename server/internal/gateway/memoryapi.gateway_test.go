package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway/memoryapi"
	"github.com/JustinBeaudry/kb/server/internal/handler"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const syntheticBearer = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type recorded struct {
	method, path, query, auth, body string
}
type apiFixture struct {
	mu       sync.Mutex
	requests []recorded
	status   int
	body     string
}

func (f *apiFixture) serve(t *testing.T) *MemoryAPI {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, recorded{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), string(body)})
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		_, _ = io.WriteString(w, f.body)
	}))
	t.Cleanup(server.Close)
	api, err := NewMemoryAPI(server.URL, syntheticBearer, 5*time.Second)
	require.NoError(t, err)
	return api
}

// retrievalBody carries a real-format signed passage cursor (start 3, next 8).
var retrievalBody = `{"schema_version":"3","chunks":[{"record_id":"a","revision":"d","text":"<猫>","claim_status":"candidate","origin_trust":"submitted","evidence":[{"artifact_id":"a","digest":"d","locator":"l","source_revision":"g","origin":{"client_id":"collector","trust":"submitted"},"applicability":{}}],"warnings":["source_evidence_is_not_a_verified_claim","unverified_task_evidence","partial_source_passage"],"passage":{"start_byte":3,"end_byte":8,"source_bytes":40000}}],"truncated":true,"truncation_reason":"wire_budget","continuation":"` + testPassageCursor(3, 8) + `","visited":0,"no_results":false}`

// A malformed success receipt for a write arrives after the server may have
// committed, so writes report unknown_outcome rather than unavailable.
func malformedErr(operation string) error {
	if operation == "submit" || operation == "checkpoint" {
		return domain.ErrUnknownOutcome
	}
	return domain.ErrUnavailable
}

func TestMemoryAPIRejectsUnsafeConfiguration(t *testing.T) {
	for _, tc := range []struct{ origin, bearer string }{
		{"http://example.test", syntheticBearer},
		{"https://user:pass@example.test", syntheticBearer},
		{"https://example.test/prefix", syntheticBearer},
		{"https://example.test/?x=1", syntheticBearer},
		{"ftp://example.test", syntheticBearer},
		{"https://example.test", "short"},
		{"https://example.test", strings.ToUpper(syntheticBearer)},
	} {
		t.Run(tc.origin+tc.bearer[:4], func(t *testing.T) {
			_, err := NewMemoryAPI(tc.origin, tc.bearer, time.Second)

			assert.ErrorIs(t, err, domain.ErrInvalidInput)
		})
	}
	for _, origin := range []string{"https://example.test", "https://example.test/", "http://127.0.0.1:9", "http://[::1]:9"} {
		_, err := NewMemoryAPI(origin, syntheticBearer, time.Second)
		assert.NoError(t, err, origin)
	}
}

func TestMemoryAPIMapsProblemsToClosedErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   error
	}{
		{401, `{"code":"unauthorized","message":"REMOTE-DETAIL"}`, domain.ErrUnauthorized},
		{403, `{"code":"forbidden"}`, domain.ErrForbidden},
		{409, `{"code":"conflict"}`, domain.ErrConflict},
		{409, `{"code":"stale"}`, domain.ErrStale},
		{410, `{"code":"deleted"}`, domain.ErrDeleted},
		{413, `{"code":"budget"}`, domain.ErrBudget},
		{413, `{"code":"invalid_input"}`, domain.ErrInvalidInput},
		{400, `{"code":"invalid_input"}`, domain.ErrInvalidInput},
		{503, `{"code":"incomplete"}`, domain.ErrIncomplete},
		{503, `{"code":"unavailable"}`, domain.ErrUnavailable},
		{500, `not json`, domain.ErrUnavailable},
		{200, `not json`, domain.ErrUnavailable},
		{200, `{"schema_version":"2","chunks":[]}`, domain.ErrUnavailable},
		{200, `{"schema_version":"3","pad":"` + strings.Repeat("x", 300<<10) + `"}`, domain.ErrUnavailable},
	} {
		t.Run(http.StatusText(tc.status)+tc.body[:min(12, len(tc.body))], func(t *testing.T) {
			f := &apiFixture{status: tc.status, body: tc.body}
			api := f.serve(t)

			_, err := api.Retrieve(t.Context(), domain.RetrievalRequest{Scope: domain.Scope{ProjectID: "demo"}, Mode: domain.RetrievalEvidence, BudgetBytes: 100})

			assert.ErrorIs(t, err, tc.want)
			assert.NotContains(t, err.Error(), "REMOTE-DETAIL")
		})
	}
}

func TestMemoryAPINeverFollowsRedirectsWithBearer(t *testing.T) {
	var leaked sync.Map
	target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		leaked.Store("authorization", r.Header.Get("Authorization"))
	}))
	t.Cleanup(target.Close)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/status", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(origin.Close)
	api, err := NewMemoryAPI(origin.URL, syntheticBearer, 5*time.Second)
	require.NoError(t, err)

	_, err = api.Status(t.Context())

	assert.ErrorIs(t, err, domain.ErrUnavailable)
	_, reached := leaked.Load("authorization")
	assert.False(t, reached, "redirect target never contacted")
}

func TestMemoryAPIRequestsAndNormalizedResponses(t *testing.T) {
	end := int64(99)
	f := &apiFixture{status: 200, body: retrievalBody}
	api := f.serve(t)

	searchBody := strings.Replace(retrievalBody, `,"passage":{"start_byte":3,"end_byte":8,"source_bytes":40000}`, ``, 1)
	searchBody = strings.Replace(searchBody, `"claim_status":"candidate"`, `"claim_status":"verified"`, 1)
	f.body = searchBody
	orient, err := api.Retrieve(t.Context(), domain.RetrievalRequest{Scope: domain.Scope{ProjectID: "demo", TaskID: "task"}, Mode: domain.RetrievalOrientation, BudgetBytes: 4000, MaxDepth: 0, MaxVisited: 9, Query: "q", EntityIDs: []string{"api"}, Applicability: domain.Applicability{Environment: "ci"}})
	require.NoError(t, err)
	f.body = strings.Replace(searchBody, `"claim_status":"verified"`, `"claim_status":"candidate"`, 1)
	f.body = strings.Replace(f.body, `"warnings":[`, `"warnings":["unverified_task_candidate",`, 1)
	working, err := api.Retrieve(t.Context(), domain.RetrievalRequest{Scope: domain.Scope{ProjectID: "demo", TaskID: "task"}, Mode: domain.RetrievalWorking, BudgetBytes: 9000, MaxDepth: 2, MaxVisited: 50})
	require.NoError(t, err)
	f.body = retrievalBody
	fetch, err := api.Fetch(t.Context(), domain.FetchRequest{ArtifactID: "a", Locator: "l", Mode: domain.FetchWorking, Scope: domain.Scope{ProjectID: "demo", TaskID: "task"}, BudgetBytes: 8000, StartByte: 3, EndByte: &end})
	require.NoError(t, err)

	require.Len(t, f.requests, 3)
	for _, r := range f.requests {
		assert.Equal(t, "Bearer "+syntheticBearer, r.auth)
	}
	assert.Equal(t, "/v1/retrieval:orient", f.requests[0].path)
	assert.JSONEq(t, `{"scope":{"project_id":"demo","task_id":"task"},"query":"q","entity_ids":["api"],"applicability":{"environment":"ci"},"budget_bytes":4000,"max_depth":0,"max_visited":9,"mode":"orientation"}`, f.requests[0].body)
	assert.Equal(t, "/v1/retrieval:search", f.requests[1].path)
	assert.Contains(t, f.requests[1].body, `"mode":"working"`)
	assert.Equal(t, "GET", f.requests[2].method)
	assert.Equal(t, "/v1/evidence/a", f.requests[2].path)
	for _, part := range []string{"mode=working", "project_id=demo", "task_id=task", "start_byte=3", "end_byte=99", "budget_bytes=8000", "locator=l"} {
		assert.Contains(t, f.requests[2].query, part)
	}
	require.Len(t, orient.Chunks, 1)
	c := orient.Chunks[0]
	assert.Equal(t, "<猫>", c.Text)
	assert.Equal(t, domain.ClaimVerified, c.ClaimStatus)
	assert.Nil(t, c.Passage)
	assert.Equal(t, "collector", c.Evidence[0].Origin.ClientID)
	assert.Equal(t, testPassageCursor(3, 8), orient.Continuation)
	assert.Equal(t, "wire_budget", orient.TruncationReason)
	assert.Equal(t, domain.ClaimCandidate, working.Chunks[0].ClaimStatus)
	assert.Equal(t, &domain.Passage{StartByte: 3, EndByte: 8, SourceBytes: 40000}, fetch.Chunks[0].Passage)
}

func TestMemoryAPIWritesAndStatus(t *testing.T) {
	f := &apiFixture{status: 201, body: `{"schema_version":"3","claim":{"claim_id":"c","revision":"r1","text":"x","kind":"finding","scope":{"project_id":"demo","task_id":"task"},"evidence":[],"depends_on":[],"applicability":{},"claim_status":"candidate","origin_trust":"submitted"}}`}
	api := f.serve(t)

	claim, err := api.Submit(t.Context(), domain.CandidateInput{ClaimID: "c", Text: "x", Kind: "finding", Scope: domain.Scope{ProjectID: "demo", TaskID: "task"}, Evidence: []domain.EvidenceRef{{ArtifactID: "a", Digest: "d", Locator: "l", SourceRevision: "g", Origin: domain.Origin{ClientID: "owner", Trust: domain.TrustOwnerAuthenticated}}}})
	require.NoError(t, err)
	assert.Equal(t, domain.ClaimCandidate, claim.Status)
	assert.NotContains(t, f.requests[0].body, "owner", "origin is derived by the server, never submitted")
	var submitted map[string]any
	require.NoError(t, json.Unmarshal([]byte(f.requests[0].body), &submitted))
	assert.Equal(t, []any{}, submitted["depends_on"])

	f.status, f.body = 200, `{"schema_version":"3","checkpoint":{"id":"cp","revision":"r2","scope":{"project_id":"demo","task_id":"task"},"objective":"o","completed":[],"pending":[],"questions":[],"evidence_revision_ids":[],"updated_at":"2026-10-30T00:00:00Z"}}`
	cp, err := api.PutCheckpoint(t.Context(), "r1", domain.Checkpoint{ID: "cp", Scope: domain.Scope{ProjectID: "demo", TaskID: "task"}, Objective: "o"})
	require.NoError(t, err)
	assert.Equal(t, "r2", cp.Revision)
	assert.Equal(t, "PUT", f.requests[1].method)
	assert.Contains(t, f.requests[1].body, `"expected_revision":"r1"`)

	f.body = `{"schema_version":"3","connection_state":"available","authorized_project_ids":["demo"],"authorized_capabilities":["status:read"],"capability_gaps":["recovery_unavailable"]}`
	status, err := api.Status(t.Context())
	require.NoError(t, err)
	assert.Equal(t, domain.Status{ConnectionState: "available", AuthorizedProjectIDs: []string{"demo"}, AuthorizedCapabilities: []string{"status:read"}, CapabilityGaps: []string{"recovery_unavailable"}}, status)

	f.body = `{"schema_version":"2"}`
	_, err = api.Status(t.Context())
	assert.ErrorIs(t, err, domain.ErrUnavailable)
}

func TestMemoryAPITransportFailureIsUnavailable(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	origin := server.URL
	server.Close()
	api, err := NewMemoryAPI(origin, syntheticBearer, time.Second)
	require.NoError(t, err)

	_, err = api.Fetch(t.Context(), domain.FetchRequest{ArtifactID: "a", Locator: "l", BudgetBytes: 10})

	assert.ErrorIs(t, err, domain.ErrUnavailable)
}

func TestMemoryAPIRejectsMalformedSuccessResponses(t *testing.T) {
	const checkpoint = `{"id":"cp","revision":"r2","scope":{"project_id":"demo","task_id":"task"},"objective":"o","completed":[],"pending":[],"questions":[],"evidence_revision_ids":[],"updated_at":"2026-10-30T00:00:00Z"}`
	const candidate = `{"schema_version":"3","claim":{"claim_id":"c","revision":"r1","text":"x","kind":"finding","scope":{"project_id":"demo","task_id":"task"},"evidence":[],"depends_on":[],"applicability":{},"claim_status":"candidate","origin_trust":"submitted"}}`
	const status = `{"schema_version":"3","connection_state":"available","authorized_project_ids":["demo"],"authorized_capabilities":["status:read"],"capability_gaps":[]}`
	for _, tc := range []struct {
		name, operation, body string
	}{
		{"missing required", "retrieve", `{"schema_version":"3"}`},
		{"null required array", "retrieve", `{"schema_version":"3","chunks":null,"truncated":false,"visited":0,"no_results":true}`},
		{"invalid claim status", "retrieve", strings.Replace(retrievalBody, `"claim_status":"candidate"`, `"claim_status":"REMOTE-SECRET"`, 1)},
		{"invalid chunk trust", "retrieve", strings.Replace(retrievalBody, `"origin_trust":"submitted"`, `"origin_trust":"REMOTE-SECRET"`, 1)},
		{"invalid nested trust", "retrieve", strings.Replace(retrievalBody, `"trust":"submitted"`, `"trust":"REMOTE-SECRET"`, 1)},
		{"invalid verification", "retrieve", strings.Replace(retrievalBody, `"warnings":`, `"verification_outcome":"REMOTE-SECRET","warnings":`, 1)},
		{"invalid truncation reason", "retrieve", strings.Replace(retrievalBody, `"wire_budget"`, `"REMOTE-SECRET"`, 1)},
		{"visited negative", "retrieve", strings.Replace(retrievalBody, `"visited":0`, `"visited":-1`, 1)},
		{"visited over limit", "retrieve", strings.Replace(retrievalBody, `"visited":0`, `"visited":51`, 1)},
		{"negative passage", "fetch", strings.Replace(retrievalBody, `"start_byte":3`, `"start_byte":-1`, 1)},
		{"passage reversed", "fetch", strings.Replace(retrievalBody, `"start_byte":3`, `"start_byte":10`, 1)},
		{"passage beyond source", "fetch", strings.Replace(retrievalBody, `"source_bytes":40000`, `"source_bytes":7`, 1)},
		{"nested missing origin", "retrieve", strings.Replace(retrievalBody, `"client_id":"collector",`, ``, 1)},
		{"nested null field", "retrieve", strings.Replace(retrievalBody, `"applicability":{}`, `"applicability":null`, 1)},
		{"nested unknown property", "retrieve", strings.Replace(retrievalBody, `"applicability":{}`, `"applicability":{"REMOTE-SECRET":"x"}`, 1)},
		{"case alias property", "retrieve", strings.Replace(retrievalBody, `"claim_status"`, `"Claim_Status"`, 1)},
		{"duplicate property", "retrieve", strings.Replace(retrievalBody, `"claim_status":"candidate"`, `"claim_status":"REMOTE-SECRET","claim_status":"candidate"`, 1)},
		{"invalid task label", "retrieve", strings.Replace(retrievalBody, `"truncated":`, `"task_checkpoint":{"label":"REMOTE-SECRET","checkpoint":`+checkpoint+`},"truncated":`, 1)},
		{"checkpoint missing scope", "checkpoint", `{"schema_version":"3","checkpoint":` + strings.Replace(checkpoint, `"scope":{"project_id":"demo","task_id":"task"},`, ``, 1) + `}`},
		{"checkpoint bad time", "checkpoint", `{"schema_version":"3","checkpoint":` + strings.Replace(checkpoint, `2026-10-30T00:00:00Z`, `REMOTE-SECRET`, 1) + `}`},
		{"nested checkpoint missing objective", "retrieve", strings.Replace(retrievalBody, `"truncated":`, `"task_checkpoint":{"label":"task_state","checkpoint":`+strings.Replace(checkpoint, `"objective":"o",`, ``, 1)+`},"truncated":`, 1)},
		{"nested applicability bad version", "retrieve", strings.Replace(retrievalBody, `"applicability":{}`, `"applicability":{"versions":{"runtime":null}}`, 1)},
		{"nested applicability bad time", "retrieve", strings.Replace(retrievalBody, `"applicability":{}`, `"applicability":{"valid_until":"REMOTE-SECRET"}`, 1)},
		{"candidate invalid nested enum", "submit", strings.Replace(candidate, `"origin_trust":"submitted"`, `"origin_trust":"REMOTE-SECRET"`, 1)},
		{"candidate claims verification", "submit", strings.Replace(candidate, `"claim_status":"candidate"`, `"claim_status":"verified"`, 1)},
		{"candidate invalid verification", "submit", strings.Replace(candidate, `"claim_status":`, `"verification_outcome":"REMOTE-SECRET","claim_status":`, 1)},
		{"candidate too many entity ids", "submit", strings.Replace(candidate, `"claim_status":`, `"entity_ids":[`+strings.Repeat(`"x",`, 50)+`"x"],"claim_status":`, 1)},
		{"candidate blank dependency", "submit", strings.Replace(candidate, `"depends_on":[]`, `"depends_on":[" "]`, 1)},
		{"status invalid state", "status", strings.Replace(status, `"available"`, `"REMOTE-SECRET"`, 1)},
		{"status null list", "status", strings.Replace(status, `"capability_gaps":[]`, `"capability_gaps":null`, 1)},
		{"status null item", "status", strings.Replace(status, `"authorized_capabilities":["status:read"]`, `"authorized_capabilities":[null]`, 1)},
		{"status blank project", "status", strings.Replace(status, `["demo"]`, `["  "]`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status := http.StatusOK
			if tc.operation == "submit" {
				status = http.StatusCreated
			}
			f := &apiFixture{status: status, body: tc.body}
			api := f.serve(t)
			var err error
			switch tc.operation {
			case "retrieve":
				_, err = api.Retrieve(t.Context(), domain.RetrievalRequest{Mode: domain.RetrievalEvidence, BudgetBytes: 16000})
			case "fetch":
				_, err = api.Fetch(t.Context(), domain.FetchRequest{ArtifactID: "a", BudgetBytes: 16000})
			case "submit":
				_, err = api.Submit(t.Context(), domain.CandidateInput{})
			case "checkpoint":
				_, err = api.PutCheckpoint(t.Context(), "", domain.Checkpoint{ID: "cp"})
			case "status":
				_, err = api.Status(t.Context())
			}
			require.ErrorIs(t, err, malformedErr(tc.operation))
			assert.NotContains(t, err.Error(), "REMOTE-SECRET")
		})
	}
}

func TestMemoryAPINestedValidationIsEnforcedBeforeProjection(t *testing.T) {
	const good = `{"schema_version":"3","chunks":[{"record_id":"c","revision":"r","text":"x","claim_status":"verified","origin_trust":"submitted","evidence":[{"artifact_id":"a","digest":"d","locator":"l","source_revision":"g","origin":{"client_id":"collector","trust":"submitted"},"applicability":{"versions":{"go":"1.27"}}}],"warnings":[]}],"truncated":false,"visited":1,"no_results":false}`
	for _, tc := range []struct{ name, body string }{
		{"nested enum", strings.Replace(good, `"trust":"submitted"`, `"trust":"REMOTE-SECRET"`, 1)},
		{"nested versions", strings.Replace(good, `"go":"1.27"`, `"go":null`, 1)},
		{"nested id", strings.Replace(good, `"source_revision":"g"`, `"source_revision":" "`, 1)},
		{"unknown nested property", strings.Replace(good, `"client_id":"collector"`, `"client_id":"collector","REMOTE-SECRET":"x"`, 1)},
		{"invalid optional verification", strings.Replace(good, `"warnings":[]`, `"warnings":[],"verification_outcome":"REMOTE-SECRET"`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := (&apiFixture{status: 200, body: tc.body}).serve(t)
			result, err := api.Retrieve(t.Context(), domain.RetrievalRequest{Mode: domain.RetrievalEvidence, Scope: domain.Scope{ProjectID: "demo"}, BudgetBytes: 4096, MaxVisited: 50})
			require.ErrorIs(t, err, domain.ErrUnavailable)
			assert.Empty(t, result.Chunks)
			assert.NotContains(t, err.Error(), "REMOTE-SECRET")
		})
	}
}

// The generated model tags and Valid methods drive validation. Compare them to
// the generated, embedded OpenAPI contract to detect schema drift when either
// side changes. A separate test checks numeric and array constraints that
// are not represented by Go types or enum Valid methods.
func TestMemoryAPIValidationMatchesOpenAPI(t *testing.T) {
	spec, err := handler.GetSpec()
	require.NoError(t, err)
	for i, tc := range []struct {
		name string
		body string
		dest any
	}{
		{"RetrievalResult", retrievalBody, &memoryapi.RetrievalResult{}},
		{"RetrievalResult", strings.Replace(retrievalBody, `"visited":0`, `"visited":51`, 1), &memoryapi.RetrievalResult{}},
		{"RetrievalResult", strings.Replace(retrievalBody, `"claim_status":"candidate"`, `"claim_status":"wrong"`, 1), &memoryapi.RetrievalResult{}},
		{"CandidateResult", `{"schema_version":"3","claim":{"claim_id":"c","revision":"r","text":"x","kind":"finding","scope":{"project_id":"demo"},"evidence":[],"depends_on":[],"applicability":{},"claim_status":"candidate","origin_trust":"submitted"}}`, &memoryapi.CandidateResult{}},
		{"CheckpointResult", `{"schema_version":"3","checkpoint":{"id":"cp","revision":"r","scope":{"project_id":"demo"},"objective":"o","completed":[],"pending":[],"questions":[],"evidence_revision_ids":[],"updated_at":"2026-10-30T00:00:00Z"}}`, &memoryapi.CheckpointResult{}},
		{"StatusResult", `{"schema_version":"3","connection_state":"available","authorized_project_ids":["demo"],"authorized_capabilities":["status:read"],"capability_gaps":[]}`, &memoryapi.StatusResult{}},
	} {
		t.Run(fmt.Sprintf("%s/%d", tc.name, i), func(t *testing.T) {
			var decoded any
			require.NoError(t, json.Unmarshal([]byte(tc.body), &decoded))
			schema := spec.Components.Schemas[tc.name]
			require.NotNil(t, schema)
			require.Equal(t, schema.Value.VisitJSON(decoded) == nil, validMemoryResponse([]byte(tc.body), tc.dest))
		})
	}
	seen := make(map[string]bool)
	var compare func(string, reflect.Type)
	compare = func(name string, typ reflect.Type) {
		t.Helper()
		if seen[name] {
			return
		}
		seen[name] = true
		schema := spec.Components.Schemas[name]
		require.NotNil(t, schema, name)
		properties := schema.Value.Properties
		require.Len(t, properties, typ.NumField(), name)
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			jsonName := strings.Split(field.Tag.Get("json"), ",")[0]
			property := properties[jsonName]
			require.NotNil(t, property, "%s.%s", name, jsonName)
			required := slices.Contains(schema.Value.Required, jsonName)
			assert.Equal(t, required, field.Type.Kind() != reflect.Pointer, "%s.%s", name, jsonName)
			ft := field.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			item := property
			if ft.Kind() == reflect.Slice {
				ft = ft.Elem()
				item = property.Value.Items
			}
			if ft.Kind() == reflect.Struct && ft != reflect.TypeOf(time.Time{}) {
				require.NotEmpty(t, item.Ref, "%s.%s", name, jsonName)
				compare(strings.TrimPrefix(item.Ref, "#/components/schemas/"), ft)
			}
			if len(item.Value.Enum) > 0 {
				e := reflect.New(ft).Elem()
				require.Equal(t, reflect.String, e.Kind())
				for _, choice := range item.Value.Enum {
					e.SetString(choice.(string))
					enum, ok := e.Interface().(interface{ Valid() bool })
					require.True(t, ok, "%s.%s", name, jsonName)
					assert.True(t, enum.Valid(), "%s.%s: %s", name, jsonName, choice)
				}
			}
		}
	}
	compare("RetrievalResult", reflect.TypeOf(memoryapi.RetrievalResult{}))
	compare("CandidateResult", reflect.TypeOf(memoryapi.CandidateResult{}))
	compare("CheckpointResult", reflect.TypeOf(memoryapi.CheckpointResult{}))
	compare("StatusResult", reflect.TypeOf(memoryapi.StatusResult{}))
}

func TestMemoryAPIValidationBoundsMatchOpenAPI(t *testing.T) {
	spec, err := handler.GetSpec()
	require.NoError(t, err)
	schemas := spec.Components.Schemas
	maxVisited := schemas["RetrievalResult"].Value.Properties["visited"].Value
	require.Equal(t, float64(0), *maxVisited.Min)
	require.Equal(t, float64(domain.MaxGraphVisited), *maxVisited.Max)
	require.Equal(t, uint64(domain.MaxGraphVisited), *schemas["ClaimRevision"].Value.Properties["entity_ids"].Value.MaxItems)
	for _, field := range []string{"start_byte", "end_byte", "source_bytes"} {
		require.Equal(t, float64(0), *schemas["Passage"].Value.Properties[field].Value.Min)
	}
	require.Equal(t, uint64(1), schemas["OpaqueID"].Value.MinLength)
	require.Equal(t, `\S`, schemas["OpaqueID"].Value.Pattern)
	require.Equal(t, uint64(1), schemas["EvidenceRef"].Value.Properties["digest"].Value.MinLength)
}

func TestMemoryAPIResponseSemanticsAndWireLimits(t *testing.T) {
	const search = `{"schema_version":"3","chunks":[{"record_id":"c","revision":"r","text":"x","claim_status":"verified","origin_trust":"submitted","evidence":[],"warnings":[]}],"truncated":false,"visited":1,"no_results":false}`
	const candidate = `{"schema_version":"3","claim":{"claim_id":"c","revision":"r","text":"x","kind":"finding","scope":{"project_id":"demo","task_id":"task"},"evidence":[],"depends_on":[],"applicability":{},"claim_status":"candidate","origin_trust":"submitted"}}`
	const checkpoint = `{"schema_version":"3","checkpoint":{"id":"cp","revision":"r","scope":{"project_id":"demo","task_id":"task"},"objective":"o","completed":[],"pending":[],"questions":[],"evidence_revision_ids":[],"updated_at":"2026-10-30T00:00:00Z"}}`
	for _, tc := range []struct {
		name, operation, body string
		status, budget        int
	}{
		{"verified result accepted", "retrieve", search, 200, 4096},
		{"candidate rejected outside working scope", "retrieve", strings.Replace(search, `"claim_status":"verified"`, `"claim_status":"candidate"`, 1), 200, 4096},
		{"candidate accepted in working scope", "working", strings.Replace(strings.Replace(search, `"claim_status":"verified"`, `"claim_status":"candidate"`, 1), `"warnings":[]`, `"warnings":["unverified_task_candidate"]`, 1), 200, 4096},
		{"candidate missing label", "working", strings.Replace(search, `"claim_status":"verified"`, `"claim_status":"candidate"`, 1), 200, 4096},
		{"candidate verification rejected", "working", strings.Replace(strings.Replace(search, `"claim_status":"verified"`, `"claim_status":"candidate"`, 1), `"warnings":`, `"verification_outcome":"supported","warnings":`, 1), 200, 4096},
		{"invalidated claim rejected", "retrieve", strings.Replace(search, `"claim_status":"verified"`, `"claim_status":"invalidated"`, 1), 200, 4096},
		{"no results contradict chunks", "retrieve", strings.Replace(search, `"no_results":false`, `"no_results":true`, 1), 200, 4096},
		{"continuation without truncation", "retrieve", strings.Replace(search, `"no_results":false`, `"continuation":"c.d","no_results":false`, 1), 200, 4096},
		{"visited over request", "retrieve", strings.Replace(search, `"visited":1`, `"visited":2`, 1), 200, 4096},
		{"wrong success status", "retrieve", search, 201, 4096},
		{"wire budget", "retrieve", search, 200, 100},
		{"correct candidate receipt", "submit", candidate, 201, 0},
		{"candidate wrong id", "submit", strings.Replace(candidate, `"claim_id":"c"`, `"claim_id":"other"`, 1), 201, 0},
		{"candidate wrong task", "submit", strings.Replace(candidate, `"task_id":"task"`, `"task_id":"other"`, 1), 201, 0},
		{"candidate verification outcome", "submit", strings.Replace(candidate, `"claim_status":"candidate"`, `"verification_outcome":"supported","claim_status":"candidate"`, 1), 201, 0},
		{"correct checkpoint", "checkpoint", checkpoint, 200, 0},
		{"checkpoint wrong id", "checkpoint", strings.Replace(checkpoint, `"id":"cp"`, `"id":"other"`, 1), 200, 0},
		{"checkpoint wrong scope", "checkpoint", strings.Replace(checkpoint, `"task_id":"task"`, `"task_id":"other"`, 1), 200, 0},
		{"invalid utf8", "retrieve", strings.Replace(search, `"text":"x"`, "\"text\":\"\xff\"", 1), 200, 4096},
		{"escaped duplicate", "retrieve", strings.Replace(search, `"claim_status":"verified"`, `"claim_status":"verified","claim_\u0073tatus":"verified"`, 1), 200, 4096},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &apiFixture{status: tc.status, body: tc.body}
			api := f.serve(t)
			var err error
			switch tc.operation {
			case "retrieve", "working":
				mode := domain.RetrievalEvidence
				if tc.operation == "working" {
					mode = domain.RetrievalWorking
				}
				_, err = api.Retrieve(t.Context(), domain.RetrievalRequest{Mode: mode, Scope: domain.Scope{ProjectID: "demo", TaskID: "task"}, BudgetBytes: tc.budget, MaxVisited: 1})
			case "submit":
				_, err = api.Submit(t.Context(), domain.CandidateInput{ClaimID: "c", Scope: domain.Scope{ProjectID: "demo", TaskID: "task"}})
			case "checkpoint":
				_, err = api.PutCheckpoint(t.Context(), "", domain.Checkpoint{ID: "cp", Scope: domain.Scope{ProjectID: "demo", TaskID: "task"}})
			}
			if strings.Contains(tc.name, "accepted") || strings.HasPrefix(tc.name, "correct") {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, malformedErr(tc.operation))
			}
		})
	}
}

func TestMemoryAPIFetchPassageValidation(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		request    domain.FetchRequest
		valid      bool
	}{
		{"valid", retrievalBody, domain.FetchRequest{ArtifactID: "a", Locator: "l", BudgetBytes: 4096, StartByte: 3}, true},
		{"request artifact mismatch", retrievalBody, domain.FetchRequest{ArtifactID: "other", Locator: "l", BudgetBytes: 4096}, false},
		{"request locator mismatch", retrievalBody, domain.FetchRequest{ArtifactID: "a", Locator: "other", BudgetBytes: 4096}, false},
		{"request start bound", retrievalBody, domain.FetchRequest{ArtifactID: "a", Locator: "l", BudgetBytes: 4096, StartByte: 4}, false},
		{"request end bound", retrievalBody, domain.FetchRequest{ArtifactID: "a", Locator: "l", BudgetBytes: 4096, EndByte: func() *int64 { v := int64(7); return &v }()}, false},
		{"incorrect byte length", strings.Replace(retrievalBody, `"end_byte":8`, `"end_byte":9`, 1), domain.FetchRequest{ArtifactID: "a", Locator: "l", BudgetBytes: 4096}, false},
		{"missing partial label", strings.Replace(retrievalBody, `,"partial_source_passage"`, ``, 1), domain.FetchRequest{ArtifactID: "a", Locator: "l", BudgetBytes: 4096}, false},
		{"missing source label", strings.Replace(retrievalBody, `"source_evidence_is_not_a_verified_claim",`, ``, 1), domain.FetchRequest{ArtifactID: "a", Locator: "l", BudgetBytes: 4096}, false},
		{"missing working label", strings.Replace(retrievalBody, `"unverified_task_evidence",`, ``, 1), domain.FetchRequest{ArtifactID: "a", Locator: "l", Mode: domain.FetchWorking, BudgetBytes: 4096}, false},
		{"incorrect digest", strings.Replace(retrievalBody, `"digest":"d"`, `"digest":"bad"`, 1), domain.FetchRequest{ArtifactID: "a", Locator: "l", BudgetBytes: 4096}, false},
		{"verified source", strings.Replace(retrievalBody, `"claim_status":"candidate"`, `"claim_status":"verified"`, 1), domain.FetchRequest{ArtifactID: "a", Locator: "l", BudgetBytes: 4096}, false},
		{"missing passage", strings.Replace(retrievalBody, `,"passage":{"start_byte":3,"end_byte":8,"source_bytes":40000}`, ``, 1), domain.FetchRequest{ArtifactID: "a", Locator: "l", BudgetBytes: 4096}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := (&apiFixture{status: http.StatusOK, body: tc.body}).serve(t)
			_, err := api.Fetch(t.Context(), tc.request)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, domain.ErrUnavailable)
			}
		})
	}
}

// Exercise the actual newline-delimited JSON-RPC adapter through the remote
// service and HTTP gateway. The malicious server's values never enter the MCP
// frame, including errors and partially decoded results.
func TestMemoryAPIMalformedResponsesNeverReachMCP(t *testing.T) {
	search := strings.Replace(retrievalBody, `,"passage":{"start_byte":3,"end_byte":8,"source_bytes":40000}`, ``, 1)
	search = strings.Replace(search, `"warnings":[`, `"warnings":["unverified_task_candidate",`, 1)
	for _, tc := range []struct {
		name, body, request string
		status              int
	}{
		{"search nested enum", strings.Replace(search, `"trust":"submitted"`, `"trust":"REMOTE-SECRET"`, 1), `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_search","arguments":{"project_id":"demo","task_id":"task","mode":"working"}}}`, 200},
		{"fetch invalid bounds", strings.Replace(retrievalBody, `"end_byte":8`, `"end_byte":9`, 1), `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_fetch","arguments":{"artifact_id":"a","locator":"l"}}}`, 200},
		{"status invalid state", `{"schema_version":"3","connection_state":"REMOTE-SECRET","authorized_project_ids":["demo"],"authorized_capabilities":[],"capability_gaps":[]}`, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_status","arguments":{}}}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &apiFixture{status: tc.status, body: tc.body}
			remote := service.NewRemoteMemoryService(f.serve(t))
			var output strings.Builder
			err := handler.NewMCPServer(remote, remote, remote, remote).ServeStdio(t.Context(), strings.NewReader(tc.request+"\n"), &output)
			require.NoError(t, err)
			require.Len(t, f.requests, 1)
			assert.NotContains(t, output.String(), "REMOTE-SECRET")
			var frame struct {
				Result struct {
					IsError bool `json:"isError"`
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"result"`
			}
			require.NoError(t, json.Unmarshal([]byte(output.String()), &frame))
			require.True(t, frame.Result.IsError)
			require.Len(t, frame.Result.Content, 1)
			assert.Contains(t, frame.Result.Content[0].Text, `"unavailable"`)
		})
	}
}

// MCP rejects writes before contacting the remote server when the service cannot
// guarantee a bounded revision receipt, even when the server would succeed.
// The fixture answers status without max_revision_bytes (an older server), so
// MCP probes status and then attempts no write.
func TestMemoryAPIMCPWritesUnavailableWithoutMutation(t *testing.T) {
	for _, tc := range []struct {
		name, request, body string
		status              int
	}{
		{"candidate", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_submit_candidate","arguments":{"project_id":"demo","task_id":"task","claim_id":"c","text":"x","kind":"finding"}}}`, `{"schema_version":"3","claim":{"claim_id":"c","revision":"r","text":"x","kind":"finding","scope":{"project_id":"demo","task_id":"task"},"evidence":[],"depends_on":[],"applicability":{},"claim_status":"candidate","origin_trust":"submitted"}}`, http.StatusCreated},
		{"checkpoint", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_checkpoint","arguments":{"project_id":"demo","task_id":"task","checkpoint_id":"cp","objective":"o"}}}`, `{"schema_version":"3","checkpoint":{"id":"cp","revision":"r","scope":{"project_id":"demo","task_id":"task"},"objective":"o","completed":[],"pending":[],"questions":[],"evidence_revision_ids":[],"updated_at":"2026-10-30T00:00:00Z"}}`, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &apiFixture{status: tc.status, body: tc.body}
			remote := service.NewRemoteMemoryService(f.serve(t))
			var output strings.Builder
			err := handler.NewMCPServer(remote, remote, remote, remote).ServeStdio(t.Context(), strings.NewReader(tc.request+"\n"), &output)
			require.NoError(t, err)
			for _, r := range f.requests {
				require.Equal(t, "GET", r.method, "MCP must not attempt a remote write")
				require.Equal(t, "/v1/status", r.path)
			}
			var frame struct {
				ID     int `json:"id"`
				Result struct {
					IsError bool `json:"isError"`
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"result"`
			}
			require.NoError(t, json.Unmarshal([]byte(output.String()), &frame))
			assert.Equal(t, 1, frame.ID)
			require.True(t, frame.Result.IsError)
			require.Len(t, frame.Result.Content, 1)
			var payload struct {
				SchemaVersion string `json:"schema_version"`
				Error         struct {
					Code   string `json:"code"`
					Action string `json:"action"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal([]byte(frame.Result.Content[0].Text), &payload))
			assert.Equal(t, "3", payload.SchemaVersion)
			assert.Equal(t, "unavailable", payload.Error.Code)
			assert.Contains(t, payload.Error.Action, "No write was attempted")
		})
	}
}

// These checks bypass MCP preflight deliberately: malformed successful mutation
// responses must still be rejected by the HTTP gateway itself.
func TestMemoryAPIDirectMutationResponseValidation(t *testing.T) {
	const candidate = `{"schema_version":"3","claim":{"claim_id":"c","revision":"r","text":"x","kind":"finding","scope":{"project_id":"demo","task_id":"task"},"evidence":[],"depends_on":[],"applicability":{},"claim_status":"candidate","origin_trust":"submitted"}}`
	const checkpoint = `{"schema_version":"3","checkpoint":{"id":"cp","revision":"r","scope":{"project_id":"demo","task_id":"task"},"objective":"o","completed":[],"pending":[],"questions":[],"evidence_revision_ids":[],"updated_at":"2026-10-30T00:00:00Z"}}`
	for _, tc := range []struct {
		name, operation, body, method, path string
		status                              int
		valid                               bool
	}{
		{"valid candidate", "submit", candidate, "POST", "/v1/candidates", http.StatusCreated, true},
		{"candidate invalid status", "submit", strings.Replace(candidate, `"claim_status":"candidate"`, `"claim_status":"REMOTE-SECRET"`, 1), "POST", "/v1/candidates", http.StatusCreated, false},
		{"valid checkpoint", "checkpoint", checkpoint, "PUT", "/v1/checkpoints/cp", http.StatusOK, true},
		{"checkpoint invalid scope", "checkpoint", strings.Replace(checkpoint, `"project_id":"demo"`, `"project_id":"REMOTE-SECRET"`, 1), "PUT", "/v1/checkpoints/cp", http.StatusOK, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &apiFixture{status: tc.status, body: tc.body}
			api := f.serve(t)
			scope := domain.Scope{ProjectID: "demo", TaskID: "task"}
			var err error
			if tc.operation == "submit" {
				var result domain.ClaimRevision
				result, err = api.Submit(t.Context(), domain.CandidateInput{ClaimID: "c", Text: "x", Kind: "finding", Scope: scope})
				if tc.valid {
					assert.Equal(t, "r", result.Revision)
				} else {
					assert.Equal(t, domain.ClaimRevision{}, result)
				}
			} else {
				var result domain.Checkpoint
				result, err = api.PutCheckpoint(t.Context(), "", domain.Checkpoint{ID: "cp", Scope: scope, Objective: "o"})
				if tc.valid {
					assert.Equal(t, "r", result.Revision)
				} else {
					assert.Equal(t, domain.Checkpoint{}, result)
				}
			}
			require.Len(t, f.requests, 1, "direct gateway call must reach the server")
			assert.Equal(t, tc.method, f.requests[0].method)
			assert.Equal(t, tc.path, f.requests[0].path)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, domain.ErrUnknownOutcome)
				assert.NotContains(t, err.Error(), "REMOTE-SECRET")
			}
		})
	}
}

type memoryAPITestIdentity struct{}

func (memoryAPITestIdentity) Authenticate(context.Context, string) (domain.Principal, error) {
	return domain.Principal{ClientID: "test-client", Role: domain.RoleAgent, ProjectIDs: []string{"demo"}, Capabilities: []string{"status:read", "retrieval:read"}}, nil
}
func (memoryAPITestIdentity) Enroll(context.Context, domain.Principal, service.EnrollInput) (service.ClientCredential, error) {
	return service.ClientCredential{}, domain.ErrUnavailable
}
func (memoryAPITestIdentity) Revoke(context.Context, domain.Principal, string) error {
	return domain.ErrUnavailable
}

type memoryAPITestRetrieval struct{ result domain.RetrievalResult }

func (r memoryAPITestRetrieval) Retrieve(context.Context, domain.Principal, domain.RetrievalRequest) (domain.RetrievalResult, error) {
	return r.result, nil
}
func (r memoryAPITestRetrieval) FetchEvidence(context.Context, domain.Principal, domain.FetchRequest) (domain.RetrievalResult, error) {
	return r.result, nil
}

func TestMemoryAPIAcceptsRealHTTPSerialization(t *testing.T) {
	now := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	targetConditions := &domain.Applicability{Environment: "ci", Versions: map[string]string{"go": "1.27"}, ValidFrom: &now}
	for _, tc := range []struct {
		name    string
		result  domain.RetrievalResult
		request domain.RetrievalRequest
	}{
		{"empty", domain.RetrievalResult{SchemaVersion: "3", Chunks: []domain.EvidenceChunk{}, NoResults: true}, domain.RetrievalRequest{Mode: domain.RetrievalEvidence, Scope: domain.Scope{ProjectID: "demo"}, BudgetBytes: 4096, MaxDepth: 2, MaxVisited: 50}},
		{"owned task head across session", domain.RetrievalResult{SchemaVersion: "3", Chunks: []domain.EvidenceChunk{}, TaskCheckpoint: &domain.TaskState{Label: "task_state", Checkpoint: domain.Checkpoint{ID: "cp", Revision: "r", Scope: domain.Scope{ProjectID: "demo", TaskID: "task", SessionID: "earlier"}, UpdatedAt: now, EvidenceReferencesOmitted: true}}}, domain.RetrievalRequest{Mode: domain.RetrievalWorking, Scope: domain.Scope{ProjectID: "demo", TaskID: "task", SessionID: "current"}, BudgetBytes: 4096, MaxDepth: 2, MaxVisited: 50}},
		{"verified published result", domain.RetrievalResult{SchemaVersion: "3", Chunks: []domain.EvidenceChunk{{RecordID: "claim", Revision: "r", Text: "verified", ClaimStatus: domain.ClaimVerified, OriginTrust: domain.TrustGatewayObserved, Applicability: targetConditions, Evidence: []domain.EvidenceRef{{ArtifactID: "artifact", Digest: "digest", Locator: "file", SourceRevision: "generation", Origin: domain.Origin{ClientID: "collector", Trust: domain.TrustCollectorAttested}}}}}, Visited: 1}, domain.RetrievalRequest{Mode: domain.RetrievalEvidence, Scope: domain.Scope{ProjectID: "demo"}, BudgetBytes: 4096, MaxDepth: 2, MaxVisited: 50}},
		{"working candidate", domain.RetrievalResult{SchemaVersion: "3", Chunks: []domain.EvidenceChunk{{RecordID: "claim", Revision: "r", Text: "candidate", ClaimStatus: domain.ClaimCandidate, OriginTrust: domain.TrustSubmitted, Warnings: []string{"unverified_task_candidate"}}}, Visited: 1}, domain.RetrievalRequest{Mode: domain.RetrievalWorking, Scope: domain.Scope{ProjectID: "demo", TaskID: "task"}, BudgetBytes: 4096, MaxDepth: 2, MaxVisited: 50}},
		{"task state omitted", domain.RetrievalResult{SchemaVersion: "3", Chunks: []domain.EvidenceChunk{}, TaskStateOmitted: true, Truncated: true, TruncationReason: "task_state_budget", Action: "Increase budget."}, domain.RetrievalRequest{Mode: domain.RetrievalWorking, Scope: domain.Scope{ProjectID: "demo", TaskID: "task"}, BudgetBytes: 4096, MaxDepth: 2, MaxVisited: 50}},
		{"visited frontier", domain.RetrievalResult{SchemaVersion: "3", Chunks: []domain.EvidenceChunk{}, Truncated: true, TruncationReason: "visited_limit", Visited: 50, NoResults: true, Action: "Narrow the query."}, domain.RetrievalRequest{Mode: domain.RetrievalEvidence, Scope: domain.Scope{ProjectID: "demo"}, BudgetBytes: 4096, MaxDepth: 2, MaxVisited: 50}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			echo, err := handler.NewRetrievalServer(memoryAPITestIdentity{}, nil, nil, nil, memoryAPITestRetrieval{tc.result}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			require.NoError(t, err)
			server := httptest.NewServer(echo)
			t.Cleanup(server.Close)
			api, err := NewMemoryAPI(server.URL, syntheticBearer, time.Second)
			require.NoError(t, err)
			result, err := api.Retrieve(t.Context(), tc.request)
			require.NoError(t, err)
			assert.Equal(t, tc.result.NoResults, result.NoResults)
			assert.Equal(t, tc.result.TruncationReason, result.TruncationReason)
			if tc.result.TaskCheckpoint != nil {
				require.NotNil(t, result.TaskCheckpoint)
				assert.Equal(t, "earlier", result.TaskCheckpoint.Checkpoint.Scope.SessionID)
			}
		})
	}
}

func TestMemoryAPINestedShapesRejectUnpairedSurrogates(t *testing.T) {
	const good = `{"schema_version":"3","chunks":[{"record_id":"c","revision":"r","text":"x","claim_status":"verified","origin_trust":"submitted","evidence":[],"warnings":[]}],"truncated":false,"visited":1,"no_results":false}`
	for _, tc := range []struct {
		name string
		text string
		want bool
	}{
		{"pair", `\ud83d\ude00`, true},
		{"high alone", `\ud800`, false},
		{"low alone", `\udc00`, false},
		{"high followed by text", `\ud800x`, false},
		{"high followed by high", `\ud800\ud800`, false},
		{"escaped slash", `\\ud800`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.Replace(good, `"text":"x"`, `"text":"`+tc.text+`"`, 1)
			api := (&apiFixture{status: 200, body: body}).serve(t)
			_, err := api.Retrieve(t.Context(), domain.RetrievalRequest{Mode: domain.RetrievalEvidence, Scope: domain.Scope{ProjectID: "demo"}, BudgetBytes: 4096, MaxVisited: 50})
			if tc.want {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, domain.ErrUnavailable)
			}
		})
	}
}
