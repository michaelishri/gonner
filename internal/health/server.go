// Package health provides an HTTP health endpoint for gonner.
package health

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/michaelishri/gonner/internal/config"
	"github.com/michaelishri/gonner/internal/logging"
	"github.com/michaelishri/gonner/internal/runner"
)

// DefaultHealthPort is the default port for the health endpoint.
const DefaultHealthPort = 8089

// Options configures a health Server.
type Options struct {
	BindAddr      string // default "0.0.0.0"
	Port          int
	AuthToken     string // empty disables auth
	EnableMetrics bool
	TLS           *config.TLSConfig
}

// Server is the HTTP health endpoint server.
type Server struct {
	opts     Options
	manager  *runner.Manager
	server   *http.Server
	listener net.Listener
	errors   chan error
	done     chan struct{}
}

// NewServer creates a new health server with default options.
// Kept for backwards compatibility.
func NewServer(port int, manager *runner.Manager) *Server {
	return NewServerWithOptions(Options{Port: port}, manager)
}

// NewServerWithOptions creates a server with the provided options.
func NewServerWithOptions(opts Options, manager *runner.Manager) *Server {
	if opts.BindAddr == "" {
		opts.BindAddr = "0.0.0.0"
	}
	return &Server{opts: opts, manager: manager, errors: make(chan error, 1), done: make(chan struct{})}
}

// Start starts the HTTP server in a goroutine. It shuts down gracefully when ctx is cancelled.
// Returns an error if the server cannot bind to the configured address.
func (s *Server) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var tlsConfig *tls.Config
	if s.opts.TLS != nil {
		cert, err := tls.LoadX509KeyPair(s.opts.TLS.CertFile, s.opts.TLS.KeyFile)
		if err != nil {
			return fmt.Errorf("loading health TLS certificate: %w", err)
		}
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRoot)
	mux.HandleFunc("/health", s.handleHealth) // public liveness probe
	mux.HandleFunc("/ready", s.handleReady)   // public readiness probe
	mux.HandleFunc("/status", s.authMiddleware(s.handleStatus))
	if s.opts.EnableMetrics {
		mux.HandleFunc("/metrics", s.authMiddleware(s.handleMetrics))
	}

	addr := net.JoinHostPort(s.opts.BindAddr, strconv.Itoa(s.opts.Port))
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("health endpoint failed to listen on %s: %w", addr, err)
	}

	// HTTP hardening: enforce timeouts to mitigate Slowloris.
	s.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 16, // 64 KiB
	}

	s.server.TLSConfig = tlsConfig
	if tlsConfig != nil {
		listener = tls.NewListener(listener, tlsConfig)
	}
	s.listener = listener
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		defer close(s.errors)
		logging.Gonner("Health endpoint listening on %s", listener.Addr())
		if err := s.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			s.errors <- fmt.Errorf("health server: %w", err)
		}
	}()
	go func() {
		defer close(s.done)
		select {
		case <-ctx.Done():
		case <-serveDone:
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.server.Shutdown(shutdownCtx); err != nil {
			_ = s.server.Close()
		}
		<-serveDone
	}()

	return nil
}

// authMiddleware enforces bearer-token auth when configured.
func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	if s.opts.AuthToken == "" {
		return next
	}
	expected := "Bearer " + s.opts.AuthToken
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		if len(got) != len(expected) || subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="gonner"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// Errors reports an unexpected serving failure; normal shutdown closes it.
func (s *Server) Errors() <-chan error { return s.errors }

// Done is closed once the listener and active handlers have shut down.
func (s *Server) Done() <-chan struct{} { return s.done }
