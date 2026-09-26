package runner

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/michaelishri/gonner/internal/config"
)

func awaitInstance(t *testing.T, mgr *Manager, check func(InstanceInfo) bool) InstanceInfo {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, i := range mgr.Instances() {
			if check(i) {
				return i
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("instance did not reach expected state")
	return InstanceInfo{}
}

func TestConditionalRestartDoesNotTouchReplacementOrSibling(t *testing.T) {
	cfg := &config.Config{Mode: "parallel", Control: &config.ControlConfig{Socket: "/unused-control-fixture.sock", RestartGrace: config.Duration(10 * time.Millisecond)}, ShutdownTimeout: config.Duration(100 * time.Millisecond), Run: []config.ProcessConfig{
		{Name: "worker", Command: "exec sleep 60", Instances: 2, AutoRestart: true, Controllable: true,
			Backoff: &config.BackoffConfig{InitialDelay: config.Duration(100 * time.Millisecond), MaxDelay: config.Duration(100 * time.Millisecond), Multiplier: 1}},
	}}
	mgr := NewManager(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- mgr.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("processes not reaped")
		}
	}()
	a := awaitInstance(t, mgr, func(i InstanceInfo) bool { return i.ID == "worker/0" && i.State == StateRunning })
	b := awaitInstance(t, mgr, func(i InstanceInfo) bool { return i.ID == "worker/1" && i.State == StateRunning })
	if a.Generation == b.Generation || len(a.Generation) != 32 {
		t.Fatal("generation not unique")
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if err := mgr.Restart(a.ID, a.Generation, 10*time.Millisecond); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	next := awaitInstance(t, mgr, func(i InstanceInfo) bool {
		return i.ID == a.ID && i.State == StateRunning && i.Generation != a.Generation
	})
	if next.PreviousGeneration != a.Generation || !next.PreviousReaped {
		t.Fatal("replacement without reap evidence")
	}
	if mgr.Restart(a.ID, a.Generation, time.Millisecond) == nil {
		t.Fatal("stale restart accepted")
	}
	if mgr.Restart("unknown", next.Generation, time.Millisecond) == nil {
		t.Fatal("unconfigured target accepted")
	}
	for _, i := range mgr.Instances() {
		if i.ID == b.ID && i.Generation != b.Generation {
			t.Fatal("sibling restarted")
		}
	}
}

func TestRestartOnSuccessAndBootstrapIdentity(t *testing.T) {
	file := filepath.Join(t.TempDir(), "identity")
	cfg := config.ProcessConfig{Name: "recycle", Instances: 1, Command: "printf '%s %s\\n' \"$GONNER_INSTANCE_ID\" \"$GONNER_GENERATION\" >> '" + file + "'", AutoRestart: true, RestartOnSuccess: true, MaxRetries: 1,
		Env:     map[string]string{"GONNER_GENERATION": "spoof", "GONNER_INSTANCE_ID": "spoof"},
		Backoff: &config.BackoffConfig{InitialDelay: config.Duration(time.Millisecond), MaxDelay: config.Duration(time.Millisecond), Multiplier: 1}}
	p := NewProcess(cfg, 0, 100*time.Millisecond, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := p.Run(ctx); err == nil || p.State() != StateFailed || p.Info().Restarts != 1 {
		t.Fatalf("retry limit was not enforced: %v, %+v", err, p.Info())
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 2*(len("recycle ")+32+1) {
		t.Fatalf("unexpected bootstrap length %d", len(data))
	}
	first, second := string(data[:40]), string(data[41:81])
	if first == second {
		t.Fatal("generation reused")
	}
}

func TestCleanExitStillStopsByDefault(t *testing.T) {
	p := NewProcess(config.ProcessConfig{Name: "once", Instances: 1, Command: "exit 0", AutoRestart: true}, 0, time.Second, nil)
	if err := p.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.Info().Restarts != 0 {
		t.Fatal("legacy clean exit restarted")
	}
}

func TestClearEnvironmentRetainsOnlyExplicitValues(t *testing.T) {
	t.Setenv("ORIGIN_INHERITED_SENTINEL", "must-not-reach-child")
	p := NewProcess(config.ProcessConfig{ClearEnv: true, Env: map[string]string{"PATH": "/usr/bin:/bin"}}, 0, time.Second, nil)
	env := p.buildEnv()
	if len(env) != 1 || env[0] != "PATH=/usr/bin:/bin" {
		t.Fatal("inherited environment escaped")
	}
}

func TestNaturalExitIsNotReportedRunningDuringBackoff(t *testing.T) {
	cfg := config.ProcessConfig{Name: "recycle", Instances: 1, Command: "exit 0", AutoRestart: true, RestartOnSuccess: true, Controllable: true,
		Backoff: &config.BackoffConfig{InitialDelay: config.Duration(time.Second), MaxDelay: config.Duration(time.Second), Multiplier: 1}}
	p := NewProcess(cfg, 0, time.Second, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = p.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		info := p.instanceInfo()
		if info.PreviousReaped {
			if info.State == StateRunning {
				t.Fatal("reaped command reported running")
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("exit never confirmed")
}
