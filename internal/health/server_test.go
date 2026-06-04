package health

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/michaelishri/gonner/internal/config"
	"github.com/michaelishri/gonner/internal/runner"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func newTestServer(t *testing.T, opts Options) (string, context.CancelFunc) {
	t.Helper()
	mgr := runner.NewManager(&config.Config{Mode: "parallel"})
	srv := NewServerWithOptions(opts, mgr)
	ctx, cancel := context.WithCancel(context.Background())
	if err := srv.Start(ctx); err != nil {
		cancel()
		t.Fatalf("start: %v", err)
	}
	// brief wait for listener
	time.Sleep(50 * time.Millisecond)
	return "http://127.0.0.1:" + itoa(opts.Port), cancel
}

func itoa(n int) string {
	s := ""
	if n == 0 {
		return "0"
	}
	for n > 0 {
		s = string(rune('0'+(n%10))) + s
		n /= 10
	}
	return s
}

func TestHealthEndpointAccessibleWithoutAuth(t *testing.T) {
	port := freePort(t)
	base, cancel := newTestServer(t, Options{Port: port, BindAddr: "127.0.0.1", AuthToken: "secret-token-1234567"})
	defer cancel()

	resp, err := http.Get(base + "/health")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
}

func TestStatusRequiresAuth(t *testing.T) {
	port := freePort(t)
	base, cancel := newTestServer(t, Options{Port: port, BindAddr: "127.0.0.1", AuthToken: "secret-token-1234567"})
	defer cancel()

	resp, err := http.Get(base + "/status")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("status: got %d want 401", resp.StatusCode)
	}
}

func TestStatusWithAuth(t *testing.T) {
	port := freePort(t)
	token := "secret-token-1234567"
	base, cancel := newTestServer(t, Options{Port: port, BindAddr: "127.0.0.1", AuthToken: token})
	defer cancel()

	req, _ := http.NewRequest("GET", base+"/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	port := freePort(t)
	base, cancel := newTestServer(t, Options{Port: port, BindAddr: "127.0.0.1", EnableMetrics: true})
	defer cancel()

	resp, err := http.Get(base + "/metrics")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "gonner_uptime_seconds") {
		t.Errorf("metrics output missing gonner_uptime_seconds: %s", body)
	}
}

func TestMetricsDisabledByDefault(t *testing.T) {
	port := freePort(t)
	base, cancel := newTestServer(t, Options{Port: port, BindAddr: "127.0.0.1"})
	defer cancel()

	resp, err := http.Get(base + "/metrics")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("status: got %d want 404", resp.StatusCode)
	}
}

func TestReadyEndpointPublic(t *testing.T) {
	port := freePort(t)
	// No critical processes are defined, so the manager reports ready.
	base, cancel := newTestServer(t, Options{Port: port, BindAddr: "127.0.0.1", AuthToken: "secret-token-1234567"})
	defer cancel()

	resp, err := http.Get(base + "/ready")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("ready: got %d want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "ready") {
		t.Errorf("ready body missing status: %s", body)
	}
}

func TestMetricsIncludesReadyGauge(t *testing.T) {
	port := freePort(t)
	base, cancel := newTestServer(t, Options{Port: port, BindAddr: "127.0.0.1", EnableMetrics: true})
	defer cancel()

	resp, err := http.Get(base + "/metrics")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "gonner_ready") {
		t.Errorf("metrics output missing gonner_ready: %s", body)
	}
}
