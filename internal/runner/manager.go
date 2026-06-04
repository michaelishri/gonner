package runner

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/michaelishri/gonner/internal/condition"
	"github.com/michaelishri/gonner/internal/config"
	"github.com/michaelishri/gonner/internal/logging"
)

// Manager orchestrates all managed processes.
type Manager struct {
	cfg          *config.Config
	processes    []*Process
	mu           sync.RWMutex
	startedAt    time.Time
	cancelFn     context.CancelFunc
	shuttingDown atomic.Bool
}

// NewManager creates a new process manager from a validated config.
func NewManager(cfg *config.Config) *Manager {
	return &Manager{
		cfg: cfg,
	}
}

// Run starts all processes and blocks until all have exited or the context is cancelled.
func (m *Manager) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	m.cancelFn = cancel
	defer func() {
		m.shuttingDown.Store(true)
		cancel()
	}()

	m.startedAt = time.Now()
	shutdownTimeout := time.Duration(m.cfg.ShutdownTimeout)

	// Start zombie reaper (no-op if not PID 1)
	reaperDone := make(chan struct{})
	StartZombieReaper(reaperDone)
	defer close(reaperDone)

	// Evaluate conditions and filter runnable processes
	var runnableConfigs []config.ProcessConfig
	for _, procCfg := range m.cfg.Run {
		shouldRun, reason, err := condition.ShouldRun(procCfg.WhenAll, procCfg.WhenAny)
		if err != nil {
			return fmt.Errorf("evaluating conditions for %q: %w", procCfg.Name, err)
		}
		if !shouldRun {
			logging.Gonner("Skipping %q: condition not met (%s)", procCfg.Name, reason)
			continue
		}
		runnableConfigs = append(runnableConfigs, procCfg)
	}

	if len(runnableConfigs) == 0 {
		logging.Gonner("No processes to run after condition evaluation")
		return nil
	}

	// Build process instances
	// readyMap tracks which process name -> ready channels for dependsOn resolution
	readyMap := make(map[string][]*Process)

	for _, procCfg := range runnableConfigs {
		for i := 0; i < procCfg.Instances; i++ {
			proc := NewProcess(procCfg, i, shutdownTimeout, cancel)
			m.mu.Lock()
			m.processes = append(m.processes, proc)
			m.mu.Unlock()
			readyMap[procCfg.Name] = append(readyMap[procCfg.Name], proc)
		}
	}

	logging.Gonner("Starting %d process(es) in %s mode", len(m.processes), m.cfg.Mode)

	g, gCtx := errgroup.WithContext(ctx)

	if m.cfg.Mode == "sequential" {
		// Sequential: start each process config one at a time
		for _, procCfg := range runnableConfigs {
			procCfg := procCfg // capture for closure
			procs := readyMap[procCfg.Name]

			// Start all instances of this process
			for _, proc := range procs {
				proc := proc
				g.Go(func() error {
					defer logging.Recover(fmt.Sprintf("runner[%s]", proc.Name()))
					return proc.Run(gCtx)
				})
			}

			// Wait for at least one instance to be ready (or exit) before moving to next
			if len(procs) > 0 {
				select {
				case <-procs[0].Ready():
				case <-procs[0].Done():
					// Process exited before becoming ready; continue to next
				case <-gCtx.Done():
					return gCtx.Err()
				}
			}
		}
	} else {
		// Parallel: start all processes with dependsOn coordination
		for _, proc := range m.processes {
			proc := proc
			g.Go(func() error {
				defer logging.Recover(fmt.Sprintf("runner[%s]", proc.Name()))
				// Wait for dependencies
				for _, depName := range proc.cfg.DependsOn {
					depProcs, ok := readyMap[depName]
					if !ok {
						// Dependency was skipped by conditions — treat as unavailable
						logging.Gonner("Dependency %q for %q was skipped — skipping %q", depName, proc.Name(), proc.Name())
						proc.state.Store(StateSkipped)
						return nil
					}
					// Wait for at least one instance of the dependency to be ready
					select {
					case <-depProcs[0].Ready():
					case <-gCtx.Done():
						return nil
					}
				}
				return proc.Run(gCtx)
			})
		}
	}

	// Wait for all goroutines
	err := g.Wait()

	// Mark as shutting down before stopping remaining processes
	m.shuttingDown.Store(true)

	// Gracefully stop any still-running processes
	m.shutdownAll()

	logging.Gonner("All processes have exited")
	return err
}

// shutdownAll sends SIGTERM to all running processes and waits for them to exit.
func (m *Manager) shutdownAll() {
	m.mu.RLock()
	procs := make([]*Process, len(m.processes))
	copy(procs, m.processes)
	m.mu.RUnlock()

	var wg sync.WaitGroup
	for _, proc := range procs {
		if proc.State() == StateRunning || proc.State() == StateStarting {
			wg.Add(1)
			go func(p *Process) {
				defer wg.Done()
				p.Stop()
			}(proc)
		}
	}
	wg.Wait()
}

// Processes returns info about all managed processes.
func (m *Manager) Processes() []ProcessInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Aggregate by process name
	type aggregate struct {
		name             string
		instances        int
		runningInstances int
		restarts         int
		critical         bool
		status           ProcessState
		uptime           string
		pid              int
	}

	agg := make(map[string]*aggregate)
	var order []string

	for _, proc := range m.processes {
		info := proc.Info()
		baseName := proc.cfg.Name

		a, exists := agg[baseName]
		if !exists {
			a = &aggregate{
				name:     baseName,
				critical: proc.cfg.Critical,
				status:   info.Status,
			}
			agg[baseName] = a
			order = append(order, baseName)
		}

		a.instances++
		a.restarts += info.Restarts
		if info.Status == StateRunning {
			a.runningInstances++
			a.pid = info.PID
			a.uptime = info.Uptime
		}

		// Aggregate status: running if any running, failed if any failed, etc.
		if info.Status == StateRunning {
			a.status = StateRunning
		} else if a.status != StateRunning && info.Status == StateFailed {
			a.status = StateFailed
		}
	}

	var result []ProcessInfo
	for _, name := range order {
		a := agg[name]
		result = append(result, ProcessInfo{
			Name:             a.name,
			Status:           a.status,
			PID:              a.pid,
			Instances:        a.instances,
			RunningInstances: a.runningInstances,
			Restarts:         a.restarts,
			Uptime:           a.uptime,
			Critical:         a.critical,
		})
	}

	return result
}

// Uptime returns how long the manager has been running.
func (m *Manager) Uptime() time.Duration {
	return time.Since(m.startedAt)
}

// Mode returns the startup mode from the config.
func (m *Manager) Mode() string {
	return m.cfg.Mode
}

// IsShuttingDown returns true if the manager's context has been cancelled.
func (m *Manager) IsShuttingDown() bool {
	return m.shuttingDown.Load()
}

// Ready reports whether gonner is ready to serve traffic. It returns true when
// the manager is not shutting down and every critical process has at least one
// running instance. If no critical processes are defined, readiness only
// requires that gonner is not shutting down.
//
// This is distinct from liveness (IsShuttingDown): a container can be alive but
// not yet ready while critical dependencies are still starting. Use /ready as a
// Kubernetes readiness probe and /health as a liveness probe.
func (m *Manager) Ready() bool {
	if m.shuttingDown.Load() {
		return false
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	criticalRunning := make(map[string]bool)
	criticalSeen := make(map[string]bool)
	for _, proc := range m.processes {
		if !proc.cfg.Critical {
			continue
		}
		criticalSeen[proc.cfg.Name] = true
		if proc.State() == StateRunning {
			criticalRunning[proc.cfg.Name] = true
		}
	}

	for name := range criticalSeen {
		if !criticalRunning[name] {
			return false
		}
	}
	return true
}

// ForwardSignal sends a signal to all running processes.
func (m *Manager) ForwardSignal(sig os.Signal) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, proc := range m.processes {
		if proc.State() == StateRunning {
			proc.ForwardSignal(sig)
		}
	}
}
