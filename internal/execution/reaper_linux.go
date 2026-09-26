package execution

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func directChildren() ([]int, error) {
	paths, err := filepath.Glob("/proc/self/task/*/children")
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("PID 1 requires /proc/self/task/*/children")
	}
	var pids []int
	read := false
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		read = true
		for _, s := range strings.Fields(string(b)) {
			pid, e := strconv.Atoi(s)
			if e != nil {
				return nil, e
			}
			pids = append(pids, pid)
		}
	}
	if !read {
		return nil, fmt.Errorf("cannot discover PID 1 children")
	}
	return pids, nil
}
func reapOrphans() error {
	children.Lock()
	defer children.Unlock()
	pids, err := directChildren()
	if err != nil {
		return err
	}
	for _, pid := range pids {
		if _, managed := children.managed[pid]; managed {
			continue
		}
		var status syscall.WaitStatus
		_, err := syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
		if err != nil && !errors.Is(err, syscall.ECHILD) && !errors.Is(err, syscall.ESRCH) && !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
	return nil
}

// StartReaper returns a join function. Only adopted, unregistered children are
// reaped; os/exec retains exclusive ownership of every managed child status.
func (s *Service) StartReaper(ctx context.Context) (func(), error) {
	if os.Getpid() != 1 {
		return func() {}, nil
	}
	if _, err := directChildren(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	sig := make(chan os.Signal, 16)
	signal.Notify(sig, syscall.SIGCHLD)
	go func() {
		defer close(done)
		defer signal.Stop(sig)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			if err := reapOrphans(); err != nil {
				s.report(&SupervisorError{err})
				return
			}
			select {
			case <-ctx.Done():
				if err := reapOrphans(); err != nil {
					s.report(&SupervisorError{err})
				}
				return
			case <-sig:
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-done }, nil
}

// Linux zombies must not consume the entire grace period: they cannot receive
// signals or hold pipes. PID 1's reaper will collect them independently.
func groupAlive(pgid int) (bool, error) {
	err := syscall.Kill(-pgid, 0)
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	paths, err := filepath.Glob("/proc/[0-9]*/stat")
	if err != nil || len(paths) == 0 {
		return true, nil
	}
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return true, nil
		}
		end := strings.LastIndexByte(string(b), ')')
		if end < 0 {
			continue
		}
		f := strings.Fields(string(b[end+1:]))
		if len(f) < 3 {
			continue
		}
		g, _ := strconv.Atoi(f[2])
		if g == pgid && f[0] != "Z" && f[0] != "X" {
			return true, nil
		}
	}
	return false, nil
}
