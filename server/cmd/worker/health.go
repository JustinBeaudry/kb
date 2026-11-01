package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/handler"
	"github.com/JustinBeaudry/kb/server/internal/platform"
	"github.com/JustinBeaudry/kb/server/internal/service"
)

type workerHealth struct {
	server *http.Server
	health atomic.Pointer[handler.HealthHandler]
	done   chan error
}

func startWorkerHealth(ctx context.Context) (*workerHealth, error) {
	address := os.Getenv("KB_WORKER_HEALTH_LISTEN")
	if address == "" {
		address = "127.0.0.1:8081"
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, domain.ErrUnavailable
	}
	h := &workerHealth{done: make(chan error, 1)}
	h.server = &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192, BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() { h.done <- h.server.Serve(listener) }()
	return h, nil
}
func (h *workerHealth) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/livez" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"live":true}`))
		return
	}
	if r.URL.Path != "/readyz" {
		http.NotFound(w, r)
		return
	}
	current := h.health.Load()
	if current == nil {
		platform.WriteUnready(w, "starting")
		return
	}
	current.ServeHTTP(w, r)
}
func (h *workerHealth) Ready(health *service.HealthService) {
	h.health.Store(handler.NewHealthHandler(health))
}
func (h *workerHealth) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	shutdownErr := h.server.Shutdown(ctx)
	servedErr := <-h.done
	if errors.Is(servedErr, http.ErrServerClosed) {
		servedErr = nil
	}
	return errors.Join(shutdownErr, servedErr)
}
