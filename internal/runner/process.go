package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/michaelishri/gonner/internal/config"
	"github.com/michaelishri/gonner/internal/execution"
	"github.com/michaelishri/gonner/internal/logging"
)

// ProcessState represents the current state of a managed process.
type ProcessState string

const (
	StatePending  ProcessState = "pending"
	StateStarting ProcessState = "starting"
	StateRunning  ProcessState = "running"
	StateStopping ProcessState = "stopping"
	StateStopped  ProcessState = "stopped"
	StateFailed   ProcessState = "failed"
	StateSkipped  ProcessState = "skipped"
)

// ProcessInfo holds runtime information about a process.
type ProcessInfo struct {
	Name             string       `json:"name"`
	Status           ProcessState `json:"status"`
	PID              int          `json:"pid,omitempty"`
	Instances        int          `json:"instances"`
	RunningInstances int          `json:"runningInstances"`
	Restarts         int          `json:"restarts"`
	Uptime           string       `json:"uptime,omitempty"`
	Critical         bool         `json:"critical"`
}

// Process owns one configured instance and a stable initial-start gate.
type Process struct {
	cfg                     config.ProcessConfig
	instanceID              int
	state                   atomic.Value
	logWriter               *logging.Writer
	service                 *execution.Service
	credential              *syscall.Credential
	backoff                 *Backoff
	shutdownTimeout         time.Duration
	mu                      sync.Mutex
	restarts, launches, pid int
	startedAt               time.Time
	cancel                  context.CancelFunc
	readyCh, doneCh         chan struct{}
	readyOnce, doneOnce     sync.Once
	onCriticalExit          func()
	generation              string
	previousGeneration      string
	previousReaped          bool
	restartRequested        bool
	generationStop          chan<- time.Duration
}

func NewProcess(cfg config.ProcessConfig, id int, timeout time.Duration, onCriticalExit func()) *Process {
	p := &Process{cfg: cfg, instanceID: id, shutdownTimeout: timeout, onCriticalExit: onCriticalExit, service: execution.NewService(), backoff: NewBackoff(cfg.Backoff), readyCh: make(chan struct{}), doneCh: make(chan struct{})}
	p.service.Diagnostic = logging.Gonner
	p.state.Store(StatePending)
	return p
}
func (p *Process) Name() string {
	if p.cfg.Instances > 1 {
		return fmt.Sprintf("%s/%d", p.cfg.Name, p.instanceID)
	}
	return p.cfg.Name
}
func (p *Process) State() ProcessState { return p.state.Load().(ProcessState) }

// Ready is closed on the first successful launch and never replaced.
func (p *Process) Ready() <-chan struct{} { return p.readyCh }
func (p *Process) Done() <-chan struct{}  { return p.doneCh }
func (p *Process) finish(state ProcessState) {
	p.state.Store(state)
	p.doneOnce.Do(func() { close(p.doneCh) })
}
func (p *Process) Info() ProcessInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	info := ProcessInfo{Name: p.Name(), Status: p.State(), PID: p.pid, Restarts: p.restarts, Critical: p.cfg.Critical}
	if info.Status == StateRunning && !p.startedAt.IsZero() {
		info.Uptime = time.Since(p.startedAt).Truncate(time.Second).String()
	}
	return info
}

// prepare resolves unsafe configuration before any conditions or commands run.
func (p *Process) prepare() error {
	if p.logWriter != nil {
		return nil
	}
	cred, err := execution.ResolveCredential(p.cfg.User, p.cfg.Group)
	if err != nil {
		return err
	}
	p.credential = cred
	opts := logging.Options{ProcessName: p.Name(), LogFilePath: p.cfg.LogFile, LogFileMode: os.FileMode(p.cfg.LogFileMode)}
	if r := p.cfg.LogRotate; r != nil {
		opts.Rotate = &logging.RotateOptions{MaxSizeMB: r.MaxSizeMB, MaxBackups: r.MaxBackups, Compress: r.Compress}
	}
	p.logWriter, err = logging.NewWriterWithOptions(opts)
	return err
}
func (p *Process) spec(command, dir string) execution.Spec {
	timeout := time.Duration(p.cfg.StopTimeout)
	if timeout <= 0 {
		timeout = p.shutdownTimeout
	}
	return execution.Spec{Command: command, Dir: dir, Env: p.buildEnv(), Credential: p.credential, StopSignal: parseSignal(p.cfg.StopSignal), StopTimeout: timeout, Output: func(r io.Reader) error { return logging.LineScanner(r, p.logWriter) }}
}
func (p *Process) fail(err error) error {
	p.state.Store(StateFailed)
	err = fmt.Errorf("process %q: %w", p.Name(), err)
	logging.Gonner("%v", err)
	if p.cfg.Critical && p.onCriticalExit != nil {
		p.onCriticalExit()
	}
	return err
}
func (p *Process) Run(parent context.Context) error {
	defer p.doneOnce.Do(func() { close(p.doneCh) })
	ownedLog := p.logWriter == nil
	defer func() {
		if ownedLog && p.logWriter != nil {
			_ = p.logWriter.Close()
		}
	}()
	if err := p.prepare(); err != nil {
		return p.fail(err)
	}
	// Manager retains log writers until every lifecycle is joined, keeping all
	// configured paths protected from pruning. Standalone callers own theirs.
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	p.mu.Lock()
	p.cancel = cancel
	p.mu.Unlock()
	p.state.Store(StateStarting)
	if err := p.runCommandsBefore(ctx); err != nil {
		var supervisor *execution.SupervisorError
		if (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) && !errors.As(err, &supervisor) {
			p.state.Store(StateStopped)
			return nil
		}
		return p.fail(err)
	}
	retries := 0
	for {
		if ctx.Err() != nil {
			p.state.Store(StateStopped)
			return nil
		}
		p.state.Store(StateStarting)
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return p.fail(err)
		}
		generation := hex.EncodeToString(nonce[:])
		stopRequest := make(chan time.Duration, 1)
		spec := p.spec(p.cfg.Command, p.cfg.WorkDir)
		spec.Env = append(spec.Env, "GONNER_INSTANCE_ID="+p.Name(), "GONNER_GENERATION="+generation)
		spec.StopRequest = stopRequest
		spec.ConfirmReaped = p.cfg.Controllable
		spec.OnStart = func(pid int) {
			p.mu.Lock()
			p.pid = pid
			p.generation = generation
			p.generationStop = stopRequest
			p.restartRequested = false
			p.startedAt = time.Now()
			if p.launches > 0 {
				p.restarts++
			}
			p.launches++
			p.state.Store(StateRunning)
			p.mu.Unlock()
			p.readyOnce.Do(func() { close(p.readyCh) })
		}
		spec.OnExit = func(runtime time.Duration) {
			p.mu.Lock()
			p.pid = 0
			p.state.Store(StateStopping)
			p.mu.Unlock()
			p.backoff.RecordExit(runtime)
		}
		spec.OnStop = p.markStopping
		result := p.service.Run(ctx, spec)
		p.mu.Lock()
		if result.Started {
			p.previousGeneration = generation
			p.previousReaped = result.Reaped
		}
		p.generationStop = nil
		p.state.Store(StateStopped)
		p.mu.Unlock()
		var supervisor *execution.SupervisorError
		if errors.As(result.Err, &supervisor) {
			return p.fail(result.Err)
		}
		if result.Cancelled {
			p.state.Store(StateStopped)
			return nil
		}
		if result.Err == nil && result.ExitCode == 0 && !p.cfg.RestartOnSuccess && !p.requestedRestart() {
			p.state.Store(StateStopped)
			return nil
		}
		err := result.Err
		if err == nil {
			err = fmt.Errorf("exit status %d", result.ExitCode)
		}
		if p.cfg.Critical || !p.cfg.AutoRestart || (p.cfg.MaxRetries > 0 && retries >= p.cfg.MaxRetries) {
			return p.fail(err)
		}
		p.state.Store(StateStarting)
		logging.Gonner("Restarting %q (retry %d)", p.Name(), retries+1)
		if !p.backoff.Wait(ctx) {
			p.state.Store(StateStopped)
			return nil
		}
		// The retry budget counts extra launch attempts; public Restarts counts only
		// successful launches after the first successful launch, in OnStart.
		retries++
	}
}
func (p *Process) markStopping() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pid != 0 {
		p.state.Store(StateStopping)
	}
}
func (p *Process) runCommandsBefore(ctx context.Context) error {
	for i, pre := range p.cfg.CommandsBefore {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		dir := pre.WorkDir
		if dir == "" {
			dir = p.cfg.WorkDir
		}
		spec := p.spec(pre.Command, dir)
		spec.OnStart = func(pid int) { p.mu.Lock(); p.pid = pid; p.mu.Unlock() }
		spec.OnExit = func(time.Duration) { p.mu.Lock(); p.pid = 0; p.state.Store(StateStarting); p.mu.Unlock() }
		spec.OnStop = p.markStopping
		r := p.service.Run(ctx, spec)
		if r.Err != nil {
			var supervisor *execution.SupervisorError
			if r.Cancelled || errors.As(r.Err, &supervisor) || !pre.ContinueOnError {
				return fmt.Errorf("commandsBefore[%d]: %w", i, r.Err)
			}
			logging.Gonner("commandsBefore[%d] for %q failed (continuing): %v", i, p.Name(), r.Err)
		}
	}
	return nil
}
func (p *Process) Stop() {
	p.mu.Lock()
	cancel := p.cancel
	p.mu.Unlock()
	if cancel != nil {
		cancel()
		<-p.doneCh
	}
}
func (p *Process) ForwardSignal(sig os.Signal) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := sig.(syscall.Signal)
	if !ok {
		return nil
	}
	return execution.SignalGroup(p.pid, s)
}
func (p *Process) buildEnv() []string {
	env := []string{}
	if !p.cfg.ClearEnv {
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "GONNER_INSTANCE_ID=") && !strings.HasPrefix(entry, "GONNER_GENERATION=") {
				env = append(env, entry)
			}
		}
	}
	for k, v := range p.cfg.Env {
		if k != "GONNER_INSTANCE_ID" && k != "GONNER_GENERATION" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// parseSignal converts a signal name to syscall.Signal. Defaults to SIGTERM.
func parseSignal(name string) syscall.Signal {
	switch strings.ToUpper(name) {
	case "", "SIGTERM":
		return syscall.SIGTERM
	case "SIGINT":
		return syscall.SIGINT
	case "SIGHUP":
		return syscall.SIGHUP
	case "SIGQUIT":
		return syscall.SIGQUIT
	case "SIGUSR1":
		return syscall.SIGUSR1
	case "SIGUSR2":
		return syscall.SIGUSR2
	case "SIGKILL":
		return syscall.SIGKILL
	default:
		return syscall.SIGTERM
	}
}
