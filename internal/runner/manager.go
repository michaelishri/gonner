package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/michaelishri/gonner/internal/condition"
	"github.com/michaelishri/gonner/internal/config"
	"github.com/michaelishri/gonner/internal/execution"
	"github.com/michaelishri/gonner/internal/logging"
)

type Manager struct {
	cfg          *config.Config
	processes    []*Process
	mu           sync.RWMutex
	startedAt    time.Time
	shuttingDown atomic.Bool
	service      *execution.Service
}

func NewManager(cfg *config.Config) *Manager {
	copyCfg := *cfg
	copyCfg.Run = append([]config.ProcessConfig(nil), cfg.Run...)
	copyCfg.ApplyDefaults()
	m := &Manager{cfg: &copyCfg, service: execution.NewService()}
	m.service.Diagnostic = logging.Gonner
	for _, c := range m.cfg.Run {
		for i := 0; i < c.Instances; i++ {
			p := NewProcess(c, i, time.Duration(m.cfg.ShutdownTimeout), nil)
			p.service = m.service
			m.processes = append(m.processes, p)
		}
	}
	return m
}

// waitStarted succeeds if ANY instance has ever started. It becomes unavailable
// only when ALL instances terminate before starting; it never polls live state.
func waitStarted(ctx context.Context, procs []*Process) bool {
	cases := []reflect.SelectCase{{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(ctx.Done())}}
	for _, p := range procs {
		cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(p.Ready())}, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(p.Done())})
	}
	remaining := len(procs)
	for remaining > 0 {
		// Prefer already-successful starts even when Done is also closed.
		for _, p := range procs {
			select {
			case <-p.Ready():
				return true
			default:
			}
		}
		chosen, _, _ := reflect.Select(cases)
		if chosen == 0 {
			return false
		}
		if chosen%2 == 1 {
			return true
		}
		cases[chosen].Chan = reflect.Value{}
		remaining--
	}
	for _, p := range procs {
		select {
		case <-p.Ready():
			return true
		default:
		}
	}
	return false
}
func (m *Manager) Run(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	m.mu.Lock()
	m.startedAt = time.Now()
	m.mu.Unlock()
	defer func() {
		m.shuttingDown.Store(true)
		for _, p := range m.processes {
			select {
			case <-p.Done():
			default:
				p.finish(StateStopped)
			}
			if p.logWriter != nil {
				_ = p.logWriter.Close()
			}
		}
	}()
	if err := config.Validate(m.cfg); err != nil {
		return err
	}
	// All paths and credentials are checked before even command conditions.
	for _, p := range m.processes {
		if err := p.prepare(); err != nil {
			p.finish(StateFailed)
			return fmt.Errorf("preflight %q: %w", p.Name(), err)
		}
	}
	// Keep the orphan reaper alive through cancellation and child cleanup.
	joinReaper, err := m.service.StartReaper(context.Background())
	if err != nil {
		return err
	}
	defer joinReaper()
	var errorsMu sync.Mutex
	var failures []error
	record := func(err error) {
		if err != nil {
			errorsMu.Lock()
			failures = append(failures, err)
			errorsMu.Unlock()
		}
	}
	watchDone := make(chan struct{})
	watchExit := make(chan struct{})
	go func() {
		defer close(watchExit)
		select {
		case err := <-m.service.Errors:
			record(err)
			cancel()
		case <-watchDone:
		}
	}()
	var stopMonitorOnce sync.Once
	stopMonitor := func() { stopMonitorOnce.Do(func() { close(watchDone); <-watchExit }) }
	defer stopMonitor()
	shutdownWatch := context.AfterFunc(ctx, func() { m.shuttingDown.Store(true) })
	defer shutdownWatch()
	groups := make(map[string][]*Process)
	for _, p := range m.processes {
		groups[p.cfg.Name] = append(groups[p.cfg.Name], p)
		p.onCriticalExit = cancel
	}
	for _, c := range m.cfg.Run {
		if ctx.Err() != nil {
			break
		}
		p := groups[c.Name][0]
		spec := p.spec("", c.WorkDir)
		spec.Output = nil
		ok, reason, err := condition.NewEvaluator(m.service, spec).ShouldRun(ctx, c.WhenAll, c.WhenAny)
		if err != nil {
			if ctx.Err() == nil {
				record(fmt.Errorf("conditions for %q: %w", c.Name, err))
			}
			cancel()
			break
		}
		if !ok {
			logging.Gonner("Skipping %q: %s", c.Name, reason)
			for _, p := range groups[c.Name] {
				p.finish(StateSkipped)
			}
		}
	}
	var wg sync.WaitGroup
	launch := func(p *Process) {
		select {
		case <-p.Done():
			return
		default:
		}
		wg.Go(func() {
			for _, dep := range p.cfg.DependsOn {
				if !waitStarted(ctx, groups[dep]) {
					if ctx.Err() != nil {
						p.finish(StateStopped)
						return
					}
					err := fmt.Errorf("process %q: dependency %q never started", p.Name(), dep)
					if p.cfg.Critical {
						p.finish(StateFailed)
						record(err)
						cancel()
					} else {
						p.finish(StateSkipped)
						record(err)
					}
					return
				}
			}
			if ctx.Err() != nil {
				p.finish(StateStopped)
				return
			}
			err := p.Run(ctx)
			record(err)
			var supervisor *execution.SupervisorError
			if errors.As(err, &supervisor) {
				cancel()
			}
		})
	}
	if m.cfg.Mode == "sequential" {
		for _, c := range m.cfg.Run {
			if ctx.Err() != nil {
				break
			}
			for _, p := range groups[c.Name] {
				launch(p)
			}
			_ = waitStarted(ctx, groups[c.Name])
		}
	} else {
		for _, p := range m.processes {
			launch(p)
		}
	}
	wg.Wait()
	joinReaper()
	stopMonitor()
	// Join the error monitor before reading failures, without closing its channel
	// twice. Drain any final infrastructure error before producing exit status.
	select {
	case err := <-m.service.Errors:
		record(err)
	default:
	}
	errorsMu.Lock()
	defer errorsMu.Unlock()
	return errors.Join(failures...)
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
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.startedAt.IsZero() {
		return 0
	}
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

// ForwardSignal forwards operational signals and reports failures to Run.
func (m *Manager) ForwardSignal(sig os.Signal) {
	for _, p := range m.processes {
		if err := p.ForwardSignal(sig); err != nil {
			m.service.ReportSignalError(err)
		}
	}
}
