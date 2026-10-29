package handler

import (
	"context"
	"log/slog"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	validator "github.com/oapi-codegen/echo-middleware"
)

type IdentityService interface {
	Authenticator
	Enroll(context.Context, domain.Principal, service.EnrollInput) (service.ClientCredential, error)
	Revoke(context.Context, domain.Principal, string) error
}
type IdentityHandler struct {
	StrictServerInterface
	identity IdentityService
}

var _ StrictServerInterface = (*IdentityHandler)(nil)

func NewIdentityServer(identity IdentityService, logger *slog.Logger) (*echo.Echo, error) {
	spec, err := GetSpec()
	if err != nil {
		return nil, domain.ErrUnavailable
	}
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.HTTPErrorHandler = func(err error, c echo.Context) {
		if c.Response().Committed {
			return
		}
		if he, ok := err.(*echo.HTTPError); ok && he.Code == 413 {
			_ = writeProblem(c, 413, "invalid_input", "Request too large.", "Limit request bodies to 65536 bytes.")
			return
		}
		_ = problem(c, err)
	}
	e.Use(requestMetadata(logger), middleware.RecoverWithConfig(middleware.RecoverConfig{DisablePrintStack: true}), middleware.BodyLimit("64K"), middleware.ContextTimeout(10*time.Second), Authentication(identity))
	// Authentication runs before validation. kin-openapi must not re-interpret the
	// already authenticated principal or treat submitted fields as authority.
	e.Use(validator.OapiRequestValidatorWithOptions(spec, &validator.Options{Options: openapi3filter.Options{AuthenticationFunc: func(context.Context, *openapi3filter.AuthenticationInput) error { return nil }}, ErrorHandler: func(c echo.Context, he *echo.HTTPError) error {
		if he.Code == 404 {
			return problem(c, domain.ErrUnavailable)
		}
		return problem(c, domain.ErrInvalidInput)
	}}))
	strict := NewStrictHandler(&IdentityHandler{identity: identity}, nil)
	wrapper := ServerInterfaceWrapper{Handler: strict}
	e.POST("/v1/clients", wrapper.EnrollClient, Capability("clients:manage", nil))
	e.POST("/v1/clients/:id\\:revoke", wrapper.RevokeClient, Capability("clients:manage", nil))
	e.GET("/v1/status", wrapper.GetStatus, Capability("status:read", nil))
	e.Any("/*", func(c echo.Context) error { return problem(c, domain.ErrUnavailable) })
	return e, nil
}
func (h *IdentityHandler) EnrollClient(ctx context.Context, request EnrollClientRequestObject) (EnrollClientResponseObject, error) {
	if request.Body == nil {
		return nil, domain.ErrInvalidInput
	}
	body := request.Body
	capabilities := make([]string, len(body.Capabilities))
	for i, capability := range body.Capabilities {
		capabilities[i] = string(capability)
	}
	credential, err := h.identity.Enroll(ctx, principal(ctx), service.EnrollInput{ClientID: body.Id, Role: domain.Role(body.Role), ProjectIDs: body.ProjectIds, Capabilities: capabilities})
	if err != nil {
		return nil, err
	}
	p := credential.Principal
	return EnrollClient201JSONResponse{SchemaVersion: "3", Bearer: credential.Bearer, Client: Client{Id: p.ClientID, Role: Role(p.Role), ProjectIds: p.ProjectIDs, Capabilities: p.Capabilities, AuthEpoch: int64(p.AuthEpoch)}}, nil
}
func (h *IdentityHandler) RevokeClient(ctx context.Context, request RevokeClientRequestObject) (RevokeClientResponseObject, error) {
	if err := h.identity.Revoke(ctx, principal(ctx), request.Id); err != nil {
		return nil, err
	}
	return RevokeClient200JSONResponse{SchemaVersion: "3", Receipt: Receipt{Id: "revocation", State: "revoked"}}, nil
}
func (h *IdentityHandler) GetStatus(ctx context.Context, _ GetStatusRequestObject) (GetStatusResponseObject, error) {
	p := principal(ctx)
	return GetStatus200JSONResponse{SchemaVersion: "3", ConnectionState: StatusResultConnectionStateAvailable, AuthorizedProjectIds: p.ProjectIDs, AuthorizedCapabilities: p.Capabilities, CapabilityGaps: []string{"recovery_unavailable", "capture_unavailable", "retrieval_unavailable"}}, nil
}
