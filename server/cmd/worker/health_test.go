package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/handler"
	"github.com/stretchr/testify/assert"
)

func TestWorkerHealthUnreadyReturnsToStarting(t *testing.T) {
	h := &workerHealth{}
	h.health.Store(handler.NewHealthHandler(nil))
	h.Unready()
	for path, want := range map[string]int{"/livez": http.StatusOK, "/readyz": http.StatusServiceUnavailable} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		assert.Equal(t, want, rec.Code, path)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	assert.Contains(t, rec.Body.String(), `"starting"`)
}
