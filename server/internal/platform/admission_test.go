package platform_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/platform"
	"github.com/stretchr/testify/assert"
)

func TestHTTPAdmissionCoversHandlerAndRetainsUncertainWrites(t *testing.T) {
	for _, status := range []int{200, 403, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			a := &workerAdmission{make(chan struct{}, 1), make(chan struct{}, 1)}
			h := platform.AdmittedHandler(a, "instance", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Len(t, a.admitted, 1)
				assert.Empty(t, a.finished)
				w.WriteHeader(status)
			}))
			response := httptest.NewRecorder()
			h.ServeHTTP(response, httptest.NewRequestWithContext(context.Background(), "POST", "/v1/artifacts", nil))
			assert.Equal(t, status, response.Code)
			assert.Len(t, a.finished, 1, "status alone is not mutation uncertainty")
		})
	}
}
