package runner

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/michaelishri/gonner/internal/logging"
)

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
	s := &SignalHandler{
		sigCh:     make(chan os.Signal, 4),
		cancelFn:  cancelFn,
		forwardFn: forwardFn,
		done:      make(chan struct{}),
	}
	signal.Notify(s.sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGUSR1, syscall.SIGUSR2)
	return s
}

// Start begins listening for signals. Call this in a goroutine.
// SIGTERM/SIGINT trigger shutdown; SIGHUP/SIGUSR1/SIGUSR2 are forwarded to children.
func (s *SignalHandler) Start() {
	defer logging.Recover("signal-handler")

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
