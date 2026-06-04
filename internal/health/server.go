// Package health provides an HTTP health endpoint for gonner.
package health

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
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
	opts    Options
	manager *runner.Manager
	server  *http.Server
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
	return &Server{opts: opts, manager: manager}
}

// Start starts the HTTP server in a goroutine. It shuts down gracefully when ctx is cancelled.
// Returns an error if the server cannot bind to the configured address.
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRoot)
	mux.HandleFunc("/health", s.handleHealth) // public liveness probe
	mux.HandleFunc("/ready", s.handleReady)   // public readiness probe
	mux.HandleFunc("/status", s.authMiddleware(s.handleStatus))
	if s.opts.EnableMetrics {
		mux.HandleFunc("/metrics", s.authMiddleware(s.handleMetrics))
	}

	addr := fmt.Sprintf("%s:%d", s.opts.BindAddr, s.opts.Port)
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

	// TLS hardening: require modern protocol versions when serving HTTPS.
	if s.opts.TLS != nil {
		s.server.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}

	go func() {
		defer logging.Recover("health-server")
		scheme := "http"
		if s.opts.TLS != nil {
			scheme = "https"
		}
		logging.Gonner("Health endpoint listening on %s://%s", scheme, addr)

		var serveErr error
		if s.opts.TLS != nil {
			serveErr = s.server.ServeTLS(listener, s.opts.TLS.CertFile, s.opts.TLS.KeyFile)
		} else {
			serveErr = s.server.Serve(listener)
		}
		if serveErr != nil && serveErr != http.ErrServerClosed {
			logging.Gonner("Health server error: %v", serveErr)
		}
	}()

	go func() {
		defer logging.Recover("health-shutdown")
		<-ctx.Done()
		logging.Gonner("Shutting down health endpoint...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.server.Shutdown(shutdownCtx)
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
