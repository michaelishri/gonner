# Gonner — Implementation Plan

## Overview

Gonner is a lightweight, PID-1-aware process manager written in Go. It reads a JSON or YAML configuration file, conditionally starts and monitors multiple child processes, handles signal forwarding and zombie reaping, and exposes health/status information via both an HTTP endpoint and a CLI subcommand.

**Primary use case**: Docker/container entrypoint (`ENTRYPOINT ["gonner"]`) for orchestrating multiple services (e.g., NGINX, Laravel scheduler, queue workers) in a single container. Also works as a regular process manager for local development with a separate config file.

**Key design principles**:
- Single static Go binary, no external dependencies
- PID 1-aware: auto-detects container context, enables zombie reaping only when needed
- Extensible condition system for conditional process startup
- Goroutine-per-process concurrency model using `errgroup` and `context.Context`
- Config auto-discovery with environment variable interpolation

---

## Project Structure

```
gonner/
├── cmd/
│   └── gonner/
│       └── main.go              # CLI entrypoint, subcommand routing
├── internal/
│   ├── config/
│   │   ├── config.go            # Config struct definitions
│   │   ├── parser.go            # JSON/YAML parsing, env interpolation
│   │   ├── discovery.go         # Auto-discovery of config files
│   │   └── validation.go        # Schema validation, DAG cycle detection
│   ├── runner/
│   │   ├── manager.go           # ProcessManager: orchestrates all processes
│   │   ├── process.go           # Single process lifecycle (start, monitor, restart)
│   │   ├── signals.go           # Signal trapping, forwarding, zombie reaping
│   │   └── backoff.go           # Exponential backoff logic for restarts
│   ├── condition/
│   │   ├── condition.go         # Condition interface definition
│   │   ├── env.go               # EnvCondition: checks KEY=VALUE
│   │   ├── file.go              # FileCondition: checks file existence
│   │   └── registry.go          # Condition type registry
│   ├── health/
│   │   ├── server.go            # HTTP health endpoint server
│   │   └── handlers.go          # /health and /status route handlers
│   └── logging/
│       └── writer.go            # Multiplexed log writer (file + stdout)
├── go.mod
├── go.sum
├── Taskfile.yml                 # Build, test, lint tasks (Task runner)
├── README.md
└── config.json                  # Example configuration
```

---

## Step 1: Project Initialization

### 1.1 Go module

- Initialize module: `go mod init github.com/<owner>/gonner`
- Minimum Go version: 1.22+ (for enhanced stdlib routing if needed)
- External dependencies to evaluate:
  - `github.com/spf13/cobra` — CLI subcommands (or use stdlib `flag` if keeping it minimal)
  - `golang.org/x/sync/errgroup` — goroutine group management
  - `gopkg.in/yaml.v3` — YAML config parsing
  - No other external dependencies; keep the binary lean

### 1.2 Taskfile.yml

Use [Task](https://taskfile.dev/) as the build runner. Create `Taskfile.yml` at project root with tasks:

- `task build` — `CGO_ENABLED=0 go build -ldflags "..." -o bin/gonner ./cmd/gonner/`
- `task test` — `go test ./... -v -race -cover`
- `task lint` — `golangci-lint run ./...`
- `task release` — multi-arch builds for `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`
- `task clean` — remove `bin/` directory

### 1.3 Version embedding

- Use `-ldflags` to embed version, commit hash, and build date at compile time
- Expose via `gonner version` subcommand

---

## Step 2: Configuration System (`internal/config/`)

### 2.1 Config auto-discovery (`discovery.go`)

Gonner searches for its config file in the following order. **First match wins**:

| Priority | Source | Path(s) checked |
|----------|--------|-----------------|
| 1 | `--config` CLI flag | Exact path provided. If it's a directory, look for `gonner.json` / `gonner.yaml` inside it. |
| 2 | Current working directory | `./gonner.json`, then `./gonner.yaml` |
| 3 | User config (XDG) | `~/.config/gonner/gonner.json`, then `~/.config/gonner/gonner.yaml` |
| 4 | System-wide | `/etc/gonner/gonner.json`, then `/etc/gonner/gonner.yaml` |

- At each priority level, JSON is checked before YAML
- If no config is found at any level, gonner exits with a clear error message listing every path it searched
- The `--config` flag accepts both file paths and directory paths
- Log which config file was selected on startup

### 2.2 Config schema

**Top-level structure:**

```json
{
  "mode": "parallel",
  "shutdownTimeout": "30s",
  "health": {
    "port": 8089
  },
  "run": [ ]
}
```

- `mode`: `"parallel"` (default) or `"sequential"` — global startup strategy
- `shutdownTimeout`: duration to wait before SIGKILL on shutdown (default `"30s"`)
- `health`: optional HTTP health endpoint config
- `run`: array of process definitions

**Process definition schema (`run` array elements):**

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `name` | string | *required* | Unique process identifier. Used in logs, health output, and `dependsOn` references. |
| `command` | string | *required* | The command to execute. Passed to `sh -c` for shell expansion. |
| `workDir` | string | `""` (inherit) | Working directory for the command. |
| `logFile` | string | `""` (none) | Path to log file. Parent directories are created if they don't exist. |
| `autoRestart` | bool | `false` | Whether to restart the process on exit. |
| `maxRetries` | int | `0` (unlimited) | Max restart attempts. Only applies when `autoRestart` is `true`. `0` means unlimited. |
| `backoff` | object | see below | Backoff configuration for restarts. |
| `backoff.initialDelay` | duration string | `"1s"` | Delay before first restart. |
| `backoff.maxDelay` | duration string | `"30s"` | Maximum delay between restarts. |
| `backoff.multiplier` | float | `2.0` | Multiplier applied to delay after each restart. |
| `instances` | int | `1` | Number of identical copies of this process to run. |
| `critical` | bool | `false` | If `true`, unexpected exit of this process triggers full shutdown of all processes. |
| `dependsOn` | []string | `[]` | Names of other processes that must be running before this one starts. |
| `whenAll` | object | `{}` | All conditions must evaluate to `true` for the process to start. |
| `whenAny` | object | `{}` | At least one condition must evaluate to `true` for the process to start. |
| `commandsBefore` | []object | `[]` | Commands to run sequentially before starting the main process. |

**`commandsBefore` element schema:**

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `command` | string | *required* | Command to run. |
| `workDir` | string | `""` (inherit from parent process) | Working directory. |
| `continueOnError` | bool | `false` | If `false`, a non-zero exit aborts this process's startup (but not other processes). If `true`, log a warning and continue. |

### 2.3 Environment variable interpolation (`parser.go`)

- Syntax: `{{env://VAR_NAME}}` — replaced with the value of environment variable `VAR_NAME`
- With default: `{{env://VAR_NAME:default_value}}` — uses `default_value` if `VAR_NAME` is unset or empty
- Interpolation is applied to **all string values** in the config after file loading but before parsing into Go structs
- Implementation: regex-based replacement pass over the raw config string before JSON/YAML unmarshalling
- Regex pattern: `\{\{env://([A-Za-z_][A-Za-z0-9_]*)(?::([^}]*))?\}\}`
- If a referenced env var is unset and no default is provided, gonner exits with an error listing the missing variable

### 2.4 Validation (`validation.go`)

Perform after parsing:

- All `name` fields must be unique
- All `dependsOn` references must refer to existing process names
- **DAG cycle detection**: build a directed graph of `dependsOn` relationships, run topological sort (Kahn's algorithm). If a cycle is detected, exit with an error listing the cycle
- `instances` must be >= 1
- `maxRetries` must be >= 0
- `backoff.multiplier` must be > 0
- `shutdownTimeout` must be parseable as a Go `time.Duration`
- `mode` must be `"parallel"` or `"sequential"`
- Warn (don't error) if `maxRetries` is set but `autoRestart` is `false`

---

## Step 3: Condition Engine (`internal/condition/`)

### 3.1 Interface (`condition.go`)

Define a `Condition` interface with methods:
- `Type() string` — returns the condition type name
- `Evaluate() (bool, error)` — evaluates the condition

All condition types implement this interface.

### 3.2 Condition types

**EnvCondition (`env.go`)**
- Config key: `"env"`
- Value format: `"KEY=VALUE"` — checks if environment variable `KEY` equals `VALUE`
- Also supports: `"KEY"` (just checks if `KEY` is set and non-empty)
- Example: `"whenAll": { "env": "LARAVEL_WEBSERVER=TRUE" }`

**FileCondition (`file.go`)**
- Config key: `"fileExists"`
- Value format: a file path string
- Returns `true` if the file or directory exists at that path
- Example: `"whenAll": { "fileExists": "/app/.env" }`

### 3.3 Registry (`registry.go`)

- A map of `string -> ConditionFactory` functions
- Each factory takes the raw config value and returns a `Condition` instance
- Built-in registrations: `"env"` -> `NewEnvCondition`, `"fileExists"` -> `NewFileCondition`
- Unknown condition types in config produce a startup error
- **Extensibility**: new condition types (e.g., `portOpen`, `httpHealthy`, `commandSucceeds`) can be added by implementing the interface and registering in the map

### 3.4 Evaluation logic

- `whenAll`: iterate all conditions, short-circuit on first `false`. Process is skipped if any condition fails.
- `whenAny`: iterate all conditions, short-circuit on first `true`. Process is skipped if all conditions fail.
- If both `whenAll` and `whenAny` are present, **both** must pass (AND relationship between the two blocks).
- If neither is present, the process always starts.
- Skipped processes are logged at INFO level: `[gonner] Skipping "Laravel Scheduler": condition not met (env LARAVEL_SCHEDULER=TRUE)`

---

## Step 4: Process Runner (`internal/runner/`)

### 4.1 ProcessManager (`manager.go`)

The central orchestrator. Responsibilities:

1. Accept the parsed config
2. Evaluate conditions for each process, build the list of runnable processes
3. Resolve `dependsOn` into a startup order (topological sort)
4. Start processes according to the global `mode`:
   - **`parallel`** (default): start all processes concurrently in goroutines, but respect `dependsOn` ordering — a process waits until all its dependencies are in "running" state before starting
   - **`sequential`**: start processes in config array order, one at a time. Each process must reach "running" state before the next one begins. `dependsOn` is still validated but is effectively implied by order.
5. Monitor all processes via the errgroup
6. Handle shutdown when triggered (signal, critical process exit, or context cancellation)

**Concurrency model:**
- Create an `errgroup.Group` with a cancellable `context.Context`
- Each process (and each instance of a multi-instance process) runs in its own goroutine within the errgroup
- A "process ready" notification uses per-process channels or `sync.WaitGroup` so that `dependsOn` waiters know when a dependency is running
- When a `critical` process exits unexpectedly → cancel the errgroup's context → all goroutines begin graceful shutdown
- The manager waits for the errgroup to finish, then exits with an appropriate status code

**State tracking:**
- Each process has a state: `pending` → `starting` → `running` → `stopping` → `stopped` / `failed`
- State transitions are communicated via channels to the health endpoint
- The manager maintains a thread-safe map of process name → current state

### 4.2 Process lifecycle (`process.go`)

Each process goroutine follows this lifecycle:

1. **Wait for dependencies**: if `dependsOn` is set, block until all named dependencies reach `running` state (with context cancellation support so we don't wait forever on shutdown)
2. **Run `commandsBefore`**: execute each pre-command sequentially:
   - Run via `os/exec.CommandContext` with the process's context
   - Capture stdout/stderr to the process's log writer
   - On non-zero exit: if `continueOnError` is `false`, log the error, set process state to `failed`, return. If `true`, log a warning, continue to next pre-command.
3. **Start main command**:
   - Execute via `os/exec.CommandContext(ctx, "sh", "-c", command)`
   - Set `Cmd.Dir` to `workDir` if specified
   - Pipe stdout and stderr to the multiplexed log writer
   - Set `Cmd.SysProcAttr` for proper process group handling
   - Set process state to `running`, notify dependents
4. **Monitor**: wait for the command to exit
   - On exit code 0: normal exit, set state to `stopped`
   - On non-zero exit:
     - If `autoRestart` is `true` and retry budget remains: increment restart counter, wait for backoff delay, go to step 3
     - If `critical` is `true`: log critical failure, cancel the errgroup context (triggers full shutdown)
     - Otherwise: set state to `failed`, log the exit code
5. **Cleanup**: on context cancellation (shutdown), send SIGTERM to child, wait up to `shutdownTimeout`, then SIGKILL

**For `instances > 1`**: the manager spawns N goroutines for that process definition. Each follows the same lifecycle independently. They share the same name in logs but are distinguished by instance index in internal tracking (e.g., `"Laravel Queue Worker/0"` through `"Laravel Queue Worker/7"`). The process is considered "running" for `dependsOn` purposes when **at least one** instance is running.

### 4.3 Signal handling (`signals.go`)

**Always (both PID 1 and regular process):**
- Trap `SIGTERM`, `SIGINT` using `signal.Notify`
- On receipt: cancel the `errgroup` context, which triggers graceful shutdown of all processes
- Forward `SIGTERM` to all child processes
- Wait up to `shutdownTimeout` for children to exit
- After timeout: send `SIGKILL` to any remaining children
- Exit with code 143 (SIGTERM) or 130 (SIGINT) to indicate signal-caused exit

**PID 1 only (detected via `os.Getpid() == 1`):**
- Enable a zombie reaping loop: periodically call `syscall.Wait4(-1, ...)` with `WNOHANG` to collect orphaned child processes
- This runs in a dedicated goroutine for the lifetime of the program
- This is critical because in a container, PID 1 is the only process that can reap zombies
- On non-PID-1 systems (local dev), this goroutine is not started — the OS init system handles zombie reaping

### 4.4 Exponential backoff (`backoff.go`)

- Implements configurable exponential backoff for process restarts
- Parameters: `initialDelay`, `maxDelay`, `multiplier` (from config, with defaults)
- Formula: `delay = min(initialDelay * multiplier^attempt, maxDelay)`
- Add jitter: ±10% random variation to prevent thundering herd
- Reset the backoff counter after a process has been running successfully for longer than `maxDelay` (indicates stability)
- Respect context cancellation — if shutdown is triggered during a backoff wait, exit immediately

---

## Step 5: Logging (`internal/logging/`)

### 5.1 Multiplexed writer (`writer.go`)

Create a custom `io.Writer` implementation per process that:

1. **Writes to gonner's stdout**: each line prefixed with timestamp and process name
   - Format: `[2026-03-10T12:00:00Z] [NGINX] <original line>`
   - For multi-instance processes: `[2026-03-10T12:00:00Z] [Queue Worker/3] <original line>`
2. **Writes to the configured `logFile`**: raw output without prefix
   - Create parent directories if they don't exist (`os.MkdirAll`)
   - Open file in append mode

**Gonner's own operational messages** (startup, shutdown, restarts, condition evaluation) are written to stderr with prefix `[gonner]`:
- Format: `[2026-03-10T12:00:00Z] [gonner] Starting process "NGINX"...`

### 5.2 Line buffering

- Process output is read line-by-line (using `bufio.Scanner`) to ensure prefixes are applied per line, not mid-line
- Each process's stdout and stderr are both piped through the same writer (combined output)

---

## Step 6: HTTP Health Endpoint (`internal/health/`)

### 6.1 Server (`server.go`)

- **Opt-in**: only starts if `health.port` is configured in the config file
- Also configurable via `--health-port` CLI flag or `GONNER_HEALTH_PORT` environment variable (CLI flag > env var > config file)
- Runs in its own goroutine, uses the same context as the process manager for graceful shutdown
- Listens on `0.0.0.0:<port>`

### 6.2 Endpoints (`handlers.go`)

**`GET /health`**
- Returns `200 OK` with `{"status": "healthy"}` when gonner is running normally
- Returns `503 Service Unavailable` with `{"status": "shutting_down"}` when shutdown has been triggered
- Useful as a Docker `HEALTHCHECK` or Kubernetes liveness probe

**`GET /status`**
- Returns detailed JSON with per-process status:
  - `uptime` — gonner uptime
  - `mode` — parallel or sequential
  - `pid` — gonner's own PID
  - `processes[]` — array with: `name`, `status`, `pid`, `instances`, `runningInstances`, `restarts`, `uptime`, `critical`
- Process status values: `pending`, `starting`, `running`, `stopping`, `stopped`, `failed`, `skipped`

---

## Step 7: CLI (`cmd/gonner/`)

### 7.1 Subcommands

Use `cobra` for subcommand handling:

**`gonner run`** (default command — invoked when no subcommand is given)
- Starts the process manager
- Flags:
  - `--config`, `-c` — path to config file or directory (overrides auto-discovery)
  - `--health-port` — override health endpoint port
  - `--log-level` — `debug`, `info` (default), `warn`, `error`
- This is the default subcommand so `ENTRYPOINT ["gonner"]` in Dockerfile just works

**`gonner status`**
- Queries a running gonner instance's HTTP health endpoint
- Flags:
  - `--host` — host to query (default: `127.0.0.1`)
  - `--port`, `-p` — port to query (default: reads from config auto-discovery, then falls back to `8089`)
- Prints a formatted table to the terminal

**`gonner version`**
- Prints version, commit hash, build date, Go version
- Example: `gonner v1.0.0 (commit abc1234, built 2026-03-10, go1.22.1)`

**`gonner validate`**
- Loads and validates the config file without starting any processes
- Useful for CI/CD pipelines to catch config errors before deployment
- Reports: config path found, parse success, validation results (unique names, DAG check, condition types valid)

### 7.2 Container entrypoint usage

In a Dockerfile:
```dockerfile
COPY gonner /usr/local/bin/gonner
COPY gonner.json /etc/gonner/gonner.json
ENTRYPOINT ["gonner"]
```

Gonner auto-discovers `/etc/gonner/gonner.json` (priority 4) and starts as PID 1.

---

## Step 8: Build & Distribution

### 8.1 Taskfile.yml

```yaml
version: '3'

vars:
  VERSION:
    sh: git describe --tags --always --dirty
  COMMIT:
    sh: git rev-parse --short HEAD
  BUILD_DATE:
    sh: date -u +%Y-%m-%dT%H:%M:%SZ
  LDFLAGS: >-
    -s -w
    -X main.version={{.VERSION}}
    -X main.commit={{.COMMIT}}
    -X main.buildDate={{.BUILD_DATE}}

tasks:
  build:
    cmds:
      - CGO_ENABLED=0 go build -ldflags "{{.LDFLAGS}}" -o bin/gonner ./cmd/gonner/
  test:
    cmds:
      - go test ./... -v -race -cover
  lint:
    cmds:
      - golangci-lint run ./...
  release:
    cmds:
      - GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "{{.LDFLAGS}}" -o bin/gonner-linux-amd64 ./cmd/gonner/
      - GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "{{.LDFLAGS}}" -o bin/gonner-linux-arm64 ./cmd/gonner/
      - GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "{{.LDFLAGS}}" -o bin/gonner-darwin-amd64 ./cmd/gonner/
      - GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "{{.LDFLAGS}}" -o bin/gonner-darwin-arm64 ./cmd/gonner/
  clean:
    cmds:
      - rm -rf bin/
```

### 8.2 Static binary

- Always build with `CGO_ENABLED=0` for a fully static binary
- Use `-ldflags "-s -w"` to strip debug info and reduce binary size
- Target: < 10MB binary

### 8.3 CI (GitHub Actions)

- On push/PR: `task lint`, `task test`
- On tag: `task release`, upload binaries to GitHub release

---

## Verification Plan

### Unit tests
- **Config parsing**: valid JSON, valid YAML, env interpolation with/without defaults, missing env var error, invalid syntax
- **Config validation**: duplicate names, missing `dependsOn` target, DAG cycle detection, invalid field values
- **Config discovery**: verify priority order, JSON-before-YAML at each level, error message when no config found
- **Condition evaluation**: env var matching (set, unset, wrong value), file existence (exists, missing)
- **Backoff calculation**: verify exponential progression, max cap, jitter range, reset after stability

### Integration tests
- **Process lifecycle**: start a simple command (`echo hello`), verify it reaches `running` then `stopped`
- **Auto-restart**: start a command that exits immediately, verify restart with backoff, verify `maxRetries` limit
- **Critical process**: start two processes (one critical), kill the critical one, verify all processes shut down
- **Dependencies**: process B depends on A, verify B waits until A is running
- **Sequential mode**: verify processes start one at a time in order
- **Conditions**: set/unset env vars, verify processes are started/skipped
- **`commandsBefore`**: verify pre-commands run before main command; verify `continueOnError` behavior
- **Signal forwarding**: send SIGTERM to gonner, verify children receive it and exit
- **Health endpoint**: start gonner with health config, query `/health` and `/status`, verify JSON responses

### Manual testing
- Build binary, run with the example Laravel config in a Docker container
- Set various env vars to enable/disable processes
- Verify `docker logs` shows multiplexed output with process name prefixes
- Send `docker stop` (SIGTERM), verify graceful shutdown within timeout
- Run `gonner status` from another terminal to verify CLI status output
- Run `gonner validate` against valid and invalid config files

---

## Decisions Log

| Decision | Choice | Rationale |
|----------|--------|-----------|
| PID 1 behavior | Auto-detect via `os.Getpid() == 1` | Works in containers and locally without mode flags |
| Concurrency | Goroutines + `errgroup` + `context.Context` | Idiomatic Go, built-in cancellation propagation |
| Critical processes | Per-process `critical` flag | Flexible — NGINX might be critical, queue workers might not be |
| `commandsBefore` failure | Per-command `continueOnError` flag (default: `false`) | Allows migrations to fail-fast while allowing optional setup commands |
| Config format | JSON + YAML, auto-detected by extension | YAML is nicer for humans, JSON for machines |
| Config discovery | Flag > CWD > XDG user > system-wide | Standard convention, supports both dev and production |
| Env interpolation | `{{env://VAR_NAME}}` syntax | Avoids ambiguity with shell `$VAR` or docker `${VAR}` syntax |
| Startup mode | Global `mode` field: `parallel` or `sequential` | Simple toggle; `dependsOn` adds fine-grained control within parallel mode |
| Instance identity | No instance IDs exposed | Kept simple; instances are fully identical |
| Health endpoint | Opt-in via config, CLI flag, or env var | Not all deployments need it; avoids unexpected port binding |
| Restart strategy | Configurable exponential backoff + max retries | Prevents restart storms while allowing persistent services |
| Build runner | Taskfile (not Makefile) | Modern, YAML-based, cross-platform, readable |
| Distribution | Static Go binary | Single artifact, no runtime dependencies, easy to COPY into Docker images |
| CLI framework | `cobra` | Cobra gives subcommands cheaply with good UX |

---

## Example Configuration (Full)

```json
{
    "mode": "parallel",
    "shutdownTimeout": "30s",
    "health": {
        "port": 8089
    },
    "run": [
        {
            "name": "NGINX",
            "command": "nginx",
            "logFile": "/var/log/gonner-nginx.log",
            "critical": true,
            "whenAll": {
                "env": "LARAVEL_WEBSERVER=TRUE"
            },
            "commandsBefore": [
                {
                    "workDir": "/app",
                    "command": "php artisan migrate",
                    "continueOnError": false
                }
            ]
        },
        {
            "name": "Laravel Scheduler",
            "workDir": "/app",
            "command": "php artisan schedule:work",
            "autoRestart": true,
            "maxRetries": 10,
            "backoff": {
                "initialDelay": "2s",
                "maxDelay": "60s",
                "multiplier": 2.0
            },
            "logFile": "/var/log/gonner-scheduler.log",
            "whenAll": {
                "env": "LARAVEL_SCHEDULER=TRUE"
            }
        },
        {
            "name": "Laravel Queue Worker",
            "workDir": "/app",
            "command": "php artisan queue:work",
            "autoRestart": true,
            "maxRetries": 0,
            "instances": 8,
            "logFile": "/var/log/gonner-queues.log",
            "dependsOn": ["NGINX"],
            "whenAll": {
                "env": "LARAVEL_QUEUE_WORKER=TRUE"
            }
        }
    ]
}
```

---

## Implementation Order (Suggested)

1. **Project scaffold** — module, directory structure, Taskfile, main.go stub
2. **Config parsing** — structs, JSON/YAML loading, env interpolation, validation
3. **Condition engine** — interface, env condition, file condition, registry
4. **Logging** — multiplexed writer with prefix and timestamp
5. **Process runner** — single process lifecycle (start, monitor, restart, backoff)
6. **Process manager** — multi-process orchestration, parallel/sequential modes, dependency resolution
7. **Signal handling** — SIGTERM/SIGINT trapping, forwarding, PID 1 zombie reaping
8. **Health endpoint** — HTTP server, /health, /status
9. **CLI** — cobra setup, `run`, `status`, `version`, `validate` subcommands
10. **Testing** — unit tests per package, integration tests for end-to-end scenarios
