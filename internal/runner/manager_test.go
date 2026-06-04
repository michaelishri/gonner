package runner

import (
	"context"
	"testing"
	"time"

	"github.com/michaelishri/gonner/internal/config"
)

func TestManager_ParallelMode(t *testing.T) {
	cfg := &config.Config{
		Mode:            "parallel",
		ShutdownTimeout: config.Duration(5 * time.Second),
		Run: []config.ProcessConfig{
			{Name: "echo1", Command: "echo hello1", Instances: 1},
			{Name: "echo2", Command: "echo hello2", Instances: 1},
		},
	}

	mgr := NewManager(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := mgr.Run(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	procs := mgr.Processes()
	if len(procs) != 2 {
		t.Errorf("expected 2 processes, got %d", len(procs))
	}
}

func TestManager_SequentialMode(t *testing.T) {
	cfg := &config.Config{
		Mode:            "sequential",
		ShutdownTimeout: config.Duration(5 * time.Second),
		Run: []config.ProcessConfig{
			{Name: "first", Command: "echo first", Instances: 1},
			{Name: "second", Command: "echo second", Instances: 1},
		},
	}

	mgr := NewManager(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := mgr.Run(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestManager_CriticalProcessShutdown(t *testing.T) {
	cfg := &config.Config{
		Mode:            "parallel",
		ShutdownTimeout: config.Duration(5 * time.Second),
		Run: []config.ProcessConfig{
			{Name: "background", Command: "sleep 30", Instances: 1},
			{Name: "critical", Command: "exit 1", Instances: 1, Critical: true},
		},
	}

	mgr := NewManager(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	start := time.Now()
	_ = mgr.Run(ctx)
	elapsed := time.Since(start)

	// Should complete quickly because critical process fails immediately
	if elapsed > 10*time.Second {
		t.Errorf("expected quick shutdown from critical failure, took %v", elapsed)
	}
}

func TestManager_MultipleInstances(t *testing.T) {
	cfg := &config.Config{
		Mode:            "parallel",
		ShutdownTimeout: config.Duration(5 * time.Second),
		Run: []config.ProcessConfig{
			{Name: "worker", Command: "echo working", Instances: 3},
		},
	}

	mgr := NewManager(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := mgr.Run(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestManager_DependsOn(t *testing.T) {
	cfg := &config.Config{
		Mode:            "parallel",
		ShutdownTimeout: config.Duration(5 * time.Second),
		Run: []config.ProcessConfig{
			{Name: "db", Command: "echo db-ready", Instances: 1},
			{Name: "app", Command: "echo app-started", Instances: 1, DependsOn: []string{"db"}},
		},
	}

	mgr := NewManager(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := mgr.Run(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestManager_NoRunnableProcesses(t *testing.T) {
	cfg := &config.Config{
		Mode:            "parallel",
		ShutdownTimeout: config.Duration(5 * time.Second),
		Run: []config.ProcessConfig{
			{
				Name:      "conditional",
				Command:   "echo hi",
				Instances: 1,
				WhenAll:   []map[string]string{{"env": "NONEXISTENT_VAR_FOR_TEST=yes"}},
			},
		},
	}

	mgr := NewManager(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := mgr.Run(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestManager_CommandsBefore(t *testing.T) {
	cfg := &config.Config{
		Mode:            "parallel",
		ShutdownTimeout: config.Duration(5 * time.Second),
		Run: []config.ProcessConfig{
			{
				Name:      "app",
				Command:   "echo main",
				Instances: 1,
				CommandsBefore: []config.PreCommand{
					{Command: "echo setup"},
				},
			},
		},
	}

	mgr := NewManager(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := mgr.Run(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestManager_ModeAndUptime(t *testing.T) {
	cfg := &config.Config{
		Mode:            "parallel",
		ShutdownTimeout: config.Duration(5 * time.Second),
		Run: []config.ProcessConfig{
			{Name: "quick", Command: "echo done", Instances: 1},
		},
	}

	mgr := NewManager(cfg)

	if mgr.Mode() != "parallel" {
		t.Errorf("Mode(): got %q, want %q", mgr.Mode(), "parallel")
	}
	if mgr.IsShuttingDown() {
		t.Error("should not be shutting down before Run")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_ = mgr.Run(ctx)

	if !mgr.IsShuttingDown() {
		t.Error("should be shutting down after Run completes")
	}
	if mgr.Uptime() <= 0 {
		t.Error("uptime should be positive after Run")
	}
}

func TestManager_ContextCancellation(t *testing.T) {
	cfg := &config.Config{
		Mode:            "parallel",
		ShutdownTimeout: config.Duration(2 * time.Second),
		Run: []config.ProcessConfig{
			{Name: "sleeper", Command: "sleep 60", Instances: 1},
		},
	}

	mgr := NewManager(cfg)
	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after a short time
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_ = mgr.Run(ctx)
	elapsed := time.Since(start)

	// Should exit within shutdown timeout + some margin
	if elapsed > 5*time.Second {
		t.Errorf("expected quick exit after cancel, took %v", elapsed)
	}
}
