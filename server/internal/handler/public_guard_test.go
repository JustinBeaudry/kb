package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/platform"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type guardAdmission struct {
	admitted, finished, active atomic.Int64
	denied                     bool
	onFinish                   func(domain.OperationPermit) error
}

func (a *guardAdmission) Admit(context.Context, string, string) (domain.OperationPermit, error) {
	n := a.admitted.Add(1)
	if a.denied {
		return domain.OperationPermit{}, domain.ErrMaintenance
	}
	a.active.Add(1)
	return domain.OperationPermit{ID: strconv.FormatInt(n, 10)}, nil
}
func (a *guardAdmission) Finish(_ context.Context, p domain.OperationPermit) error {
	if a.onFinish != nil {
		if err := a.onFinish(p); err != nil {
			return err
		}
	}
	a.finished.Add(1)
	a.active.Add(-1)
	return nil
}

type guardIdentity struct {
	fixtureIdentity
	authenticate func(context.Context, string) (domain.Principal, error)
}

func (i guardIdentity) Authenticate(ctx context.Context, bearer string) (domain.Principal, error) {
	return i.authenticate(ctx, bearer)
}
func guardLogger() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

// guardPrefix is shared by every fixture bearer, so tests can assert that no
// bearer reaches a response or a log.
var guardPrefix = strings.Repeat("a", 48)
var guardBearers atomic.Int64

// guardRequest presents a fresh credential, so shared-capacity tests are not
// limited by one credential's share.
func guardRequest() *http.Request {
	return guardRequestAs(guardPrefix + fmt.Sprintf("%016x", guardBearers.Add(1)))
}
func guardRequestAs(bearer string) *http.Request {
	r := httptest.NewRequest("GET", "/v1/status", nil)
	r.Header.Set("Authorization", "Bearer "+bearer)
	return r
}

func TestPublicGuardRejectsMalformedBeforeAdmissionAndAuthentication(t *testing.T) {
	cases := [][]string{nil, {""}, {"Basic " + strings.Repeat("a", 64)}, {"bearer " + strings.Repeat("a", 64)}, {"Bearer short"}, {"Bearer " + strings.Repeat("a", 65)}, {"Bearer  " + strings.Repeat("a", 63)}, {"Bearer " + strings.Repeat("a", 63) + " "}, {"Bearer " + strings.Repeat("a", 64), "Bearer " + strings.Repeat("b", 64)}}
	for index, headers := range cases {
		t.Run(strconv.Itoa(index), func(t *testing.T) {
			var auth atomic.Int64
			a := &guardAdmission{}
			e, err := NewIdentityServer(guardIdentity{authenticate: func(context.Context, string) (domain.Principal, error) {
				auth.Add(1)
				return domain.Principal{}, domain.ErrUnauthorized
			}}, guardLogger())
			require.NoError(t, err)
			h := GuardPublicAPI(platform.AdmittedHandler(a, "api", e), guardLogger())
			r := guardRequest()
			r.Header.Del("Authorization")
			for _, v := range headers {
				r.Header.Add("Authorization", v)
			}
			response := httptest.NewRecorder()
			h.ServeHTTP(response, r)
			assert.Equal(t, 401, response.Code)
			assert.Zero(t, a.admitted.Load())
			assert.Zero(t, a.finished.Load())
			assert.Zero(t, auth.Load())
			var problem Problem
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &problem))
			assert.Equal(t, ProblemSchemaVersion("3"), problem.SchemaVersion)
			assert.Equal(t, ProblemCode("unauthorized"), problem.Code)
			assert.NotEmpty(t, problem.RequestId)
		})
	}
}

func TestPublicGuardKeepsCredentialLookupInsideMaintenance(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(strconv.FormatBool(closed), func(t *testing.T) {
			a := &guardAdmission{denied: closed}
			var auth atomic.Int64
			e, err := NewIdentityServer(guardIdentity{authenticate: func(ctx context.Context, _ string) (domain.Principal, error) {
				auth.Add(1)
				assert.Equal(t, int64(1), a.active.Load())
				assert.True(t, domain.OperationSettled(ctx))
				return domain.Principal{}, domain.ErrUnauthorized
			}}, guardLogger())
			require.NoError(t, err)
			h := GuardPublicAPI(platform.AdmittedHandler(a, "api", e), guardLogger())
			response := httptest.NewRecorder()
			h.ServeHTTP(response, guardRequest())
			if closed {
				assert.Equal(t, 503, response.Code)
				assert.Zero(t, auth.Load())
				assert.Zero(t, a.finished.Load())
			} else {
				assert.Equal(t, 401, response.Code)
				assert.Equal(t, int64(1), auth.Load())
				assert.Equal(t, int64(1), a.finished.Load())
			}
		})
	}
}

func TestPublicGuardBoundsBurstAndRefillsEightPerSecond(t *testing.T) {
	now := time.Unix(100, 0)
	a := &guardAdmission{}
	var logs strings.Builder
	h := newPublicAPIGuard(platform.AdmittedHandler(a, "api", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401) })), slog.New(slog.NewJSONHandler(&logs, nil)), func() time.Time { return now })
	for range 20 {
		response := httptest.NewRecorder()
		r := guardRequest()
		r.Header.Del("Authorization")
		h.ServeHTTP(response, r)
		require.Equal(t, 401, response.Code)
	}
	assert.Zero(t, a.admitted.Load(), "malformed requests must not consume dispatch capacity")
	for range 16 {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, guardRequest())
		require.Equal(t, 401, response.Code)
	}
	for range 20 {
		response := httptest.NewRecorder()
		r := guardRequest()
		r.Header.Set("X-Forwarded-For", "attacker-controlled-identity")
		h.ServeHTTP(response, r)
		assert.Equal(t, 429, response.Code)
		assert.Equal(t, "1", response.Header().Get("Retry-After"))
		assert.Less(t, response.Body.Len(), 1024)
		var p Problem
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &p))
		assert.Equal(t, ProblemSchemaVersion("3"), p.SchemaVersion)
		assert.Equal(t, ProblemCode("unavailable"), p.Code)
		assert.Equal(t, p.RequestId, response.Header().Get("X-Request-Id"))
		assert.NotContains(t, response.Body.String(), guardPrefix)
	}
	assert.Equal(t, int64(16), a.admitted.Load())
	assert.Equal(t, int64(16), a.finished.Load())
	assert.Contains(t, logs.String(), `"status":429`)
	assert.Contains(t, logs.String(), `"request_id":`)
	assert.NotContains(t, logs.String(), "attacker-controlled")
	assert.NotContains(t, logs.String(), guardPrefix)
	now = now.Add(125 * time.Millisecond)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, guardRequest())
	assert.Equal(t, 401, response.Code)
	response = httptest.NewRecorder()
	h.ServeHTTP(response, guardRequest())
	assert.Equal(t, 429, response.Code)
	now = now.Add(time.Hour)
	for range 16 {
		response = httptest.NewRecorder()
		h.ServeHTTP(response, guardRequest())
		assert.Equal(t, 401, response.Code)
	}
	response = httptest.NewRecorder()
	h.ServeHTTP(response, guardRequest())
	assert.Equal(t, 429, response.Code)
}

func TestPublicGuardHoldsEightSlotsThroughFinishAndRejectsWithoutQueue(t *testing.T) {
	finishing := make(chan struct{}, 8)
	release := make(chan struct{})
	done := make(chan int, 8)
	a := &guardAdmission{onFinish: func(p domain.OperationPermit) error {
		n, _ := strconv.Atoi(p.ID)
		if n <= 8 {
			finishing <- struct{}{}
			<-release
		}
		return nil
	}}
	now := time.Now()
	h := newPublicAPIGuard(platform.AdmittedHandler(a, "api", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })), guardLogger(), func() time.Time { return now })
	for range 8 {
		go func() {
			response := httptest.NewRecorder()
			h.ServeHTTP(response, guardRequest())
			done <- response.Code
		}()
	}
	for range 8 {
		select {
		case <-finishing:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("dispatch did not reach Finish")
		}
	}
	response := httptest.NewRecorder()
	h.ServeHTTP(response, guardRequest())
	assert.Equal(t, 429, response.Code)
	assert.Equal(t, int64(8), a.admitted.Load())
	assert.Zero(t, a.finished.Load())
	close(release)
	for range 8 {
		select {
		case code := <-done:
			assert.Equal(t, 204, code)
		case <-time.After(time.Second):
			t.Fatal("dispatch did not join")
		}
	}
	response = httptest.NewRecorder()
	h.ServeHTTP(response, guardRequest())
	assert.Equal(t, 204, response.Code)
	assert.Equal(t, int64(9), a.finished.Load())
}

func TestPublicGuardReleasesSlotAfterFailuresAndPanics(t *testing.T) {
	for _, mode := range []string{"success", "handler_failure", "finish_failure", "panic"} {
		t.Run(mode, func(t *testing.T) {
			a := &guardAdmission{}
			if mode == "finish_failure" {
				a.onFinish = func(domain.OperationPermit) error { return domain.ErrUnavailable }
			}
			now := time.Now()
			h := newPublicAPIGuard(platform.AdmittedHandler(a, "api", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if mode == "panic" {
					panic("private panic")
				}
				if mode == "handler_failure" {
					w.WriteHeader(503)
				} else {
					w.WriteHeader(204)
				}
			})), guardLogger(), func() time.Time { return now })
			for range 9 {
				response := httptest.NewRecorder()
				if mode == "panic" {
					assert.Panics(t, func() { h.ServeHTTP(response, guardRequest()) })
				} else {
					h.ServeHTTP(response, guardRequest())
					assert.NotEqual(t, 429, response.Code)
				}
			}
			assert.Equal(t, int64(9), a.admitted.Load())
		})
	}
}

func TestPublicGuardDoesNotSettleUncertainWork(t *testing.T) {
	store := &outcomeMaintenanceStore{state: domain.MaintenanceState{Phase: "bootstrap", Fence: domain.MaintenanceFence{OperationID: "bootstrap", Generation: 1}}}
	maintenance, err := service.NewMaintenanceService(t.Context(), store)
	require.NoError(t, err)
	require.NoError(t, maintenance.SetPhase(t.Context(), store.state.Fence, "recovering"))
	require.NoError(t, maintenance.MarkRecoveryComplete(t.Context(), store.state.Fence))
	require.NoError(t, maintenance.Resume(t.Context(), store.state.Fence))
	h := GuardPublicAPI(platform.AdmittedHandler(maintenance, "api", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		domain.MarkEffectUncertain(r.Context())
		w.WriteHeader(409)
	})), guardLogger())
	response := httptest.NewRecorder()
	h.ServeHTTP(response, guardRequest())
	require.Equal(t, 409, response.Code)
	fence, err := maintenance.Begin(t.Context(), "backup")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	t.Cleanup(cancel)
	assert.Error(t, maintenance.WaitDrained(ctx, fence))
}

func TestPublicGuardGivesEachCredentialHalfTheCapacity(t *testing.T) {
	now := time.Unix(100, 0)
	a := &guardAdmission{}
	h := newPublicAPIGuard(platform.AdmittedHandler(a, "api", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })), guardLogger(), func() time.Time { return now })
	noisy := strings.Repeat("b", 64)
	serve := func(r *http.Request) int {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, r)
		return response.Code
	}
	for range credentialRequestBurst {
		require.Equal(t, 204, serve(guardRequestAs(noisy)))
	}
	assert.Equal(t, 429, serve(guardRequestAs(noisy)), "one credential's burst is half the shared burst")
	assert.Equal(t, 204, serve(guardRequestAs(strings.Repeat("c", 64))), "other clients keep the rest")

	// The share refills at half the shared rate.
	now = now.Add(time.Second / credentialRequestsPerSecond)
	assert.Equal(t, 204, serve(guardRequestAs(noisy)))
	assert.Equal(t, 429, serve(guardRequestAs(noisy)))
}

func TestPublicGuardBoundsOneCredentialsConcurrentRequests(t *testing.T) {
	entered := make(chan struct{}, publicConcurrentRequests)
	release := make(chan struct{})
	a := &guardAdmission{}
	now := time.Unix(100, 0)
	h := newPublicAPIGuard(platform.AdmittedHandler(a, "api", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		<-release
		w.WriteHeader(204)
	})), guardLogger(), func() time.Time { return now })
	noisy := strings.Repeat("d", 64)
	done := make(chan int, credentialConcurrentRequests)
	for range credentialConcurrentRequests {
		go func() {
			response := httptest.NewRecorder()
			h.ServeHTTP(response, guardRequestAs(noisy))
			done <- response.Code
		}()
	}
	for range credentialConcurrentRequests {
		select {
		case <-entered:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("requests did not start")
		}
	}
	response := httptest.NewRecorder()
	h.ServeHTTP(response, guardRequestAs(noisy))
	assert.Equal(t, 429, response.Code, "a fifth concurrent request from one credential waits for none")
	other := httptest.NewRecorder()
	otherDone := make(chan struct{})
	go func() { h.ServeHTTP(other, guardRequestAs(strings.Repeat("e", 64))); close(otherDone) }()
	close(release)
	<-otherDone
	assert.Equal(t, 204, other.Code, "another client still has slots")
	for range credentialConcurrentRequests {
		assert.Equal(t, 204, <-done)
	}
}

func TestPublicGuardCredentialTableStaysBounded(t *testing.T) {
	now := time.Unix(100, 0)
	g := newPublicAPIGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }), guardLogger(), func() time.Time { return now }).(*publicAPIGuard)
	for i := range maxTrackedCredentials + 10 {
		now = now.Add(time.Second) // keep the shared bucket full
		response := httptest.NewRecorder()
		g.ServeHTTP(response, guardRequest())
		require.Equal(t, 204, response.Code, i)
	}
	assert.LessOrEqual(t, len(g.byKey), maxTrackedCredentials)
}
