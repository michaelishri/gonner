package health

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/michaelishri/gonner/internal/runner"
)

type healthResponse struct {
	Status string `json:"status"`
}

type statusResponse struct {
	Uptime    string               `json:"uptime"`
	Mode      string               `json:"mode"`
	PID       int                  `json:"pid"`
	Processes []runner.ProcessInfo `json:"processes"`
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/status", http.StatusTemporaryRedirect)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	if s.manager.IsShuttingDown() {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(healthResponse{Status: "shutting_down"})
		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(healthResponse{Status: "healthy"})
}

// handleReady is a readiness probe. It returns 200 only when gonner is not
// shutting down and all critical processes are running. Suitable as a
// Kubernetes readiness probe (distinct from the /health liveness probe).
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	if s.manager.Ready() {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(healthResponse{Status: "ready"})
		return
	}

	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(healthResponse{Status: "not_ready"})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	resp := statusResponse{
		Uptime:    s.manager.Uptime().Truncate(time.Second).String(),
		Mode:      s.manager.Mode(),
		PID:       os.Getpid(),
		Processes: s.manager.Processes(),
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// handleMetrics serves Prometheus-compatible metrics in the text exposition format.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	// Top-level metrics
	fmt.Fprintf(w, "# HELP gonner_uptime_seconds Time since gonner started.\n")
	fmt.Fprintf(w, "# TYPE gonner_uptime_seconds gauge\n")
	fmt.Fprintf(w, "gonner_uptime_seconds %d\n", int64(s.manager.Uptime().Seconds()))

	fmt.Fprintf(w, "# HELP gonner_shutting_down 1 if gonner is shutting down.\n")
	fmt.Fprintf(w, "# TYPE gonner_shutting_down gauge\n")
	val := 0
	if s.manager.IsShuttingDown() {
		val = 1
	}
	fmt.Fprintf(w, "gonner_shutting_down %d\n", val)

	fmt.Fprintf(w, "# HELP gonner_ready 1 if gonner is ready (all critical processes running).\n")
	fmt.Fprintf(w, "# TYPE gonner_ready gauge\n")
	ready := 0
	if s.manager.Ready() {
		ready = 1
	}
	fmt.Fprintf(w, "gonner_ready %d\n", ready)

	procs := s.manager.Processes()
	fmt.Fprintf(w, "# HELP gonner_process_running_instances Running instances of each managed process.\n")
	fmt.Fprintf(w, "# TYPE gonner_process_running_instances gauge\n")
	for _, p := range procs {
		fmt.Fprintf(w, "gonner_process_running_instances{name=%q} %d\n", p.Name, p.RunningInstances)
	}

	fmt.Fprintf(w, "# HELP gonner_process_configured_instances Configured instances of each managed process.\n")
	fmt.Fprintf(w, "# TYPE gonner_process_configured_instances gauge\n")
	for _, p := range procs {
		fmt.Fprintf(w, "gonner_process_configured_instances{name=%q} %d\n", p.Name, p.Instances)
	}

	fmt.Fprintf(w, "# HELP gonner_process_restarts_total Restart count for each managed process.\n")
	fmt.Fprintf(w, "# TYPE gonner_process_restarts_total counter\n")
	for _, p := range procs {
		fmt.Fprintf(w, "gonner_process_restarts_total{name=%q} %d\n", p.Name, p.Restarts)
	}

	fmt.Fprintf(w, "# HELP gonner_process_up 1 if the process is in the running state.\n")
	fmt.Fprintf(w, "# TYPE gonner_process_up gauge\n")
	for _, p := range procs {
		up := 0
		if p.Status == runner.StateRunning {
			up = 1
		}
		fmt.Fprintf(w, "gonner_process_up{name=%q,critical=%q} %d\n", p.Name, boolStr(p.Critical), up)
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
