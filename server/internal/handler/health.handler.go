package handler

import (
	"context"
	"encoding/json"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"net/http"
)

type PublicHealth interface {
	Liveness() domain.HealthReport
	Readiness(context.Context) domain.HealthReport
}
type HealthHandler struct{ health PublicHealth }

func NewHealthHandler(h PublicHealth) *HealthHandler { return &HealthHandler{health: h} }
func (h *HealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || (r.URL.Path != "/livez" && r.URL.Path != "/readyz") {
		http.NotFound(w, r)
		return
	}
	report := domain.HealthReport{Reasons: []string{"startup"}}
	if h.health != nil {
		if r.URL.Path == "/livez" {
			report = h.health.Liveness()
		} else {
			report = h.health.Readiness(r.Context())
		}
	}
	ok := report.Ready
	if r.URL.Path == "/livez" {
		ok = report.Live
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if !ok {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(struct {
		OK      bool     `json:"ok"`
		Reasons []string `json:"reasons,omitempty"`
	}{ok, report.Reasons})
}
