package handler_test

import (
	"context"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/handler"
	"github.com/stretchr/testify/assert"
	"net/http/httptest"
	"testing"
)

type privateHealth struct{}

func (privateHealth) Liveness() domain.HealthReport { return domain.HealthReport{Live: true} }
func (privateHealth) Readiness(context.Context) domain.HealthReport {
	return domain.HealthReport{Live: true, Maintenance: domain.MaintenanceState{Fence: domain.MaintenanceFence{OperationID: "private-operation"}, BackupID: "private-backup"}, JournalSequence: 123, Reasons: []string{"maintenance"}}
}
func TestPublicHealthOnlyExposesBooleansAndSafeReasons(t *testing.T) {
	h := handler.NewHealthHandler(privateHealth{})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	assert.Equal(t, 503, w.Code)
	assert.JSONEq(t, `{"ok":false,"reasons":["maintenance"]}`, w.Body.String())
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/livez", nil))
	assert.Equal(t, 200, w.Code)
	assert.JSONEq(t, `{"ok":true}`, w.Body.String())
}
