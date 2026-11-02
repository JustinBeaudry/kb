package handler

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"io"
	"net/http"
	"strings"
)

type ServiceAdmission interface {
	Admit(context.Context, string, string) (domain.OperationPermit, error)
	Finish(context.Context, domain.OperationPermit) error
	State(context.Context) (domain.MaintenanceState, error)
}
type MaintenanceHandler struct {
	admission ServiceAdmission
	secret    string
}

func NewMaintenanceHandler(admission ServiceAdmission, secret string) (*MaintenanceHandler, error) {
	if admission == nil || len(secret) < 32 {
		return nil, domain.ErrInvalidInput
	}
	return &MaintenanceHandler{admission: admission, secret: secret}, nil
}
func (h *MaintenanceHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+h.secret)) != 1 {
		controlError(w, domain.ErrUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/admit":
		var input struct {
			InstanceID string `json:"instance_id"`
			Kind       string `json:"kind"`
		}
		if decodeControl(w, r, &input) != nil {
			controlError(w, domain.ErrInvalidInput)
			return
		}
		if strings.HasPrefix(input.Kind, "host-admin:") || strings.Contains(input.Kind, "recovery") {
			controlError(w, domain.ErrForbidden)
			return
		}
		p, err := h.admission.Admit(r.Context(), input.InstanceID, input.Kind)
		if err != nil {
			controlError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(p)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/finish":
		var p domain.OperationPermit
		if decodeControl(w, r, &p) != nil || p.Unknown {
			controlError(w, domain.ErrInvalidInput)
			return
		}
		if strings.HasPrefix(p.Kind, "host-admin:") || strings.Contains(p.Kind, "recovery") {
			controlError(w, domain.ErrForbidden)
			return
		}
		if err := h.admission.Finish(r.Context(), p); err != nil {
			controlError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(struct {
			OK bool `json:"ok"`
		}{true})
	case r.Method == http.MethodGet && r.URL.Path == "/v1/state":
		state, err := h.admission.State(r.Context())
		if err != nil {
			controlError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(state)
	default:
		http.NotFound(w, r)
	}
}
func decodeControl(w http.ResponseWriter, r *http.Request, target any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	d.DisallowUnknownFields()
	if d.Decode(target) != nil || d.Decode(&struct{}{}) != io.EOF {
		return domain.ErrInvalidInput
	}
	return nil
}
func controlError(w http.ResponseWriter, err error) {
	code, status := "unavailable", http.StatusServiceUnavailable
	switch {
	case errors.Is(err, domain.ErrUnauthorized):
		code, status = "unauthorized", http.StatusUnauthorized
	case errors.Is(err, domain.ErrForbidden):
		code, status = "forbidden", http.StatusForbidden
	case errors.Is(err, domain.ErrInvalidInput):
		code, status = "invalid_input", http.StatusBadRequest
	case errors.Is(err, domain.ErrStale):
		code, status = "stale", http.StatusConflict
	case errors.Is(err, domain.ErrUnknownOutcome):
		code, status = "unknown_outcome", http.StatusConflict
	case errors.Is(err, domain.ErrMaintenance):
		code = "maintenance"
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Code string `json:"code"`
	}{code})
}
