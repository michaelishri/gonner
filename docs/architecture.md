# Architecture Overview

Gonner is a single Go binary that orchestrates child processes from a declarative config file. This document explains the major components and how they interact.

## Process model

```
                 ┌──────────────────────────────────────────┐
                 │             gonner (PID 1 or N)          │
                 │                                          │
                 │  ┌─────────┐  ┌─────────┐  ┌──────────┐  │
  signals  ───►  │  │ signal  │  │  proc   │  │  health  │  │
                 │  │ handler │  │ manager │  │  server  │  │
                 │  └────┬────┘  └────┬────┘  └────┬─────┘  │
                 │       │            │            │        │
                 │       └──── ctx ───┴────────────┘        │
                 │                    │                     │
                 │       ┌────────────┴────────────┐        │
                 │       ▼                         ▼        │
                 │   process[i].Run            process[j].Run
                 │       │                         │        │
                 │   exec.Cmd ─┬─ stdout ─►  Writer ─► stdout (prefixed)
                 │             │                            └► logFile (raw)
                 │             └─ stderr ─►  Writer ─► …    │
                 └──────────────────────────────────────────┘
```

## Components

### `cmd/gonner` — entry point

Thin wrapper around the cobra-based subcommands (`run`, `status`, `validate`, `version`). `run` is the default.

### `internal/config` — schema, parsing, validation

- `discovery.go` — locate the config file (flag > CWD > XDG > /etc).
- `parser.go` — env-var interpolation pass, then unmarshal JSON or YAML.
- `validation.go` — schema checks, duplicate-name detection, DAG cycle detection (Kahn's algorithm), warning accumulator.
- `duration.go` — `time.Duration` wrapper that accepts JSON/YAML strings.
- `config.go` — typed config structs and `ApplyDefaults`.

### `internal/condition` — pluggable startup conditions

- `condition.go` — `Condition` interface.
- `registry.go` — `Create`, `Register`, `EvaluateAll`, `EvaluateAny`, `ShouldRun`.
- `env.go`, `file.go`, `portopen.go`, `commandsucceeds.go` — built-in types.

To add a new condition type, implement `Condition` and call `condition.Register("name", factory)` from an `init()` block in your own package. Custom builds can keep this in a `internal/conditionx/` directory.

### `internal/runner` — lifecycle orchestration

- `manager.go` — top-level orchestrator. Evaluates conditions, builds the `[]*Process` set, drives `parallel` or `sequential` startup, gathers status for the health endpoint.
- `process.go` — per-process state machine, restart loop, signal propagation, credential drop.
- `backoff.go` — exponential backoff with ±10% jitter and post-stability reset.
- `signals.go` — `SignalHandler` (SIGTERM/SIGINT → shutdown, SIGHUP/USR* → forward) and `StartZombieReaper` (SIGCHLD-driven, PID-1 only).

### `internal/health` — HTTP endpoint

- `server.go` — bind, TLS, timeouts (Slowloris-safe), bearer-token middleware, graceful shutdown.
- `handlers.go` — `/health`, `/status`, `/metrics`.

### `internal/logging` — multiplexed writer

- `writer.go` — per-process writer that fans out to stdout (prefixed) + log file (raw), with size-based rotation and optional gzip; also exposes `Gonner` (operational stderr) and `Recover` (panic-safe goroutines).

---

## Concurrency model

- **Top context.** `cmd.run` creates a single cancellable `context.Context`. Cancellation propagates to:
  - the signal handler (via the `cancelFn` it owns),
  - each `Process.Run` (via `errgroup.WithContext`),
  - the health server (graceful shutdown).
- **errgroup.** `Manager.Run` launches every process in an `errgroup.Group`. A returned error or context cancellation triggers shutdown of the remaining processes via `Manager.shutdownAll`.
- **Per-process goroutines.** Each instance has its own goroutine running `Process.Run`. Within that, the process spawns two more for stdout/stderr line scanning.
- **`readyCh` / `doneCh`.** Channels per process used by `dependsOn` waiters in `parallel` mode and the next-up sequencer in `sequential` mode.
- **Mutex.** `Process.mu` guards `currentCmd`, `cmdDone`, `startedAt`, `restarts`, `readyCh`. State itself is an `atomic.Value` so `State()` is lock-free.

---

## PID 1 reaping coordination

When `os.Getpid() == 1`, `StartZombieReaper` subscribes to `SIGCHLD`. On each signal it drains all currently-exited children via `wait4(-1, WNOHANG)`. Each reaped child's `WaitStatus` is stored in the package-level `ReapedStatuses` `sync.Map` keyed by PID.

`Process.runCommand` calls `cmd.Wait()` as usual. If the reaper got there first, Go's `cmd.Wait` returns "wait: no child processes" (an error that is **not** an `*exec.ExitError`). At that point `runCommand` falls back to `ReapedStatuses.LoadAndDelete(pid)` to recover the real exit code.

This avoids the classic "reaper-vs-exec.Cmd" race that plagues PID-1 supervisors written in Go.

---

## Shutdown flow

1. Trigger: SIGTERM/SIGINT received, **or** a critical process exits unexpectedly, **or** the outer context is cancelled.
2. `SignalHandler` (or `Process.onCriticalExit`) calls the top `cancelFn`.
3. The errgroup's context is cancelled; every `Process.Run` checks the context and stops looping.
4. `Manager.shutdownAll` iterates remaining processes and calls `Process.Stop` concurrently:
   - send the configured `stopSignal` to the process group,
   - wait up to `stopTimeout` (per-process) or `shutdownTimeout` (global),
   - escalate to SIGKILL if still alive.
5. Health server's shutdown goroutine completes within 5s.
6. `Manager.Run` returns; gonner exits with the errgroup's error (if any).

---

## Build & versioning

- Versioning via git tags. `Taskfile.yml` injects `version`, `commit`, `buildDate` through `-ldflags`.
- `CGO_ENABLED=0` for a fully static binary, even on Alpine.
- Stripped (`-s -w`) by default; target binary < 10 MB.
