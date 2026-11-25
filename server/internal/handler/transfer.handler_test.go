package handler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type transferHandlerService struct {
	result       service.ExportManifest
	err          error
	scope        domain.Scope
	continuation string
	principal    domain.Principal
}

func (f *transferHandlerService) Export(_ context.Context, p domain.Principal, scope domain.Scope, continuation string) (service.ExportManifest, error) {
	f.principal, f.scope, f.continuation = p, scope, continuation
	return f.result, f.err
}

func TestExportHTTPReportsStoredStatusAndProvenance(t *testing.T) {
	verified := domain.ClaimRevision{ClaimID: "c1", Revision: "r1", Text: "fact", Kind: "fact", Scope: domain.Scope{ProjectID: "demo"}, Status: domain.ClaimVerified, Evidence: []domain.EvidenceRef{{ArtifactID: "a1", Digest: strings.Repeat("a", 64), Locator: "loc", SourceRevision: "g1", Origin: domain.Origin{ClientID: "collector", Trust: domain.TrustCollectorAttested, AttestationID: "att"}}}}
	candidate := domain.ClaimRevision{ClaimID: "c2", Revision: "r2", Text: "maybe", Kind: "finding", Scope: domain.Scope{ProjectID: "demo", TaskID: "t"}, Status: domain.ClaimCandidate}
	for _, tc := range []struct {
		name   string
		body   string
		result service.ExportManifest
		err    error
		status int
		code   string
	}{
		{"page", `{"scope":{"project_id":"demo"}}`, service.ExportManifest{Scope: domain.Scope{ProjectID: "demo"}, Claims: []service.ExportedClaim{{Claim: verified, OriginTrust: domain.TrustCollectorAttested}, {Claim: candidate, OriginTrust: domain.TrustSubmitted}}, Omitted: 1, Completeness: domain.CompletenessPartial, Continuation: "next.sig"}, nil, 200, ""},
		{"resume", `{"scope":{"project_id":"demo"},"continuation":"cursor.sig"}`, service.ExportManifest{Scope: domain.Scope{ProjectID: "demo"}, Completeness: domain.CompletenessComplete}, nil, 200, ""},
		{"stale cursor", `{"scope":{"project_id":"demo"},"continuation":"cursor.sig"}`, service.ExportManifest{}, domain.ErrStale, 409, "stale"},
		{"forbidden", `{"scope":{"project_id":"demo"}}`, service.ExportManifest{}, domain.ErrForbidden, 403, "forbidden"},
		{"submitted authority rejected", `{"scope":{"project_id":"demo"},"claim_status":"verified"}`, service.ExportManifest{}, nil, 400, "invalid_input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &transferHandlerService{result: tc.result, err: tc.err}
			server, err := NewServer(fixtureIdentity{}, nil, nil, nil, nil, nil, nil, svc, slog.New(slog.NewJSONHandler(io.Discard, nil)))
			require.NoError(t, err)
			req := httptest.NewRequest("POST", "/v1/exports", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer fixture-secret")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			server.ServeHTTP(rec, req)

			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			if tc.status != 200 {
				assert.Equal(t, tc.code, body["code"])
				return
			}
			assert.Equal(t, "client", svc.principal.ClientID)
			assert.Equal(t, domain.Scope{ProjectID: "demo"}, svc.scope)
			if tc.name == "resume" {
				assert.Equal(t, "cursor.sig", svc.continuation)
				assert.NotContains(t, body, "continuation")
				assert.Equal(t, "complete", body["completeness"])
				assert.Equal(t, []any{}, body["claims"])
				return
			}
			assert.JSONEq(t, `{"schema_version":"3","scope":{"project_id":"demo"},"omitted":1,"completeness":"partial","continuation":"next.sig","claims":[
				{"claim_id":"c1","revision":"r1","text":"fact","kind":"fact","scope":{"project_id":"demo"},"claim_status":"verified","origin_trust":"collector_attested","depends_on":[],"applicability":{},
				 "evidence":[{"artifact_id":"a1","digest":"`+strings.Repeat("a", 64)+`","locator":"loc","source_revision":"g1","origin":{"client_id":"collector","trust":"collector_attested","attestation_id":"att"},"applicability":{}}]},
				{"claim_id":"c2","revision":"r2","text":"maybe","kind":"finding","scope":{"project_id":"demo","task_id":"t"},"claim_status":"candidate","origin_trust":"submitted","depends_on":[],"applicability":{},"evidence":[]}]}`, rec.Body.String())
		})
	}
}

func TestExportRouteUnavailableWithoutTransferService(t *testing.T) {
	server, err := NewServer(fixtureIdentity{}, nil, nil, nil, nil, nil, nil, nil, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	require.NoError(t, err)
	req := httptest.NewRequest("POST", "/v1/exports", strings.NewReader(`{"scope":{"project_id":"demo"}}`))
	req.Header.Set("Authorization", "Bearer fixture-secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	assert.Equal(t, 503, rec.Code, rec.Body.String())
}
