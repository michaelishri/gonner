package runner

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/michaelishri/gonner/internal/logging"
)

// ReapedStatuses records exit statuses for processes reaped by the zombie reaper
// before exec.Cmd.Wait could call wait4 itself. Indexed by PID.
// Process.runCommand consults this map when cmd.Wait returns ECHILD.
//
// Entries are reaped statuses for *all* descendants of gonner (PID 1 reaps
// orphaned grandchildren too). Only direct children that gonner waits on ever
// consume an entry, so untracked PIDs are pruned by age to bound memory use.
var ReapedStatuses sync.Map // map[int]reapedStatus

// reapedStatusTTL is how long an unclaimed reaped status is retained before the
// reaper prunes it. Direct children are claimed almost immediately by
// Process.runCommand; anything older is an orphaned descendant nobody waits on.
const reapedStatusTTL = 30 * time.Second

// reapedStatus pairs a wait status with the time it was recorded so stale,
// unclaimed entries (orphaned descendants) can be pruned.
type reapedStatus struct {
	status   syscall.WaitStatus
	reapedAt time.Time
}

// SignalHandler manages signal trapping and forwarding for gonner.
type SignalHandler struct {
	sigCh     chan os.Signal
	cancelFn  func()
	forwardFn func(os.Signal)
	done      chan struct{}
}

// NewSignalHandler creates a signal handler that calls cancelFn on SIGTERM/SIGINT
// and forwards SIGHUP/SIGUSR1/SIGUSR2 to child processes via forwardFn.
func NewSignalHandler(cancelFn func(), forwardFn func(os.Signal)) *SignalHandler {
	return &SignalHandler{
		sigCh:     make(chan os.Signal, 4),
		cancelFn:  cancelFn,
		forwardFn: forwardFn,
		done:      make(chan struct{}),
	}
}

// Start begins listening for signals. Call this in a goroutine.
// SIGTERM/SIGINT trigger shutdown; SIGHUP/SIGUSR1/SIGUSR2 are forwarded to children.
func (s *SignalHandler) Start() {
	defer logging.Recover("signal-handler")
	signal.Notify(s.sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGUSR1, syscall.SIGUSR2)

	for {
		select {
		case sig := <-s.sigCh:
			switch sig {
			case syscall.SIGTERM, syscall.SIGINT:
				logging.Gonner("Received signal %s, initiating shutdown...", sig)
				s.cancelFn()
				return
			default:
				logging.Gonner("Forwarding signal %s to child processes", sig)
				if s.forwardFn != nil {
					s.forwardFn(sig)
				}
			}
		case <-s.done:
			return
		}
	}
}

// Stop unregisters signal handlers and stops the handler goroutine.
func (s *SignalHandler) Stop() {
	signal.Stop(s.sigCh)
	select {
	case <-s.done:
	default:
		close(s.done)
	}
}

// StartZombieReaper starts a goroutine that reaps zombie child processes.
// Only active when gonner is running as PID 1 (typical in a container).
//
// To avoid racing with os/exec's own wait4(pid) calls, the reaper records each
// reaped child's exit status in ReapedStatuses; Process.runCommand falls back
// to that map when cmd.Wait returns ECHILD ("wait: no child processes").
//
// The goroutine runs until the done channel is closed.
func StartZombieReaper(done <-chan struct{}) {
	if os.Getpid() != 1 {
		logging.Gonner("Not PID 1 (pid=%d), skipping zombie reaper", os.Getpid())
		return
	}

	logging.Gonner("Running as PID 1, starting zombie reaper")

	sigCh := make(chan os.Signal, 16)
	signal.Notify(sigCh, syscall.SIGCHLD)

	go func() {
		defer logging.Recover("zombie-reaper")
		defer signal.Stop(sigCh)

		for {
			select {
			case <-done:
				return
			case <-sigCh:
				// Drain all currently-exited children.
				for {
					var ws syscall.WaitStatus
					pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
					if err != nil || pid <= 0 {
						break
					}
					ReapedStatuses.Store(pid, reapedStatus{status: ws, reapedAt: time.Now()})
					logging.Gonner("Reaped child PID %d (exit %d)", pid, ws.ExitStatus())
				}
				// Prune unclaimed statuses from orphaned descendants that no
				// goroutine will ever wait on, bounding memory over time.
				pruneReapedStatuses()
			}
		}
	}()
}

// pruneReapedStatuses removes unclaimed reaped statuses older than reapedStatusTTL.
// These accumulate when gonner (as PID 1) reaps orphaned descendants that none of
// its own goroutines wait on. Without pruning, the map would grow unbounded over
// the lifetime of a long-running container.
func pruneReapedStatuses() {
	cutoff := time.Now().Add(-reapedStatusTTL)
	ReapedStatuses.Range(func(key, value any) bool {
		if rs, ok := value.(reapedStatus); ok && rs.reapedAt.Before(cutoff) {
			ReapedStatuses.Delete(key)
		}
		return true
	})
}
