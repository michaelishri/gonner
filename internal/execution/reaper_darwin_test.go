package execution

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func startDarwinGroup(t *testing.T, command string, args ...string) int {
	t.Helper()
	cmd := exec.Command(command, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd.Process.Pid
}

func TestDarwinZombieGroupCleanup(t *testing.T) {
	pid := startDarwinGroup(t, "true")
	deadline := time.Now().Add(5 * time.Second)
	for {
		proc, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
		if err != nil {
			t.Fatal(err)
		}
		if proc.Proc.P_stat == darwinZombie {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not become a zombie")
		}
		time.Sleep(time.Millisecond)
	}
	// Deliberately leave the child unreaped until cleanup so the group remains
	// present with only a zombie throughout each operation below.
	if alive, err := groupAlive(pid); alive || err != nil {
		t.Fatalf("zombie group: alive=%v, err=%v", alive, err)
	}
	if err := SignalGroup(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("signalling zombie group: %v", err)
	}
	if err := stopGroup(pid, syscall.SIGTERM, 50*time.Millisecond); err != nil {
		t.Fatalf("stopping zombie group: %v", err)
	}
}

func TestDarwinLiveGroupPreservesSignalErrors(t *testing.T) {
	pid := startDarwinGroup(t, "sleep", "30")
	if alive, err := groupAlive(pid); !alive || err != nil {
		t.Fatalf("live group: alive=%v, err=%v", alive, err)
	}
	// Exercise permission-denial handling without requiring privileged tests
	// or signalling another user's processes.
	if exited, err := groupSignalResult(pid, syscall.EPERM); exited || !errors.Is(err, syscall.EPERM) {
		t.Fatalf("suppressed live-group permission error: exited=%v, err=%v", exited, err)
	}
	if err := SignalGroup(pid, syscall.Signal(-1)); !errors.Is(err, syscall.EINVAL) {
		t.Fatalf("suppressed invalid signal: %v", err)
	}
}

func TestDarwinCancellationDuringExit(t *testing.T) {
	for i := 0; i < 100; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		r := NewService().Run(ctx, Spec{
			Command:     "exec sleep 30",
			StopSignal:  syscall.SIGKILL,
			StopTimeout: time.Second,
			OnStart:     func(int) { cancel() },
		})
		cancel()
		if r.Err != context.Canceled {
			t.Fatalf("cancellation %d: %v", i, r.Err)
		}
	}
}
