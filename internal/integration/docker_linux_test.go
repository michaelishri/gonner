//go:build linux

package integration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/michaelishri/gonner/internal/condition"
	"github.com/michaelishri/gonner/internal/config"
	"github.com/michaelishri/gonner/internal/execution"
	"github.com/michaelishri/gonner/internal/logging"
	"github.com/michaelishri/gonner/internal/runner"
)

func TestDockerPID1AndPrivilegeBoundaries(t *testing.T) {
	if os.Getenv("GONNER_DOCKER_TESTS") != "1" {
		t.Skip("set GONNER_DOCKER_TESTS=1; requires Docker and alpine:3.21")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "integration.test")
	build := exec.Command("go", "test", "-c", "-o", binary, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for name, data := range map[string]string{"passwd": "root:x:0:0::/root:/bin/sh\nworker:x:2001:3001::/tmp:/bin/sh\n", "group": "root:x:0:\nprimary:x:3001:\noverride:x:3002:\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"stress", "credentials", "missing-kill", "files", "conditions", "control"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			name := "gonner-audit-" + mode + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
			t.Cleanup(func() {
				cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = exec.CommandContext(cleanup, "docker", "rm", "-f", name).Run()
			})
			args := []string{"run", "--rm", "--name", name, "--network", "none", "--read-only", "--pids-limit", "2048", "--tmpfs", "/tmp:rw,nosuid,nodev,mode=1777", "--cap-drop", "ALL", "--cap-add", "SETUID", "--cap-add", "SETGID", "--cap-add", "CHOWN"}
			if mode != "missing-kill" {
				args = append(args, "--cap-add", "KILL")
			}
			args = append(args, "--mount", "type=bind,src="+dir+",dst=/fixture,readonly", "--mount", "type=bind,src="+filepath.Join(dir, "passwd")+",dst=/etc/passwd,readonly", "--mount", "type=bind,src="+filepath.Join(dir, "group")+",dst=/etc/group,readonly", "-e", "GONNER_CONTAINER_HELPER="+mode, "-e", "GOMAXPROCS=4", "alpine:3.21", "/fixture/integration.test", "-test.run=^TestContainerHelper$", "-test.v", "-test.timeout=150s")
			out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
			if err != nil {
				t.Fatalf("container %s: %v\n%s", mode, err, out)
			}
			t.Logf("%s passed in isolated PID namespace", mode)
		})
	}
}
func TestContainerHelper(t *testing.T) {
	mode := os.Getenv("GONNER_CONTAINER_HELPER")
	if mode == "" {
		t.Skip("container helper")
	}
	if os.Getpid() != 1 {
		t.Fatal("test must actually run as PID 1")
	}
	switch mode {
	case "stress":
		testStress(t)
	case "credentials":
		testCredentials(t)
	case "missing-kill":
		testMissingKill(t)
	case "files":
		testUnsafeFiles(t)
	case "conditions":
		testConditionGroups(t)
	case "control":
		testControlledGroups(t)
	default:
		t.Fatalf("unknown mode %q", mode)
	}
}
func testStress(t *testing.T) {
	// 512 command probes + 2,048 precommands + 1,024 main shells. Their fast
	// exits compete with SIGCHLD delivery and adopted grandchildren.
	cfg := &config.Config{ShutdownTimeout: config.Duration(100 * time.Millisecond)}
	for i := 0; i < 256; i++ {
		cfg.Run = append(cfg.Run, config.ProcessConfig{Name: fmt.Sprintf("p%d", i), Command: "true", Instances: 4, WhenAll: []map[string]string{{"commandSucceeds": "true"}, {"commandSucceeds": "true"}}, CommandsBefore: []config.PreCommand{{Command: "true"}, {Command: "true"}}})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	mgr := runner.NewManager(cfg)
	if err := mgr.Run(ctx); err != nil {
		t.Fatalf("managed exit status stolen: %v", err)
	}
	for _, p := range mgr.Processes() {
		if p.Status != "stopped" || p.Instances != 4 {
			t.Fatalf("unexpected status: %+v", p)
		}
	}
	cfg = &config.Config{Run: []config.ProcessConfig{{Name: "bad-main", Command: "exit 23"}, {Name: "bad-pre", Command: "true", CommandsBefore: []config.PreCommand{{Command: "exit 19"}}}, {Name: "false-probe", Command: "exit 99", WhenAll: []map[string]string{{"commandSucceeds": "exit 27"}}}}}
	err := runner.NewManager(cfg).Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "exit status 23") || !strings.Contains(err.Error(), "exit status 19") {
		t.Fatalf("nonzero statuses lost: %v", err)
	}
	noChildren(t)
}
func testCredentials(t *testing.T) {
	// A differing UID/GID proves numeric lookup uses passwd rather than uid=gid.
	cred, err := execution.ResolveCredential("2001", "")
	if err != nil {
		t.Fatal(err)
	}
	if cred.Uid != 2001 || cred.Gid != 3001 {
		t.Fatalf("credential=%+v", cred)
	}
	named, err := execution.ResolveCredential("worker", "override")
	if err != nil || named.Gid != 3002 {
		t.Fatalf("override=%+v %v", named, err)
	}
	unknown, err := execution.ResolveCredential("4000000000", "3001")
	if err != nil || unknown.Gid != 3001 {
		t.Fatalf("unknown UID with explicit GID: %+v %v", unknown, err)
	}
	if _, err := execution.ResolveCredential("4000000000", ""); err == nil {
		t.Fatal("unknown UID without GID accepted")
	}
	if err := syscall.Setgroups([]int{3002}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	var mu sync.Mutex
	service := execution.NewService()
	r := service.Run(context.Background(), execution.Spec{Command: "id -u; id -g; id -G", Credential: cred, Output: func(r io.Reader) error {
		b, err := io.ReadAll(r)
		mu.Lock()
		_, _ = output.Write(b)
		mu.Unlock()
		return err
	}})
	if r.Err != nil || output.String() != "2001\n3001\n3001\n" {
		t.Fatalf("credential result: %q %v", output.String(), r.Err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r = service.Run(ctx, execution.Spec{Command: "exec sleep 30", Credential: cred, StopTimeout: 100 * time.Millisecond, OnStart: func(int) { cancel() }})
	if r.Err != context.Canceled {
		t.Fatalf("cross-UID shutdown: %v", r.Err)
	}
	noChildren(t)
}
func testMissingKill(t *testing.T) {
	if _, err := execution.ResolveCredential("2001", ""); err == nil || !strings.Contains(err.Error(), "CAP_KILL") {
		t.Fatalf("missing KILL not rejected: %v", err)
	}
	cfg := &config.Config{Run: []config.ProcessConfig{{Name: "probe", Command: "true", WhenAll: []map[string]string{{"commandSucceeds": "touch /tmp/probe-ran"}}}, {Name: "child", Command: "touch /tmp/child-ran; exec sleep 30", User: "2001"}}}
	if err := runner.NewManager(cfg).Run(context.Background()); err == nil {
		t.Fatal("manager accepted missing KILL")
	}
	for _, name := range []string{"probe-ran", "child-ran"} {
		if _, err := os.Stat("/tmp/" + name); !os.IsNotExist(err) {
			t.Fatal("workload started before privilege preflight")
		}
	}
	noChildren(t)
}
func testUnsafeFiles(t *testing.T) {
	if err := os.Mkdir("/tmp/logs", 0700); err != nil {
		t.Fatal(err)
	}
	victim := "/tmp/victim"
	if err := os.WriteFile(victim, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"symlink", "hardlink", "fifo", "device", "owned-dir", "writable-dir"} {
		path := "/tmp/logs/" + kind
		switch kind {
		case "symlink":
			if err := os.Symlink(victim, path); err != nil {
				t.Fatal(err)
			}
		case "hardlink":
			if err := os.Link(victim, path); err != nil {
				t.Fatal(err)
			}
		case "fifo":
			if err := syscall.Mkfifo(path, 0600); err != nil {
				t.Fatal(err)
			}
		case "device":
			path = "/dev/null"
		case "owned-dir":
			_ = os.Mkdir(path, 0700)
			if err := os.Chown(path, 2001, 3001); err != nil {
				t.Fatal(err)
			}
			path += "/app.log"
		case "writable-dir":
			_ = os.Mkdir(path, 0777)
			_ = os.Chmod(path, 0777)
			path += "/app.log"
		}
		if w, err := logging.NewWriter("root", path); err == nil {
			_ = w.Close()
			t.Fatalf("accepted unsafe %s", kind)
		}
	}
	b, _ := os.ReadFile(victim)
	st, _ := os.Stat(victim)
	if string(b) != "private" || st.Mode().Perm() != 0600 {
		t.Fatal("victim altered")
	}
	cfg := &config.Config{Run: []config.ProcessConfig{{Name: "probe", Command: "true", WhenAll: []map[string]string{{"commandSucceeds": "touch /tmp/unsafe-probe-ran"}}}, {Name: "unsafe", Command: "true", LogFile: "/tmp/logs/owned-dir/app.log"}}}
	if err := runner.NewManager(cfg).Run(context.Background()); err == nil {
		t.Fatal("unsafe log directory accepted")
	}
	if _, err := os.Stat("/tmp/unsafe-probe-ran"); !os.IsNotExist(err) {
		t.Fatal("condition ran before path validation")
	}
}
func testConditionGroups(t *testing.T) {
	service := execution.NewService()
	join, err := service.StartReaper(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer join()
	for _, cancelled := range []bool{false, true} {
		marker := fmt.Sprintf("/tmp/child-%v", cancelled)
		command := "sleep 30 & echo $! > " + marker
		ctx, cancel := context.WithCancel(context.Background())
		if cancelled {
			command += "; wait"
			go func() {
				for {
					if _, err := os.Stat(marker); err == nil {
						cancel()
						return
					}
					select {
					case <-ctx.Done():
						return
					case <-time.After(time.Millisecond):
					}
				}
			}()
		}
		ok, _, err := condition.NewEvaluator(service, execution.Spec{}).ShouldRun(ctx, []map[string]string{{"commandSucceeds": command}}, nil)
		cancel()
		if cancelled {
			if err != context.Canceled && !strings.Contains(fmt.Sprint(err), "context canceled") {
				t.Fatalf("cancelled probe: %v", err)
			}
		} else if !ok || err != nil {
			t.Fatalf("normal probe: %v %v", ok, err)
		}
	}
	// Normal main and precommand exits also clean up background descendants.
	cfg := &config.Config{ShutdownTimeout: config.Duration(50 * time.Millisecond), Run: []config.ProcessConfig{{Name: "orphans", Command: "sleep 30 & true", CommandsBefore: []config.PreCommand{{Command: "sleep 30 & true"}}}}}
	if err := runner.NewManager(cfg).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	noChildren(t)
}
func noChildren(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		paths, err := filepath.Glob("/proc/self/task/*/children")
		if err != nil {
			t.Fatal(err)
		}
		var live []string
		for _, path := range paths {
			b, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			live = append(live, strings.Fields(string(b))...)
		}
		if len(live) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("unreaped or live descendants: %v", live)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func testControlledGroups(t *testing.T) {
	cfg := &config.Config{
		Control:         &config.ControlConfig{Socket: "/tmp/unused-control.sock", RestartGrace: config.Duration(50 * time.Millisecond)},
		ShutdownTimeout: config.Duration(50 * time.Millisecond),
		Run: []config.ProcessConfig{{Name: "worker", Instances: 2,
			Command:     "trap '' TERM; sleep 60 & touch /tmp/ready-$GONNER_GENERATION; wait",
			AutoRestart: true, RestartOnSuccess: true, Controllable: true,
			Backoff: &config.BackoffConfig{InitialDelay: config.Duration(200 * time.Millisecond), MaxDelay: config.Duration(200 * time.Millisecond), Multiplier: 1},
		}},
	}
	mgr := runner.NewManager(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	done := make(chan error, 1)
	go func() { done <- mgr.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("controlled processes did not join")
		}
		noChildren(t)
	}()
	await := func(check func([]runner.InstanceInfo) bool) []runner.InstanceInfo {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			instances := mgr.Instances()
			if check(instances) {
				return instances
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("control state did not settle")
		return nil
	}
	first := await(func(states []runner.InstanceInfo) bool {
		for _, s := range states {
			if s.State != runner.StateRunning {
				return false
			}
			if _, err := os.Stat("/tmp/ready-" + s.Generation); err != nil {
				return false
			}
		}
		return len(states) == 2
	})
	old := first[0]
	started := time.Now()
	if err := mgr.Restart(old.ID, old.Generation, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	next := await(func(states []runner.InstanceInfo) bool {
		return states[0].Generation != old.Generation && states[0].State == runner.StateRunning
	})
	if time.Since(started) > time.Second || !next[0].PreviousReaped || next[0].PreviousGeneration != old.Generation {
		t.Fatalf("replacement lacks bounded reap evidence: %+v", next)
	}
	if next[1].Generation != first[1].Generation {
		t.Fatal("sibling was restarted")
	}
	if err := mgr.Restart(old.ID, old.Generation, time.Millisecond); err == nil {
		t.Fatal("stale generation accepted")
	}
}
