package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/michaelishri/gonner/internal/config"
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

// Process manages a single process (or one instance of a multi-instance process).
type Process struct {
	cfg             config.ProcessConfig
	instanceID      int
	state           atomic.Value // ProcessState
	logWriter       *logging.Writer
	backoff         *Backoff
	restarts        int
	startedAt       time.Time
	currentCmd      *exec.Cmd
	cmdDone         chan struct{}
	shutdownTimeout time.Duration
	mu              sync.Mutex

	readyCh        chan struct{}
	doneCh         chan struct{}
	onCriticalExit func()
}

// NewProcess creates a new Process instance.
func NewProcess(
	cfg config.ProcessConfig,
	instanceID int,
	shutdownTimeout time.Duration,
	onCriticalExit func(),
) *Process {
	p := &Process{
		cfg:             cfg,
		instanceID:      instanceID,
		backoff:         NewBackoff(cfg.Backoff),
		shutdownTimeout: shutdownTimeout,
		readyCh:         make(chan struct{}),
		doneCh:          make(chan struct{}),
		onCriticalExit:  onCriticalExit,
	}
	p.state.Store(StatePending)
	return p
}

// Name returns the display name for this process (includes instance ID if needed).
func (p *Process) Name() string {
	if p.cfg.Instances > 1 {
		return fmt.Sprintf("%s/%d", p.cfg.Name, p.instanceID)
	}
	return p.cfg.Name
}

// State returns the current state.
func (p *Process) State() ProcessState {
	return p.state.Load().(ProcessState)
}

// Ready returns a channel that is closed when the process is running.
func (p *Process) Ready() <-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.readyCh
}

// Done returns a channel that is closed when the process lifecycle is over.
func (p *Process) Done() <-chan struct{} {
	return p.doneCh
}

// Info returns current runtime info.
func (p *Process) Info() ProcessInfo {
	p.mu.Lock()
	defer p.mu.Unlock()

	info := ProcessInfo{
		Name:     p.Name(),
		Status:   p.State(),
		Restarts: p.restarts,
		Critical: p.cfg.Critical,
	}

	if p.currentCmd != nil && p.currentCmd.Process != nil {
		info.PID = p.currentCmd.Process.Pid
	}

	if !p.startedAt.IsZero() && p.State() == StateRunning {
		info.Uptime = time.Since(p.startedAt).Truncate(time.Second).String()
	}

	return info
}

// Run executes the full process lifecycle: commandsBefore, start, monitor, restart.
// It blocks until the process terminates or the context is cancelled.
func (p *Process) Run(ctx context.Context) error {
	defer logging.Recover(fmt.Sprintf("process[%s]", p.Name()))
	defer close(p.doneCh)
	name := p.Name()

	// Initialize log writer
	logOpts := logging.Options{
		ProcessName: name,
		LogFilePath: p.cfg.LogFile,
		LogFileMode: os.FileMode(p.cfg.LogFileMode),
	}
	if p.cfg.LogRotate != nil {
		logOpts.Rotate = &logging.RotateOptions{
			MaxSizeMB:  p.cfg.LogRotate.MaxSizeMB,
			MaxBackups: p.cfg.LogRotate.MaxBackups,
			Compress:   p.cfg.LogRotate.Compress,
		}
	}
	w, err := logging.NewWriterWithOptions(logOpts)
	if err != nil {
		p.state.Store(StateFailed)
		return fmt.Errorf("creating log writer for %s: %w", name, err)
	}
	p.logWriter = w
	defer p.logWriter.Close()

	// Run commandsBefore
	p.state.Store(StateStarting)
	if err := p.runCommandsBefore(ctx); err != nil {
		p.state.Store(StateFailed)
		logging.Gonner("Process %q failed during commandsBefore: %v", name, err)
		return nil
	}

	for {
		if ctx.Err() != nil {
			p.state.Store(StateStopped)
			return nil
		}

		exitCode, err := p.runCommand(ctx)
		if ctx.Err() != nil {
			p.state.Store(StateStopped)
			logging.Gonner("Process %q stopped (shutdown)", name)
			return nil
		}

		if err != nil {
			logging.Gonner("Process %q exited with error: %v", name, err)
		} else if exitCode == 0 {
			logging.Gonner("Process %q exited normally (code 0)", name)
			p.state.Store(StateStopped)
			return nil
		} else {
			logging.Gonner("Process %q exited with code %d", name, exitCode)
		}

		if p.cfg.Critical {
			p.state.Store(StateFailed)
			logging.Gonner("CRITICAL: Process %q failed — triggering full shutdown", name)
			if p.onCriticalExit != nil {
				p.onCriticalExit()
			}
			return nil
		}

		if !p.cfg.AutoRestart {
			p.state.Store(StateFailed)
			return nil
		}

		p.mu.Lock()
		p.restarts++
		p.mu.Unlock()

		if p.cfg.MaxRetries > 0 && p.restarts > p.cfg.MaxRetries {
			p.state.Store(StateFailed)
			logging.Gonner("Process %q exceeded max retries (%d)", name, p.cfg.MaxRetries)
			return nil
		}

		logging.Gonner("Restarting %q (attempt %d)...", name, p.restarts)
		if !p.backoff.Wait(ctx) {
			p.state.Store(StateStopped)
			return nil
		}
	}
}

// runCommandsBefore executes pre-commands sequentially.
func (p *Process) runCommandsBefore(ctx context.Context) error {
	for i, preCmd := range p.cfg.CommandsBefore {
		logging.Gonner("Running commandsBefore[%d] for %q: %s", i, p.Name(), preCmd.Command)

		workDir := preCmd.WorkDir
		if workDir == "" {
			workDir = p.cfg.WorkDir
		}

		cmd := exec.CommandContext(ctx, "sh", "-c", preCmd.Command)
		if workDir != "" {
			cmd.Dir = workDir
		}
		cmd.Env = p.buildEnv()
		if err := applyCredential(cmd, p.cfg.User, p.cfg.Group); err != nil {
			if preCmd.ContinueOnError {
				logging.Gonner("commandsBefore[%d] for %q credential error (continuing): %v", i, p.Name(), err)
				continue
			}
			return fmt.Errorf("commandsBefore[%d] credential: %w", i, err)
		}

		stdout, _ := cmd.StdoutPipe()
		stderr, _ := cmd.StderrPipe()

		if err := cmd.Start(); err != nil {
			if preCmd.ContinueOnError {
				logging.Gonner("commandsBefore[%d] for %q failed to start (continuing): %v", i, p.Name(), err)
				continue
			}
			return fmt.Errorf("commandsBefore[%d] failed to start: %w", i, err)
		}

		go logging.LineScanner(stdout, p.logWriter)
		go logging.LineScanner(stderr, p.logWriter)

		if err := cmd.Wait(); err != nil {
			if preCmd.ContinueOnError {
				logging.Gonner("commandsBefore[%d] for %q failed (continuing): %v", i, p.Name(), err)
				continue
			}
			return fmt.Errorf("commandsBefore[%d] failed: %w", i, err)
		}

		logging.Gonner("commandsBefore[%d] for %q completed", i, p.Name())
	}
	return nil
}

// runCommand executes the main command once and waits for it to exit.
// Returns the exit code and any error.
func (p *Process) runCommand(ctx context.Context) (int, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", p.cfg.Command)
	if p.cfg.WorkDir != "" {
		cmd.Dir = p.cfg.WorkDir
	}
	cmd.Env = p.buildEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := applyCredential(cmd, p.cfg.User, p.cfg.Group); err != nil {
		return -1, fmt.Errorf("setting credential: %w", err)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return -1, fmt.Errorf("creating stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return -1, fmt.Errorf("creating stderr pipe: %w", err)
	}

	// Refresh ready channel so dependants of this restart can re-arm if needed.
	p.mu.Lock()
	if p.readyCh == nil {
		p.readyCh = make(chan struct{})
	} else {
		select {
		case <-p.readyCh:
			// Was closed by previous start — make a fresh one.
			p.readyCh = make(chan struct{})
		default:
		}
	}
	p.mu.Unlock()

	if err := cmd.Start(); err != nil {
		return -1, fmt.Errorf("starting command: %w", err)
	}

	p.mu.Lock()
	p.currentCmd = cmd
	p.cmdDone = make(chan struct{})
	p.startedAt = time.Now()
	readyCh := p.readyCh
	p.mu.Unlock()

	p.state.Store(StateRunning)
	p.backoff.RecordStart()

	// Signal readiness once.
	select {
	case <-readyCh:
	default:
		close(readyCh)
	}

	logging.Gonner("Process %q started (PID %d)", p.Name(), cmd.Process.Pid)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer logging.Recover("stdout-scanner")
		logging.LineScanner(stdoutPipe, p.logWriter)
	}()
	go func() {
		defer wg.Done()
		defer logging.Recover("stderr-scanner")
		logging.LineScanner(stderrPipe, p.logWriter)
	}()

	waitErr := cmd.Wait()
	wg.Wait()

	_, _ = io.Copy(io.Discard, stdoutPipe)
	_, _ = io.Copy(io.Discard, stderrPipe)

	exitCode := 0
	pid := cmd.Process.Pid

	// PID-1 reaper may have stolen the wait — recover exit status if so.
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else if v, ok := ReapedStatuses.LoadAndDelete(pid); ok {
			ws := v.(reapedStatus).status
			exitCode = ws.ExitStatus()
			if exitCode == 0 {
				waitErr = nil
			}
		} else {
			exitCode = -1
		}
	} else {
		// Clean exit; drop any stale reaper entry.
		ReapedStatuses.Delete(pid)
	}

	p.mu.Lock()
	close(p.cmdDone)
	p.currentCmd = nil
	p.mu.Unlock()

	return exitCode, waitErr
}

// Stop sends the configured stop signal to the process and waits for it to exit.
// If the process doesn't exit within the timeout, SIGKILL is sent.
func (p *Process) Stop() {
	p.mu.Lock()
	cmd := p.currentCmd
	done := p.cmdDone
	p.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return
	}

	p.state.Store(StateStopping)

	stopSig := parseSignal(p.cfg.StopSignal)
	timeout := time.Duration(p.cfg.StopTimeout)
	if timeout <= 0 {
		timeout = p.shutdownTimeout
	}

	if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
		_ = syscall.Kill(-pgid, stopSig)
	} else {
		_ = cmd.Process.Signal(stopSig)
	}

	select {
	case <-done:
	case <-time.After(timeout):
		logging.Gonner("Process %q did not exit within %s, sending SIGKILL", p.Name(), timeout)
		if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		} else {
			_ = cmd.Process.Kill()
		}
	}
}

// ForwardSignal sends a signal to the process group.
func (p *Process) ForwardSignal(sig os.Signal) {
	p.mu.Lock()
	cmd := p.currentCmd
	p.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return
	}

	sysSignal, ok := sig.(syscall.Signal)
	if !ok {
		return
	}

	if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
		_ = syscall.Kill(-pgid, sysSignal)
	} else {
		_ = cmd.Process.Signal(sig)
	}
}

// buildEnv returns the environment for child processes.
// It inherits the current environment and overlays any per-process env vars.
func (p *Process) buildEnv() []string {
	if len(p.cfg.Env) == 0 {
		return nil
	}
	env := os.Environ()
	for k, v := range p.cfg.Env {
		env = append(env, k+"="+v)
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

// applyCredential sets cmd.SysProcAttr.Credential from a user/group name or UID/GID.
// No-op if userSpec is empty. Returns an error if lookup fails or current process is not root.
func applyCredential(cmd *exec.Cmd, userSpec, groupSpec string) error {
	if userSpec == "" {
		return nil
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("dropping privileges to %q requires running as root", userSpec)
	}

	var uid, gid uint64
	u, err := user.Lookup(userSpec)
	if err != nil {
		// Try numeric UID
		n, errN := strconv.ParseUint(userSpec, 10, 32)
		if errN != nil {
			return fmt.Errorf("looking up user %q: %w", userSpec, err)
		}
		uid = n
		gid = n
	} else {
		uid, _ = strconv.ParseUint(u.Uid, 10, 32)
		gid, _ = strconv.ParseUint(u.Gid, 10, 32)
	}

	if groupSpec != "" {
		g, err := user.LookupGroup(groupSpec)
		if err != nil {
			n, errN := strconv.ParseUint(groupSpec, 10, 32)
			if errN != nil {
				return fmt.Errorf("looking up group %q: %w", groupSpec, err)
			}
			gid = n
		} else {
			gid, _ = strconv.ParseUint(g.Gid, 10, 32)
		}
	}

	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Credential = &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}
	return nil
}
