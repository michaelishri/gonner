package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/michaelishri/gonner/internal/config"
)

func TestFailedAndSkippedDependencyChainsTerminate(t *testing.T) {
	for _, skip := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed", true: "skipped"}[skip], func(t *testing.T) {
			first := config.ProcessConfig{Name: "first", Command: "true", CommandsBefore: []config.PreCommand{{Command: "exit 7"}}}
			if skip {
				first.WhenAll = []map[string]string{{"fileExists": "/gonner-missing-condition-marker"}}
			}
			marker := filepath.Join(t.TempDir(), "unrelated")
			mgr := NewManager(&config.Config{Run: []config.ProcessConfig{first, {Name: "second", Command: "true", DependsOn: []string{"first"}}, {Name: "third", Command: "true", DependsOn: []string{"second"}}, {Name: "unrelated", Command: "touch '" + marker + "'"}}})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := mgr.Run(ctx); err == nil {
				t.Fatal("unavailable dependency must fail eventual exit")
			}
			if ctx.Err() != nil {
				t.Fatal("dependency chain hung until deadline")
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatal("noncritical failure cancelled unrelated work")
			}
			for _, p := range mgr.processes {
				select {
				case <-p.Done():
				default:
					t.Errorf("%s Done open", p.Name())
				}
			}
			if mgr.processes[1].State() != StateSkipped || mgr.processes[2].State() != StateSkipped {
				t.Fatal("dependants not skipped")
			}
		})
	}
}
func TestCriticalPrecommandFailureCancelsAndReturnsError(t *testing.T) {
	for _, continueOnError := range []bool{false, true} {
		cmds := []config.ProcessConfig{{Name: "critical", Command: "true", Critical: true, CommandsBefore: []config.PreCommand{{Command: "exit 5", ContinueOnError: continueOnError}}}}
		if !continueOnError {
			cmds = append(cmds, config.ProcessConfig{Name: "other", Command: "exec sleep 30"})
		}
		mgr := NewManager(&config.Config{ShutdownTimeout: config.Duration(100 * time.Millisecond), Run: cmds})
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := mgr.Run(ctx)
		cancel()
		if (err == nil) != continueOnError {
			t.Fatalf("continue=%v error=%v", continueOnError, err)
		}
		if !continueOnError && mgr.processes[0].State() != StateFailed {
			t.Fatal("critical precommand not failed")
		}
	}
}
func TestCriticalUnavailableDependencyIsFatal(t *testing.T) {
	mgr := NewManager(&config.Config{Run: []config.ProcessConfig{{Name: "missing", Command: "true", WhenAll: []map[string]string{{"fileExists": "/gonner-missing"}}}, {Name: "critical", Command: "true", Critical: true, DependsOn: []string{"missing"}}, {Name: "other", Command: "exec sleep 30"}}, ShutdownTimeout: config.Duration(100 * time.Millisecond)})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := mgr.Run(ctx); err == nil || !strings.Contains(err.Error(), "never started") {
		t.Fatalf("error=%v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("critical blocked dependency did not cancel")
	}
}
func TestAnyInstanceStartGateAndShortLivedPrerequisite(t *testing.T) {
	// Barriers deliberately keep instance zero pending. This exercises the exact
	// shared gate used by parallel dependencies and sequential startup.
	p0 := NewProcess(config.ProcessConfig{Name: "multi", Instances: 2}, 0, time.Second, nil)
	p1 := NewProcess(config.ProcessConfig{Name: "multi", Instances: 2}, 1, time.Second, nil)
	p1.readyOnce.Do(func() { close(p1.readyCh) })
	p1.finish(StateStopped)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan bool, 1)
	go func() { result <- waitStarted(ctx, []*Process{p0, p1}) }()
	select {
	case ok := <-result:
		if !ok {
			t.Fatal("successful second instance ignored")
		}
	case <-time.After(time.Second):
		t.Fatal("waited on instance zero")
	}
	// Ready must remain the same closed channel across every restart.
	ready := p1.Ready()
	p1.state.Store(StateStarting)
	if ready != p1.Ready() {
		t.Fatal("ready gate replaced")
	}
	if !waitStarted(ctx, []*Process{p1}) {
		t.Fatal("one-shot prerequisite no longer satisfies gate")
	}
}
func TestBackoffExcludesDowntimeAndCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := NewBackoff(&config.BackoffConfig{InitialDelay: config.Duration(time.Second), MaxDelay: config.Duration(4 * time.Second), Multiplier: 2})
		b.Next()
		b.Next()
		attempt := b.attempt
		time.Sleep(time.Hour) // downtime/cleanup, not process runtime
		b.RecordExit(time.Millisecond)
		if b.attempt != attempt {
			t.Fatal("downtime reset escalation")
		}
		b.RecordExit(5 * time.Second)
		if b.attempt != 0 {
			t.Fatal("stable runtime did not reset")
		}
	})
}
func TestBackoffStatePIDAndCancelledRetryCount(t *testing.T) {
	mgr := NewManager(&config.Config{Run: []config.ProcessConfig{{Name: "retry", Command: "exit 1", AutoRestart: true, Backoff: &config.BackoffConfig{InitialDelay: config.Duration(time.Hour), MaxDelay: config.Duration(time.Hour), Multiplier: 1}}}})
	p := mgr.processes[0]
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- mgr.Run(ctx) }()
	select {
	case <-p.Ready():
	case <-time.After(time.Second):
		cancel()
		t.Fatal("did not start")
	}
	deadline := time.Now().Add(time.Second)
	for {
		info := p.Info()
		if info.Status == StateStarting && info.PID == 0 {
			if info.Restarts != 0 {
				t.Fatal("counted unlaunched retry")
			}
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("stale running state: %+v", info)
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if p.Info().Restarts != 0 {
		t.Fatal("cancelled retry counted")
	}
}
func TestRetryLimitCountsOnlySuccessfulRelaunches(t *testing.T) {
	cfg := config.ProcessConfig{Name: "retry", Command: "exit 1", AutoRestart: true, MaxRetries: 2, Backoff: &config.BackoffConfig{InitialDelay: config.Duration(time.Millisecond), MaxDelay: config.Duration(time.Millisecond), Multiplier: 1}}
	mgr := NewManager(&config.Config{Run: []config.ProcessConfig{cfg}})
	if err := mgr.Run(context.Background()); err == nil {
		t.Fatal("retry exhaustion returned success")
	}
	if got := mgr.Processes()[0].Restarts; got != 2 {
		t.Fatalf("restarts=%d want=2", got)
	}
	cfg.WorkDir = "/gonner-nonexistent-directory"
	mgr = NewManager(&config.Config{Run: []config.ProcessConfig{cfg}})
	if err := mgr.Run(context.Background()); err == nil {
		t.Fatal("failed starts returned success")
	}
	if got := mgr.Processes()[0].Restarts; got != 0 {
		t.Fatalf("failed launches counted as restarts: %d", got)
	}
}
func TestReadinessIncludesPendingAndSkippedCriticalAndUptimeRace(t *testing.T) {
	mgr := NewManager(&config.Config{Run: []config.ProcessConfig{{Name: "critical", Command: "true", Critical: true, Instances: 2, WhenAll: []map[string]string{{"fileExists": "/gonner-missing"}}}}})
	if mgr.Ready() {
		t.Fatal("pending critical was ready")
	}
	if mgr.Uptime() != 0 {
		t.Fatal("pre-run uptime was nonzero")
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				_ = mgr.Uptime()
				_ = mgr.Processes()
				_ = mgr.Ready()
			}
		}
	})
	if err := mgr.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	close(stop)
	wg.Wait()
	procs := mgr.Processes()
	if len(procs) != 1 || procs[0].Status != StateSkipped || procs[0].Instances != 2 {
		t.Fatalf("skipped status=%+v", procs)
	}
	if mgr.Ready() {
		t.Fatal("skipped critical ready")
	}
}
func TestPreflightFinishesBeforeCommandConditions(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	mgr := NewManager(&config.Config{Run: []config.ProcessConfig{{Name: "first", Command: "true", WhenAll: []map[string]string{{"commandSucceeds": "touch '" + marker + "'"}}}, {Name: "unsafe", Command: "true", User: "4294967295", Group: "1"}}})
	if err := mgr.Run(context.Background()); err == nil {
		t.Fatal("unsafe credentials accepted")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("condition ran before preflight completed")
	}
}
func TestCriticalAndNoncriticalPermanentExitsReturnError(t *testing.T) {
	for _, critical := range []bool{false, true} {
		m := NewManager(&config.Config{Run: []config.ProcessConfig{{Name: "failed", Command: "exit 17", Critical: critical}}})
		if err := m.Run(context.Background()); err == nil {
			t.Fatalf("critical=%v exit returned nil", critical)
		}
	}
	m := NewManager(&config.Config{Run: []config.ProcessConfig{{Name: "one-shot", Command: "true", AutoRestart: true}}})
	if err := m.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.Processes()[0].Restarts != 0 {
		t.Fatal("restarted successful one-shot")
	}
}

func TestDependencyRunsWhenInstanceZeroCannotStart(t *testing.T) {
	for _, mode := range []string{"parallel", "sequential"} {
		t.Run(mode, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "dependant")
			m := NewManager(&config.Config{Mode: mode, Run: []config.ProcessConfig{{Name: "multi", Command: "true", Instances: 2}, {Name: "dependent", Command: "touch '" + marker + "'", DependsOn: []string{"multi"}}}})
			m.processes[0].cfg.WorkDir = "/gonner-missing-instance-zero"
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := m.Run(ctx); err == nil {
				t.Fatal("lost instance zero failure")
			}
			if ctx.Err() != nil {
				t.Fatal("hung on instance zero")
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatal("second instance did not release dependant")
			}
		})
	}
}
func TestReadyChannelSurvivesActualRestarts(t *testing.T) {
	m := NewManager(&config.Config{Run: []config.ProcessConfig{{Name: "retry", Command: "false", AutoRestart: true, MaxRetries: 1, Backoff: &config.BackoffConfig{InitialDelay: config.Duration(time.Millisecond), MaxDelay: config.Duration(time.Millisecond), Multiplier: 1}}}})
	p := m.processes[0]
	ready := p.Ready()
	if err := m.Run(context.Background()); err == nil {
		t.Fatal("expected exhaustion")
	}
	if p.Info().Restarts != 1 || p.Ready() != ready {
		t.Fatal("first-start gate changed during restart")
	}
	select {
	case <-ready:
	default:
		t.Fatal("first-start gate not closed")
	}
}
