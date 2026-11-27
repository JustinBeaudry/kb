package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fixtureIdentity struct{}

func (fixtureIdentity) Authenticate(_ context.Context, bearer string) (domain.Principal, error) {
	if bearer != "fixture-secret" {
		return domain.Principal{}, domain.ErrUnauthorized
	}
	return domain.Principal{ClientID: "client", Role: domain.RoleAgent, ProjectIDs: []string{"demo"}, Capabilities: []string{"status:read"}}, nil
}
func (fixtureIdentity) Enroll(context.Context, domain.Principal, service.EnrollInput) (service.ClientCredential, error) {
	return service.ClientCredential{}, domain.ErrForbidden
}
func (fixtureIdentity) Revoke(context.Context, domain.Principal, string) error {
	return domain.ErrForbidden
}
func TestRequestValidatorRejectsForgedOrigin(t *testing.T) {
	e, err := NewIdentityServer(fixtureIdentity{}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	require.NoError(t, err)
	for _, tc := range []struct {
		name, body string
		want       int
	}{{"origin", `{"id":"client","role":"agent","project_ids":["demo"],"capabilities":["status:read"],"origin":{"trust":"owner_authenticated"}}`, 400}, {"enum", `{"id":"client","role":"root","project_ids":["demo"],"capabilities":["status:read"]}`, 400}, {"oversized", strings.Repeat("x", 65537), 413}} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/clients", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer fixture-secret")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			responseBody := rec.Body.String()
			assert.Equal(t, tc.want, rec.Code)
			fixtureSecret := "fixture-secret"
			assert.NotContains(t, responseBody, fixtureSecret)
			assert.NotContains(t, responseBody, "owner_authenticated")
			assert.NotContains(t, responseBody, "private-project")
		})
	}
}

func TestClientHTTPAuthenticationAndStatus(t *testing.T) {
	var logs strings.Builder
	e, err := NewIdentityServer(fixtureIdentity{}, slog.New(slog.NewJSONHandler(&logs, nil)))
	require.NoError(t, err)
	for _, tc := range []struct {
		name, method, path, token, body string
		want                            int
	}{{"missing credential", "GET", "/v1/status", "", "", 401}, {"wrong credential", "GET", "/v1/status", "fixture-secret-wrong", "", 401}, {"status", "GET", "/v1/status", "fixture-secret", "", 200}, {"valid enrollment forbidden", "POST", "/v1/clients", "fixture-secret", `{"id":"other","role":"agent","project_ids":["demo"],"capabilities":["status:read"]}`, 403}, {"unimplemented", "GET", "/v1/clients", "fixture-secret", "", 503}, {"unknown", "GET", "/unimplemented", "fixture-secret", "", 503}} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			assert.Equal(t, tc.want, rec.Code)
			assert.NotEmpty(t, rec.Header().Get("X-Request-Id"))
			assert.NotContains(t, rec.Body.String(), "fixture-secret")
		})
	}
	assert.NotContains(t, logs.String(), "fixture-secret")
	assert.NotContains(t, logs.String(), "other")
	assert.Contains(t, logs.String(), "request_id")
}

// signedOwnerFixture stands in for the future trusted owner-auth boundary; the
// production authenticator deliberately never mints or accepts owner bearers.
type signedOwnerFixture struct{ fail bool }

func (signedOwnerFixture) Authenticate(context.Context, string) (domain.Principal, error) {
	return domain.Principal{ClientID: "owner-boundary", OwnerID: "owner-public", Role: domain.RoleOwner, Capabilities: []string{"clients:manage"}}, nil
}
func (f signedOwnerFixture) Enroll(_ context.Context, _ domain.Principal, input service.EnrollInput) (service.ClientCredential, error) {
	if f.fail {
		return service.ClientCredential{}, domain.ErrForbidden
	}
	p := domain.Principal{ClientID: input.ClientID, Role: input.Role, ProjectIDs: input.ProjectIDs, Capabilities: input.Capabilities}
	return service.ClientCredential{ClientID: p.ClientID, Bearer: "one-time-synthetic-fixture", Principal: p}, nil
}
func (f signedOwnerFixture) Revoke(context.Context, domain.Principal, string) error {
	if f.fail {
		return domain.ErrForbidden
	}
	return nil
}
func TestTrustedOwnerBoundaryMapsStrictIdentityDTOs(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("failure-%t", fail), func(t *testing.T) {
			e, err := NewIdentityServer(signedOwnerFixture{fail: fail}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
			require.NoError(t, err)
			for _, tc := range []struct {
				path, body string
				success    int
			}{{"/v1/clients", `{"id":"client","role":"agent","project_ids":["demo"],"capabilities":["status:read"]}`, 201}, {"/v1/clients/client:revoke", "", 200}} {
				req := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
				req.Header.Set("Authorization", "Bearer authenticated-boundary-fixture")
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, req)
				if fail {
					assert.Equal(t, 403, rec.Code)
					assert.NotContains(t, rec.Body.String(), "one-time-synthetic-fixture")
				} else {
					assert.Equal(t, tc.success, rec.Code)
				}
			}
		})
	}
}

func TestRequestValidatorNamesFailingFieldWithoutValues(t *testing.T) {
	e, err := NewIdentityServer(fixtureIdentity{}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	require.NoError(t, err)
	for _, tc := range []struct {
		name, body, field string
	}{
		{"missing required", `{"id":"client","project_ids":["demo"],"capabilities":["status:read"]}`, "/role"},
		{"bad enum", `{"id":"client","role":"root-SECRET-VALUE","project_ids":["demo"],"capabilities":["status:read"]}`, "/role"},
		{"unknown property has no schema pointer", `{"id":"client","role":"agent","project_ids":["demo"],"capabilities":["status:read"],"origin":{"trust":"owner_authenticated"}}`, ""},
		{"unsafe key is not echoed", `{"id":"client","role":"agent","project_ids":["demo"],"capabilities":["status:read"],"x<script>":1}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/clients", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer fixture-secret")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			var problem Problem
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem))
			assert.Equal(t, ProblemCode("invalid_input"), problem.Code)
			if tc.field == "" {
				assert.Nil(t, problem.Field)
			} else {
				require.NotNil(t, problem.Field)
				assert.Equal(t, tc.field, *problem.Field)
			}
			assert.NotContains(t, rec.Body.String(), "SECRET-VALUE")
			assert.NotContains(t, rec.Body.String(), "<script>")
			assert.NotContains(t, rec.Body.String(), "owner_authenticated")
		})
	}
}

type enrollingIdentity struct {
	fixtureIdentity
	got *service.EnrollInput
}

func (enrollingIdentity) Authenticate(_ context.Context, bearer string) (domain.Principal, error) {
	if bearer != "owner-secret" {
		return domain.Principal{}, domain.ErrUnauthorized
	}
	return domain.Principal{ClientID: "owner", OwnerID: "owner", Role: domain.RoleOwner, Capabilities: []string{"clients:manage"}}, nil
}
func (e enrollingIdentity) Enroll(_ context.Context, _ domain.Principal, input service.EnrollInput) (service.ClientCredential, error) {
	*e.got = input
	p := domain.Principal{ClientID: input.ClientID, Role: input.Role, ProjectIDs: input.ProjectIDs, Capabilities: input.Capabilities, AuthEpoch: 1}
	credential := service.ClientCredential{ClientID: input.ClientID, Bearer: "issued-bearer", Principal: p}
	if input.TTL > 0 {
		at := time.Date(2026, 11, 27, 13, 0, 0, 0, time.UTC)
		credential.ExpiresAt = &at
	}
	return credential, nil
}

func TestEnrollmentTTLOverHTTP(t *testing.T) {
	var got service.EnrollInput
	e, err := NewIdentityServer(enrollingIdentity{got: &got}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	require.NoError(t, err)
	enroll := func(extra string) *httptest.ResponseRecorder {
		body := `{"id":"agent-1","role":"agent","project_ids":["demo"],"capabilities":["status:read"]` + extra + `}`
		req := httptest.NewRequest(http.MethodPost, "/v1/clients", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer owner-secret")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}

	for _, ttl := range []string{"3599", "31622401", "0", "-1", `"3600"`} {
		got = service.EnrollInput{}
		rec := enroll(`,"ttl_seconds":` + ttl)
		assert.Equal(t, http.StatusBadRequest, rec.Code, ttl)
		assert.Empty(t, got.ClientID, "a rejected TTL never reaches enrollment: %s", ttl)
	}

	rec := enroll(`,"ttl_seconds":3600`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, time.Hour, got.TTL)
	var result struct {
		Client struct {
			ExpiresAt *time.Time `json:"expires_at"`
		} `json:"client"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	require.NotNil(t, result.Client.ExpiresAt)
	assert.Equal(t, time.Date(2026, 11, 27, 13, 0, 0, 0, time.UTC), result.Client.ExpiresAt.UTC())

	rec = enroll(`,"ttl_seconds":31622400`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, service.MaxCredentialTTL, got.TTL)

	rec = enroll("")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Zero(t, got.TTL)
	assert.NotContains(t, rec.Body.String(), "expires_at")
}
