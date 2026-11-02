package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, `{"event":"fixture_proxy_stopped"}`)
		os.Exit(1)
	}
}
func run() error {
	proxy, err := NewProxy("http://127.0.0.1:8333", 8<<20, 120*time.Second)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	makeServer := func(address string, handler http.Handler) *http.Server {
		return &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 120 * time.Second, WriteTimeout: 120 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 32 << 10, ErrorLog: log.New(io.Discard, "", 0)}
	}
	servers := []*http.Server{makeServer("0.0.0.0:8334", proxy.DataHandler()), makeServer("0.0.0.0:8335", proxy.ControlHandler())}
	failures := make(chan error, len(servers))
	for _, server := range servers {
		go func() { failures <- server.ListenAndServe() }()
	}
	select {
	case <-ctx.Done():
	case err = <-failures:
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, server := range servers {
		_ = server.Shutdown(shutdown)
	}
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
