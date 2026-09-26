package execution

import (
	"context"
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	darwinZombie  = 5      // SZOMB in <sys/proc.h>.
	darwinExiting = 0x2000 // P_WEXIT in <sys/proc.h>.
)

func (s *Service) StartReaper(ctx context.Context) (func(), error) { return func() {}, nil }
func groupAlive(pid int) (bool, error) {
	exited, err := groupSignalResult(pid, syscall.Kill(-pid, 0))
	return !exited && err == nil, err
}

// Darwin's killpg can return EPERM for zombies and processes committed to exit.
// Confirm that all members are exiting before suppressing that error;
// permission failures for live members must still reach the supervisor.
func groupSignalResult(pid int, err error) (exited bool, signalErr error) {
	if errors.Is(err, syscall.ESRCH) {
		return true, nil
	}
	if errors.Is(err, syscall.EPERM) {
		procs, inspectErr := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pid)
		if inspectErr != nil {
			return false, errors.Join(err, fmt.Errorf("inspecting process group %d: %w", pid, inspectErr))
		}
		for _, proc := range procs {
			if proc.Proc.P_stat != darwinZombie && proc.Proc.P_flag&darwinExiting == 0 {
				return false, fmt.Errorf("process group %d contains live PID %d (state %d, flags %#x): %w", pid, proc.Proc.P_pid, proc.Proc.P_stat, proc.Proc.P_flag, err)
			}
		}
		return true, nil
	}
	return false, err
}
