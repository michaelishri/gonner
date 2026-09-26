# Changelog

All notable changes to this project are documented in this file. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## v0.1.0

### Security

- Resolve all 25 findings from the codebase audit, with permanent regression coverage and a resolution matrix in `docs/audits/2026-09-25-codebase-audit.md`.
- Validate log and PID paths through retained directory descriptors; reject unsafe ownership, writable ancestors, leaf symlinks, hardlinks, special files, and unsafe ACLs before workloads start.
- Resolve numeric users through account lookup, require a group for unknown UIDs, clear supplementary groups, and check Linux capabilities including `CAP_KILL` for cross-UID supervision.
- Raise the Go baseline to 1.26.7 and scan source plus all release binaries with pinned govulncheck v1.8.0 before publication.
- Parse release labels as JSON environment data instead of interpolating them into shell commands.

### Fixed

- Give every managed child exactly one wait owner; coordinate Linux PID 1 orphan reaping with the shared execution service used by main commands, precommands, and command conditions.
- Treat macOS groups containing only exited processes as stopped while preserving signal permission errors for live processes.
- Preserve oversized and unterminated output, drain pipes after child exit, share log sinks across instances, prevent backup collisions, and keep console output flowing after file errors.
- Propagate unavailable dependencies without hangs and allow any successfully started instance to satisfy the initial dependency gate.
- Report permanent failures with exit 1, cancel all work for critical failures including required precommands, and allow unrelated work to continue after noncritical failures.
- Correct restart counts, backoff stability tracking, and process status while waiting to restart.
- Validate TLS certificates before binding or starting workloads, propagate unexpected health-server failures, and protect uptime reads against races.
- Include pending and skipped critical processes in readiness and status, normalize bare ports in TCP conditions, and honor an explicit `gonner status --port 8089` over environment defaults.

### Changed

- **Migration required:** custom conditions implement `Evaluate(context.Context) (bool, error)`; command conditions inherit process credentials, environment, and working directory and terminate their group on cancellation or timeout.
- **Migration required:** strict path and privilege validation can reject previously accepted configurations; sequential forward dependencies are rejected.
- Oversized console lines are split into prefixed 64 KiB chunks. Raw files remain byte-exact, and legacy rotated files are preserved rather than pruned.
- Release publication requires native Linux and macOS verification, Docker PID 1 and privilege-boundary tests, and vulnerability scans. Binaries cover Linux/macOS on amd64 and arm64.
- See `docs/operations.md#upgrade-from-the-audited-baseline` before upgrading.

## v0.0.3

### Added

- Nine isolated shutdown regression cases covering signals, shell-wrapped children, timeout escalation, startup commands, and critical-process failure.

### Fixed

- SIGTERM and SIGINT now honor the configured `stopSignal` and `stopTimeout` for the entire process group instead of immediately killing the directly launched process.
- Descendants are stopped even after their original shell exits, including when a command exits normally or is about to restart.
- `commandsBefore` now uses the same graceful process-group shutdown as main commands, and its log readers finish before the log writer closes.
- Cancellation during sequential startup now waits for already-started processes to finish shutting down.
- Shutdown status now updates when cancellation begins, and cancelled startup commands are reported as stopped rather than failed.

## v0.0.2-alpha

### Added

- **`GET /ready` readiness probe** on the health endpoint — returns `200` only when gonner is not shutting down and every `critical` process is running; `503` otherwise. Distinct from the `/health` liveness probe. Exposed as the `gonner_ready` Prometheus gauge.
- **`gonner status` authentication & TLS** — new `--token`/`-t` (falls back to `GONNER_HEALTH_TOKEN`), `--tls`, and `--insecure` flags for querying authenticated and HTTPS health endpoints.
- **Per-process privilege dropping** via `user` / `group` (requires gonner to start as root).
- **Per-process `stopSignal`** (defaults to `SIGTERM`; accepts `SIGTERM|SIGINT|SIGHUP|SIGQUIT|SIGUSR1|SIGUSR2|SIGKILL`).
- **Per-process `stopTimeout`** override of the global `shutdownTimeout`.
- **Log rotation** — size-based with optional gzip and retention (`logRotate.{maxSizeMB,maxBackups,compress}`).
- **Configurable log file mode** (`logFileMode`, default `0o600`).
- **Health endpoint hardening**:
  - `bindAddr` (default `0.0.0.0`, can restrict to `127.0.0.1`).
  - Bearer-token auth via `authToken` config or `GONNER_HEALTH_TOKEN` env var.
  - HTTP timeouts (`ReadHeaderTimeout=5s`, `Read/Write=15s`, `Idle=60s`, `MaxHeaderBytes=64KiB`) to mitigate Slowloris.
  - TLS support via `health.tls.{certFile,keyFile}`.
- **Prometheus `/metrics` endpoint** (opt-in with `health.metrics: true`).
- **New conditions**: `portOpen` (TCP reachability) and `commandSucceeds` (arbitrary shell check).
- **`--health-bind` CLI flag** to override the health endpoint bind address.
- **`pidFile` top-level config** for sysadmin integration.
- **Panic recovery** in all long-running goroutines.
- **PID 1 reaper coordination** with `os/exec` via `ReapedStatuses` lookup, eliminating the wait4 race.
- Comprehensive documentation in `docs/`.

### Changed

- **`whenAll` / `whenAny` are now arrays** of single-key `{ type: value }` objects
  instead of single objects. This allows multiple conditions of the **same type**
  (e.g. several `portOpen` checks) in one block. *(Breaking change — update existing
  configs from `"whenAll": { "env": "X" }` to `"whenAll": [ { "env": "X" } ]`.)*
- `gonner validate` now rejects unknown condition types and empty condition values
  in `whenAll` / `whenAny`.
- Default log file mode is now `0o600` (was `0o644`).
- Default log directory mode is now `0o750` (was `0o755`).
- Zombie reaper is now SIGCHLD-driven instead of 100ms-polled.
- Health endpoint TLS now enforces a minimum protocol version of TLS 1.2.

### Fixed

- Race in `Process.readyCh` re-creation during restart cycles.
- `cmd.Wait` `ECHILD` recovery when PID-1 zombie reaper claims the child first.
- Unbounded growth of the PID-1 reaper's `ReapedStatuses` map — unclaimed exit statuses from orphaned descendants are now pruned after 30s.
