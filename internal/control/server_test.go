package control

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michaelishri/gonner/internal/config"
	"github.com/michaelishri/gonner/internal/runner"
)

type fakeManager struct{ calls int }

func (*fakeManager) Instances() []runner.InstanceInfo {
	return []runner.InstanceInfo{{ID: "worker", Generation: strings.Repeat("a", 32), State: runner.StateRunning}}
}
func (m *fakeManager) Restart(id, gen string, _ time.Duration) error {
	if id != "worker" || gen != strings.Repeat("a", 32) {
		return runner.ErrGeneration
	}
	m.calls++
	return nil
}
func (*fakeManager) IsShuttingDown() bool { return false }

func TestStrictControlRequests(t *testing.T) {
	valid := `{"instance":"worker","expectedGeneration":"` + strings.Repeat("a", 32) + `"}`
	cases := []struct {
		body   string
		status int
	}{
		{valid, 202}, {valid + `{}`, 400}, {`{"instance":"worker","instance":"other"}`, 400}, {`{"pid":12}`, 400},
		{strings.Replace(valid, "worker", "absent", 1), 409}, {strings.Repeat(" ", 1025) + valid, 400},
	}
	for _, tt := range cases {
		m := &fakeManager{}
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/v1/restart", strings.NewReader(tt.body))
		r.Header.Set("Content-Type", "application/json")
		Handler(config.ControlConfig{}, m).ServeHTTP(w, r)
		if w.Code != tt.status {
			t.Fatalf("status %d want %d", w.Code, tt.status)
		}
	}
}

func TestUnixSocketProtectionAndLifecycle(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	cfg := config.ControlConfig{Socket: filepath.Join(dir, "control.sock")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop, err := Start(ctx, cfg, &fakeManager{})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	info, _ := os.Stat(cfg.Socket)
	if info.Mode().Perm() != 0600 {
		t.Fatal("socket not private")
	}
	if _, err := Start(ctx, cfg, &fakeManager{}); err == nil {
		t.Fatal("replaced live socket")
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", cfg.Socket)
	}}
	defer transport.CloseIdleConnections()
	c := &http.Client{Transport: transport, Timeout: time.Second}
	res, err := c.Get("http://local/v1/instances")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(b), `"version":1`) {
		t.Fatal("missing management version")
	}
	stop()
	os.Chmod(dir, 0755)
	if _, err := Start(ctx, cfg, &fakeManager{}); err == nil {
		t.Fatal("public directory accepted")
	}
}
