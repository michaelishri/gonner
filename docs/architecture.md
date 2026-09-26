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

- `manager.go` — top-level orchestrator. Builds every configured instance at construction, performs preflight, evaluates conditions, drives `parallel` or `sequential` startup, gathers status for the health endpoint.
- `process.go` — per-process state machine, restart loop, signal propagation, credential drop.
- `backoff.go` — exponential backoff with ±10% jitter and post-stability reset.
- `signals.go` — `SignalHandler` (SIGTERM/SIGINT → shutdown, SIGHUP/USR* → forward). Signal registration is synchronous before workload startup.

### `internal/health` — HTTP endpoint

- `server.go` — bind, TLS, timeouts (Slowloris-safe), bearer-token middleware, graceful shutdown.
- `handlers.go` — `/health`, `/status`, `/metrics`.

### `internal/logging` — multiplexed writer

- `writer.go` — per-process prefix writers over a shared sink registry, with bounded streaming, exclusive size-based rotation and optional gzip; also exposes `Gonner` (operational stderr) and `Recover` (panic-safe goroutines).

---

## Concurrency model

- `Manager.Run` uses a cancellable context and a wait group. Permanent noncritical failures are collected without cancelling unrelated work; critical and infrastructure failures cancel the context. Every started lifecycle is joined before return.
- Each instance has stable first-success and lifecycle-done channels. Dependency and sequential gates watch every instance and terminate if all instances finish without starting.
- `Process.mu` protects its current PID, start time and successful restart count; state uses an atomic value. Manager uptime is mutex-protected and zero before `Run`.
- `internal/execution` owns main, precommand and probe execution without importing configuration or logging. Each child gets a process group and exactly one `exec.Cmd.Wait`. Explicit `os.Pipe` descriptors keep readers open after `Wait`, so output can drain before lifecycle completion.
- `internal/safefile` holds verified directory descriptors for logs and PID files. `internal/logging` shares descriptors, rotation counters and locks by canonical directory identity and leaf name.

## PID 1 reaping coordination

On Linux PID 1, the execution service enumerates direct children through `/proc/self/task/*/children`. Startup fails if this discovery is unavailable. Reaping runs on `SIGCHLD` and at least once per second. A process-wide registry lock covers both `Start` plus registration and orphan selection plus PID-specific nonblocking `Wait4`. Registered children are always excluded: their sole wait owner is `exec.Cmd.Wait`. There is no wildcard wait or exit-status cache. The reaper stays active through workload cancellation and cleanup. Non-PID-1 Linux and Darwin do not start an orphan reaper.

---

## Shutdown flow

1. Trigger: SIGTERM/SIGINT received, **or** a critical process exits unexpectedly, **or** the outer context is cancelled.
2. `SignalHandler` (or `Process.onCriticalExit`) calls the top `cancelFn`.
3. The manager context is cancelled; the manager marks itself as shutting down, and process lifecycles stop starting or restarting commands.
4. The shared execution service performs group shutdown concurrently for each active command:
   - send the configured `stopSignal` to the command's process group,
   - wait up to `stopTimeout` (per-process) or `shutdownTimeout` (global) for the group to exit,
   - escalate to SIGKILL for remaining group members, even if the original shell has already exited.
   Both `commandsBefore` and main commands use this path. Command conditions use the same execution service but kill their group on probe cancellation or timeout. Each command has its own process group; its initial PID remains the group ID after the shell exits. Any descendants left in that group when a command exits normally are also stopped before the lifecycle advances. Descendants that create a separate session or process group are outside this group-based shutdown mechanism.
5. Health server's shutdown goroutine completes within 5s.
6. After all started process lifecycles finish shutdown and reap their direct children, `Manager.Run` returns; gonner exits 1 if any permanent or supervisor failure was recorded, otherwise 0.

---

## Build & versioning

- Versioning via git tags. `Taskfile.yml` injects `version`, `commit`, `buildDate` through `-ldflags`.
- `CGO_ENABLED=0` for a fully static binary, even on Alpine.
- Stripped (`-s -w`) by default; target binary < 10 MB.
