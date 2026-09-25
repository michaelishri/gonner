package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// These integration tests use a Linux subreaper to adopt and clean up every
// descendant, including the children that a broken supervisor leaves behind.
func TestRunShutdown(t *testing.T) {
	const prGetChildSubreaper = 37
	const prSetChildSubreaper = 36
	var previous int32
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, prGetChildSubreaper, uintptr(unsafe.Pointer(&previous)), 0, 0, 0, 0); errno != 0 {
		t.Fatal(errno)
	}
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0, 0, 0, 0); errno != 0 {
		t.Fatal(errno)
	}
	t.Cleanup(func() {
		if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, prSetChildSubreaper, uintptr(previous), 0, 0, 0, 0); errno != 0 {
			t.Error(errno)
		}
	})

	for _, tc := range []struct {
		name       string
		signal     syscall.Signal
		stopSignal string
		wrap       bool
		ignore     bool
		global     bool
		exitShell  bool
		preCommand bool
		sequential bool
		critical   bool
	}{
		{name: "sigterm_exec", signal: syscall.SIGTERM},
		{name: "sigint_custom_stop_signal", signal: syscall.SIGINT, stopSignal: "SIGUSR1"},
		{name: "sigterm_shell_child", signal: syscall.SIGTERM, wrap: true},
		{name: "shell_child_timeout", signal: syscall.SIGTERM, wrap: true, ignore: true},
		{name: "global_timeout", signal: syscall.SIGTERM, ignore: true, global: true},
		{name: "exited_shell_child", exitShell: true},
		{name: "commands_before", signal: syscall.SIGTERM, wrap: true, preCommand: true},
		{name: "sequential_startup", signal: syscall.SIGTERM, preCommand: true, sequential: true},
		{name: "critical_exit", wrap: true, critical: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			worker := shellQuote(executable) + " -test.run=^TestShutdownHelper$ -- worker"
			command := "exec " + worker
			if tc.wrap {
				// Keep the shell alive instead of allowing it to optimize the
				// final command into an exec. SIGTERM will kill this wrapper
				// before the worker finishes its delayed graceful shutdown.
				command = worker + "; exit $?"
			}
			if tc.exitShell {
				command = worker + " & while [ ! -f " + shellQuote(filepath.Join(dir, "ready")) + " ]; do sleep 0.01; done"
			}
			process := map[string]any{
				"name": "worker", "command": command, "autoRestart": true,
				"stopTimeout": "500ms",
			}
			if tc.stopSignal != "" {
				process["stopSignal"] = tc.stopSignal
			}
			if tc.preCommand {
				process["commandsBefore"] = []map[string]any{
					{"command": command, "continueOnError": true},
					{"command": "touch " + shellQuote(filepath.Join(dir, "next-pre-command"))},
				}
				process["command"] = "touch " + shellQuote(filepath.Join(dir, "main-started"))
			}
			processes := []map[string]any{process}
			if tc.critical {
				processes = append(processes, map[string]any{
					"name": "critical", "critical": true,
					"command": "echo $$ > " + shellQuote(filepath.Join(dir, "critical-pgid")) +
						"; while [ ! -f " + shellQuote(filepath.Join(dir, "trigger")) + " ]; do sleep 0.01; done; exit 1",
				})
			}
			cfg := map[string]any{"shutdownTimeout": "3s", "run": processes}
			if tc.global {
				delete(process, "stopTimeout")
				cfg["shutdownTimeout"] = "500ms"
			}
			if tc.sequential {
				cfg["mode"] = "sequential"
				processes = append(processes, map[string]any{
					"name": "later", "command": "touch " + shellQuote(filepath.Join(dir, "later-started")),
				})
				cfg["run"] = processes
			}
			data, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "gonner.json"), data, 0o600); err != nil {
				t.Fatal(err)
			}

			cmd := exec.Command(executable, "-test.run=^TestShutdownHelper$", "--", "gonner")
			cmd.Env = append(os.Environ(),
				"GONNER_SHUTDOWN_HELPER=1", "GONNER_SHUTDOWN_DIR="+dir,
				"GONNER_HEALTH_PORT=0",
				"GONNER_SHUTDOWN_SIGNAL="+tc.stopSignal,
				"GONNER_SHUTDOWN_IGNORE="+strconv.FormatBool(tc.ignore),
				"GORACE=atexit_sleep_ms=0",
			)
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			var output bytes.Buffer
			cmd.Stdout = &output
			cmd.Stderr = &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			var waitErr error
			go func() {
				waitErr = cmd.Wait()
				close(done)
			}()
			var pgid int
			t.Cleanup(func() {
				groups := []int{pgid}
				if data, err := os.ReadFile(filepath.Join(dir, "critical-pgid")); err == nil {
					if criticalPGID, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
						groups = append(groups, criticalPGID)
					}
				}
				for _, group := range groups {
					if group > 0 {
						_ = syscall.Kill(-group, syscall.SIGKILL)
					}
				}
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("supervisor did not exit during cleanup")
				}
				for _, group := range groups {
					if group <= 0 {
						continue
					}
					deadline := time.Now().Add(5 * time.Second)
					for !reapProcessGroup(t, group) && time.Now().Before(deadline) {
						time.Sleep(10 * time.Millisecond)
					}
					if err := syscall.Kill(-group, 0); !errors.Is(err, syscall.ESRCH) {
						t.Errorf("process group %d survived cleanup: %v", group, err)
					}
				}
			})

			deadline := time.Now().Add(5 * time.Second)
			for pgid == 0 {
				if data, err := os.ReadFile(filepath.Join(dir, "ready")); err == nil {
					pgid, err = strconv.Atoi(string(data))
					if err != nil {
						t.Fatal(err)
					}
					break
				}
				select {
				case <-done:
					t.Fatalf("supervisor exited before worker readiness: %v\n%s", waitErr, &output)
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("worker did not become ready")
				}
				time.Sleep(10 * time.Millisecond)
			}

			start := time.Now()
			if tc.critical {
				if err := os.WriteFile(filepath.Join(dir, "trigger"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			} else if !tc.exitShell {
				if err := cmd.Process.Signal(tc.signal); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("supervisor did not finish shutdown")
			}
			elapsed := time.Since(start)
			if waitErr != nil {
				t.Errorf("supervisor exit: %v\n%s", waitErr, &output)
			}
			stopped, err := os.ReadFile(filepath.Join(dir, "stopped"))
			if tc.ignore {
				if !errors.Is(err, os.ErrNotExist) {
					t.Errorf("signal-ignoring worker unexpectedly stopped gracefully: %s (%v)", stopped, err)
				}
				if elapsed < 450*time.Millisecond || elapsed > 2*time.Second {
					t.Errorf("shutdown took %s; expected the configured 500ms timeout", elapsed)
				}
			} else if err != nil {
				t.Errorf("worker did not finish graceful shutdown before supervisor exit: %v\n%s", err, &output)
			} else {
				want := syscall.SIGTERM.String()
				if tc.stopSignal == "SIGUSR1" {
					want = syscall.SIGUSR1.String()
				}
				if string(stopped) != want {
					t.Errorf("stop signal: got %q, want %q", stopped, want)
				}
			}
			for _, marker := range []string{"next-pre-command", "main-started", "later-started"} {
				if _, err := os.Stat(filepath.Join(dir, marker)); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("%s ran during shutdown", marker)
				}
			}
			// Reap exited orphans and allow SIGKILL a short scheduling margin.
			deadline = time.Now().Add(200 * time.Millisecond)
			for !reapProcessGroup(t, pgid) && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if err := syscall.Kill(-pgid, 0); !errors.Is(err, syscall.ESRCH) {
				t.Errorf("worker process group %d is still alive after supervisor exit: %v", pgid, err)
			}
		})
	}
}

func reapProcessGroup(t *testing.T, pgid int) bool {
	t.Helper()
	for {
		pid, err := syscall.Wait4(-pgid, nil, syscall.WNOHANG, nil)
		if errors.Is(err, syscall.ECHILD) {
			return true
		}
		if err != nil {
			t.Errorf("reaping process group %d: %v", pgid, err)
			return false
		}
		if pid == 0 {
			return false
		}
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func TestShutdownHelper(t *testing.T) {
	if os.Getenv("GONNER_SHUTDOWN_HELPER") != "1" {
		return
	}
	dir := os.Getenv("GONNER_SHUTDOWN_DIR")
	if os.Args[len(os.Args)-1] == "gonner" {
		rootCmd.SetArgs([]string{"run", "--config", filepath.Join(dir, "gonner.json")})
		if err := Execute(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	stopSignal := syscall.SIGTERM
	if os.Getenv("GONNER_SHUTDOWN_SIGNAL") == "SIGUSR1" {
		stopSignal = syscall.SIGUSR1
	}
	signals := make(chan os.Signal, 1)
	if os.Getenv("GONNER_SHUTDOWN_IGNORE") == "true" {
		signal.Ignore(stopSignal)
	} else {
		signal.Notify(signals, stopSignal)
	}
	if err := os.WriteFile(filepath.Join(dir, "ready.tmp"), []byte(strconv.Itoa(syscall.Getpgrp())), 0o600); err != nil {
		os.Exit(2)
	}
	if err := os.Rename(filepath.Join(dir, "ready.tmp"), filepath.Join(dir, "ready")); err != nil {
		os.Exit(2)
	}
	sig := <-signals
	time.Sleep(100 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(dir, "stopped"), []byte(sig.String()), 0o600); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}
