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
	identity    IdentityService
	capture     CaptureService
	checkpoints CheckpointService
	memory      MemoryService
	retrieval   RetrievalService
	policy      PolicyService
}

var _ StrictServerInterface = (*IdentityHandler)(nil)

func NewIdentityServer(identity IdentityService, logger *slog.Logger) (*echo.Echo, error) {
	return newMemoryServer(identity, nil, nil, logger)
}
func newMemoryServer(identity IdentityService, capture CaptureService, checkpoints CheckpointService, logger *slog.Logger) (*echo.Echo, error) {
	return configuredMemoryServer(identity, capture, checkpoints, nil, nil, logger)
}
func configuredMemoryServer(identity IdentityService, capture CaptureService, checkpoints CheckpointService, memory MemoryService, retrieval RetrievalService, logger *slog.Logger, policies ...PolicyService) (*echo.Echo, error) {
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
			action := "Limit request bodies to 65536 bytes."
			if c.Path() == "/v1/artifacts" {
				action = "Split evidence into chunks of at most 8388608 decoded bytes."
			}
			_ = writeProblem(c, 413, "invalid_input", "Request too large.", action)
			return
		}
		_ = problem(c, err)
	}
	e.Use(requestMetadata(logger), middleware.RecoverWithConfig(middleware.RecoverConfig{DisablePrintStack: true}), func(next echo.HandlerFunc) echo.HandlerFunc {
		small := middleware.BodyLimit("64K")(next)
		artifact := middleware.BodyLimit("11206656")(next)
		return func(c echo.Context) error {
			if capture != nil && c.Request().Method == "POST" && c.Path() == "/v1/artifacts" {
				return artifact(c)
			}
			return small(c)
		}
	}, middleware.ContextTimeout(10*time.Second), Authentication(identity))
	// Authentication runs before validation. kin-openapi must not re-interpret the
	// already authenticated principal or treat submitted fields as authority.
	e.Use(validator.OapiRequestValidatorWithOptions(spec, &validator.Options{Options: openapi3filter.Options{AuthenticationFunc: func(context.Context, *openapi3filter.AuthenticationInput) error { return nil }}, ErrorHandler: func(c echo.Context, he *echo.HTTPError) error {
		if he.Code == 404 {
			return problem(c, domain.ErrUnavailable)
		}
		return problem(c, domain.ErrInvalidInput)
	}}))
	var policy PolicyService
	if len(policies) > 0 {
		policy = policies[0]
	}
	strict := NewStrictHandler(&IdentityHandler{identity: identity, capture: capture, checkpoints: checkpoints, memory: memory, retrieval: retrieval, policy: policy}, nil)

	wrapper := ServerInterfaceWrapper{Handler: strict}
	if policy != nil {
		e.GET("/v1/policies/:project", wrapper.GetPolicy)
	}
	e.POST("/v1/clients", wrapper.EnrollClient, Capability("clients:manage", nil))
	e.POST("/v1/clients/:id\\:revoke", wrapper.RevokeClient, Capability("clients:manage", nil))
	e.GET("/v1/status", wrapper.GetStatus, Capability("status:read", nil))
	if capture != nil {
		e.POST("/v1/artifacts", wrapper.StageArtifact)
		e.POST("/v1/artifacts/:id\\:complete", wrapper.CompleteArtifact)
		e.POST("/v1/events\\:append", wrapper.AppendEvents)
	}
	if checkpoints != nil {
		e.PUT("/v1/checkpoints/:id", wrapper.PutCheckpoint)
		e.GET("/v1/checkpoints/:id", wrapper.GetCheckpoint)
	}
	if memory != nil {
		e.POST("/v1/candidates", wrapper.SubmitCandidate)
	}
	if retrieval != nil {
		e.POST("/v1/retrieval\\:orient", wrapper.OrientMemory)
		e.POST("/v1/retrieval\\:search", wrapper.SearchMemory)
		e.GET("/v1/evidence/:id", wrapper.FetchEvidence)
	}
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
	gaps := []string{"recovery_unavailable"}
	if h.retrieval == nil {
		gaps = append(gaps, "retrieval_unavailable")
	}
	if h.capture == nil {
		gaps = append(gaps, "capture_unavailable")
	}
	return GetStatus200JSONResponse{SchemaVersion: "3", ConnectionState: StatusResultConnectionStateAvailable, AuthorizedProjectIds: p.ProjectIDs, AuthorizedCapabilities: p.Capabilities, CapabilityGaps: gaps}, nil
}
