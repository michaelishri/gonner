// Package execution owns child creation, waiting, group shutdown and pipe FDs.
// It has no dependency on configuration, conditions or logging.
package execution

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// The registry spans services so concurrent managers/probes cannot reap one
// another's children. Start and registration share the reaper's lock.
var children = struct {
	sync.Mutex
	managed map[int]*exec.Cmd
}{managed: make(map[int]*exec.Cmd)}

type SupervisorError struct{ Err error }

func (e *SupervisorError) Error() string { return "supervisor: " + e.Err.Error() }
func (e *SupervisorError) Unwrap() error { return e.Err }

type Service struct {
	Diagnostic func(string, ...any)
	Errors     chan error
}

func NewService() *Service { return &Service{Errors: make(chan error, 1)} }
func (s *Service) diagnostic(f string, a ...any) {
	if s.Diagnostic != nil {
		s.Diagnostic(f, a...)
	}
}
func (s *Service) report(err error) {
	select {
	case s.Errors <- err:
	default:
	}
}

type Spec struct {
	Command, Dir string
	Env          []string
	Credential   *syscall.Credential
	StopSignal   syscall.Signal
	StopTimeout  time.Duration
	// Output must drain the reader even if an output destination fails.
	Output  func(io.Reader) error
	OnStart func(int)
	OnExit  func(time.Duration)
	OnStop  func()
}
type Result struct {
	Started   bool
	Cancelled bool
	ExitCode  int
	Runtime   time.Duration
	Err       error
}

// Run calls exec.Cmd.Wait exactly once. The explicit pipe ownership ensures
// Wait never closes a reader while output is still being drained.
func (s *Service) Run(ctx context.Context, spec Spec) Result {
	r := Result{ExitCode: -1}
	if err := ctx.Err(); err != nil {
		r.Err = err
		r.Cancelled = true
		return r
	}
	if spec.StopSignal == 0 {
		spec.StopSignal = syscall.SIGTERM
	}
	if spec.StopTimeout <= 0 {
		spec.StopTimeout = 30 * time.Second
	}
	cmd := exec.Command("sh", "-c", spec.Command)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Credential: spec.Credential}
	outR, outW, err := os.Pipe()
	if err != nil {
		r.Err = err
		return r
	}
	defer outR.Close()
	defer outW.Close()
	errR, errW, err := os.Pipe()
	if err != nil {
		r.Err = err
		return r
	}
	defer errR.Close()
	defer errW.Close()
	cmd.Stdout = outW
	cmd.Stderr = errW
	children.Lock()
	if ctx.Err() != nil {
		children.Unlock()
		r.Err = ctx.Err()
		r.Cancelled = true
		return r
	}
	err = cmd.Start()
	if err == nil {
		children.managed[cmd.Process.Pid] = cmd
	}
	children.Unlock()
	if err != nil {
		r.Err = fmt.Errorf("starting command: %w", err)
		return r
	}
	_ = outW.Close()
	_ = errW.Close()
	r.Started = true
	start := time.Now()
	pid := cmd.Process.Pid
	if spec.OnStart != nil {
		spec.OnStart(pid)
	}
	var drains sync.WaitGroup
	var outputErr [2]error
	for i, reader := range []*os.File{outR, errR} {
		drains.Go(func() {
			if spec.Output != nil {
				outputErr[i] = spec.Output(reader)
			} else {
				_, outputErr[i] = io.Copy(io.Discard, reader)
			}
		})
	}
	drainDone := make(chan struct{})
	go func() { drains.Wait(); close(drainDone) }()
	var stopOnce sync.Once
	var stopErr error
	stop := func() {
		stopOnce.Do(func() {
			if spec.OnStop != nil {
				spec.OnStop()
			}
			stopErr = stopGroup(pid, spec.StopSignal, spec.StopTimeout)
			if stopErr != nil {
				s.report(&SupervisorError{stopErr})
			}
		})
	}
	watchDone := make(chan struct{})
	watchExited := make(chan struct{})
	shutdownStarted := make(chan time.Time, 1)
	go func() {
		defer close(watchExited)
		select {
		case <-ctx.Done():
			shutdownStarted <- time.Now()
			stop()
		case <-watchDone:
		}
	}()
	waitErr := cmd.Wait()
	r.Cancelled = ctx.Err() != nil
	r.Runtime = time.Since(start)
	// Clear externally visible running/PID immediately, before group cleanup,
	// output draining or backoff. Runtime excludes all of those activities.
	if spec.OnExit != nil {
		spec.OnExit(r.Runtime)
	}
	children.Lock()
	if children.managed[pid] == cmd {
		delete(children.managed, pid)
	}
	children.Unlock()
	if cmd.ProcessState != nil {
		r.ExitCode = cmd.ProcessState.ExitCode()
	}
	stop()
	close(watchDone)
	<-watchExited
	select {
	case <-drainDone:
	case <-ctx.Done():
		// Cancellation bounds even escaped descendants retaining a pipe. Ordinary
		// completion always drains to EOF. Report any forced truncation explicitly.
		started := time.Now()
		select {
		case started = <-shutdownStarted:
		default:
		}
		remaining := time.Until(started.Add(spec.StopTimeout))
		timer := time.NewTimer(remaining)
		select {
		case <-drainDone:
		case <-timer.C:
			s.diagnostic("Output drain exceeded shutdown deadline for PID %d; closing pipes (output may be truncated)", pid)
			_ = outR.Close()
			_ = errR.Close()
			<-drainDone
		}
		timer.Stop()
	}
	r.Err = waitErr
	if r.Cancelled {
		r.Err = ctx.Err()
	}
	if stopErr != nil {
		r.Err = errors.Join(r.Err, &SupervisorError{stopErr})
	}
	for _, err := range outputErr {
		if err != nil && !errors.Is(err, os.ErrClosed) {
			r.Err = errors.Join(r.Err, &SupervisorError{fmt.Errorf("draining child output: %w", err)})
		}
	}
	return r
}
func stopGroup(pid int, sig syscall.Signal, timeout time.Duration) error {
	if err := SignalGroup(pid, sig); err != nil {
		return err
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		alive, err := groupAlive(pid)
		if err != nil {
			return err
		}
		if !alive {
			return nil
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			return SignalGroup(pid, syscall.SIGKILL)
		}
	}
}
func SignalGroup(pid int, sig syscall.Signal) error {
	if pid <= 0 {
		return nil
	}
	err := syscall.Kill(-pid, sig)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// ReportSignalError promotes a signal failure to a supervisor failure.
func (s *Service) ReportSignalError(err error) { s.report(&SupervisorError{err}) }
