# Changelog

All notable changes to this project are documented in this file. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
