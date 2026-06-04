# Configuration Reference

Gonner is configured by a single JSON or YAML file. This document is the canonical reference for every field.

## Table of Contents

- [Config discovery](#config-discovery)
- [Environment variable interpolation](#environment-variable-interpolation)
- [Top-level fields](#top-level-fields)
- [`health`](#health)
- [`run[]` — process definitions](#run--process-definitions)
- [`backoff`](#backoff)
- [`commandsBefore[]`](#commandsbefore)
- [`logRotate`](#logrotate)
- [Conditions: `whenAll` / `whenAny`](#conditions-whenall--whenany)
- [CLI](#cli)
- [Environment variable overrides](#environment-variable-overrides)

---

## Config discovery

When `gonner run` is invoked, the binary searches for a config file in this order. **First match wins.**

| Priority | Source | Paths |
|---|---|---|
| 1 | `--config` flag | The exact path. If it's a directory, gonner looks for `gonner.json` then `gonner.yaml` inside it. |
| 2 | Current working directory | `./gonner.json`, then `./gonner.yaml` |
| 3 | XDG user config | `$XDG_CONFIG_HOME/gonner/gonner.{json,yaml}`, falling back to `~/.config/gonner/...` |
| 4 | System-wide | `/etc/gonner/gonner.{json,yaml}` |

If no file is found, gonner exits with the list of paths searched.

JSON is preferred over YAML at every level. The selected path is logged at startup.

---

## Environment variable interpolation

Any **string value** in the config may reference an environment variable using `{{env://VAR}}` or `{{env://VAR:default}}` syntax. Interpolation is applied **before** parsing, so it works inside any string field — including command lines, paths, and tokens.

```json
{
  "run": [
    {
      "name": "worker",
      "command": "php artisan queue:work --queue={{env://QUEUE_NAME:default}}",
      "env": { "QUEUE_TIMEOUT": "{{env://QUEUE_TIMEOUT:60}}" }
    }
  ]
}
```

If a referenced variable is unset **and** no default is provided, gonner exits at startup with `missing required environment variables: VAR`.

The interpolation regex is `\{\{env://([A-Za-z_][A-Za-z0-9_]*)(:[^}]*)?\}\}`.

> **Security note:** Because the substitution happens before parsing, an env var that contains JSON metacharacters or a shell metacharacter could change the config structure or the command line. Treat env vars used in interpolation as trusted input. See [security.md](security.md).

---

## Top-level fields

| Field | Type | Default | Description |
|---|---|---|---|
| `mode` | string | `"parallel"` | `"parallel"` (start all, respecting `dependsOn`) or `"sequential"` (start one at a time in config order). |
| `shutdownTimeout` | duration | `"30s"` | How long to wait for processes to exit before sending SIGKILL. Parsed by Go's `time.ParseDuration` (e.g. `"500ms"`, `"2m"`). |
| `pidFile` | string | — | Optional path to write gonner's PID on startup; removed at shutdown. |
| `health` | object | — | Optional HTTP health endpoint. See [`health`](#health). |
| `run` | array | — | Process definitions. Required, must contain at least one entry. |

---

## `health`

The health endpoint is **opt-in**. Set `health.port` to enable it.

| Field | Type | Default | Description |
|---|---|---|---|
| `port` | int | — | Required. TCP port to listen on. |
| `bindAddr` | string | `"0.0.0.0"` | Bind address. **Set to `"127.0.0.1"` for localhost-only.** |
| `authToken` | string | — | If set, `/status` and `/metrics` require `Authorization: Bearer <token>`. `/health` is always public. May be overridden by `GONNER_HEALTH_TOKEN` env var (preferred for secrets). |
| `metrics` | bool | `false` | Enable Prometheus-compatible `/metrics`. |
| `tls` | object | — | TLS settings — `{ "certFile": "...", "keyFile": "..." }`. When set, the server speaks HTTPS with a minimum protocol version of TLS 1.2. |

Endpoints:

- `GET /health` — unauthenticated **liveness** probe; returns `200 {"status":"healthy"}` while running, `503 {"status":"shutting_down"}` during shutdown. Use as a Docker `HEALTHCHECK` or Kubernetes liveness probe.
- `GET /ready` — unauthenticated **readiness** probe; returns `200 {"status":"ready"}` only when gonner is not shutting down **and** every `critical` process has a running instance, otherwise `503 {"status":"not_ready"}`. Use as a Kubernetes readiness probe. If no `critical` processes are defined, readiness tracks liveness.
- `GET /status` — full per-process detail (uptime, PID, restart count, etc.). Authenticated if `authToken` is set.
- `GET /metrics` — Prometheus text format. Only if `metrics: true`. Authenticated if `authToken` is set.

---

## `run[]` — process definitions

Each entry defines a managed process.

| Field | Type | Default | Description |
|---|---|---|---|
| `name` | string | required | Unique within the file. Used in logs, `dependsOn`, status output. |
| `command` | string | required | Executed via `sh -c`. |
| `workDir` | string | inherit | Working directory. |
| `env` | object | — | Extra environment variables: `{ "KEY": "VALUE" }`. Merged on top of gonner's environment. |
| `user` | string | — | Username or numeric UID to drop to before exec. **Requires gonner to start as root.** |
| `group` | string | — | Group name or GID. Requires `user`. |
| `logFile` | string | — | Path to append raw process output. Parents are created (`0o750`). |
| `logFileMode` | int | `0o600` | POSIX mode bits for the log file (e.g. `0o600`, `0o640`). |
| `logRotate` | object | — | Size-based rotation. See [`logRotate`](#logrotate). |
| `autoRestart` | bool | `false` | Restart on non-zero exit (and on zero exit too if true? — no: clean exit ends the lifecycle). |
| `maxRetries` | int | `0` | Cap on restarts. `0` = unlimited (only applies if `autoRestart` is true). |
| `backoff` | object | defaults | See [`backoff`](#backoff). |
| `instances` | int | `1` | Number of identical copies. Each instance has its own PID, log prefix, and restart counter. |
| `critical` | bool | `false` | Unexpected exit triggers full shutdown of the entire gonner process tree. |
| `dependsOn` | []string | `[]` | Process names that must be Running before this one starts. |
| `whenAll` | array | — | See [Conditions](#conditions-whenall--whenany). |
| `whenAny` | array | — | See [Conditions](#conditions-whenall--whenany). |
| `commandsBefore` | array | `[]` | Pre-start commands. See [`commandsBefore`](#commandsbefore). |
| `stopSignal` | string | `"SIGTERM"` | Signal sent on shutdown. One of `SIGTERM`, `SIGINT`, `SIGHUP`, `SIGQUIT`, `SIGUSR1`, `SIGUSR2`, `SIGKILL`. |
| `stopTimeout` | duration | global `shutdownTimeout` | Per-process override of how long to wait before escalating to SIGKILL. |

### Lifecycle states

A process moves through: `pending → starting → running → stopping → stopped` (or `failed`). Skipped processes (conditions evaluated false) are `skipped`.

### Multi-instance processes

When `instances > 1`, gonner spawns N goroutines, each with its own PID and restart counter. Log prefixes are suffixed with `/INDEX` (e.g. `[queue/3]`). A process is considered "running" for `dependsOn` resolution as soon as **any one** instance reaches `running`.

---

## `backoff`

Exponential backoff applied between restart attempts.

| Field | Type | Default | Description |
|---|---|---|---|
| `initialDelay` | duration | `"1s"` | Delay before the first restart. |
| `maxDelay` | duration | `"30s"` | Cap on the delay. |
| `multiplier` | float | `2.0` | Applied after each restart. |

Jitter of ±10% is added to each delay to avoid thundering herds. The counter resets after the process has stayed running for longer than `maxDelay`.

---

## `commandsBefore[]`

Sequentially executed pre-start commands. Their stdout/stderr stream to the same log writer as the main process.

| Field | Type | Default | Description |
|---|---|---|---|
| `command` | string | required | Shell command. |
| `workDir` | string | parent's `workDir` | Working directory. |
| `continueOnError` | bool | `false` | If `false`, a non-zero exit marks the process `failed` and the main command never starts. If `true`, a warning is logged and gonner continues. |

`commandsBefore` inherits the parent process's `env`, `user`, and `group`.

---

## `logRotate`

Size-based log rotation. Applies only when `logFile` is set.

| Field | Type | Default | Description |
|---|---|---|---|
| `maxSizeMB` | int | `0` (disabled) | Maximum size in megabytes before rotating. |
| `maxBackups` | int | `0` (unlimited) | Max number of rotated files to retain. |
| `compress` | bool | `false` | Gzip rotated files. |

Rotated files are named `<logFile>.<UTC-timestamp>` (e.g. `app.log.20260310T120000Z`); compressed files get a `.gz` suffix. Backup pruning keeps the most recent `maxBackups` files lexicographically.

For long-term storage and search, consider shipping logs to your centralized logging stack instead of relying on rotation alone.

---

## Conditions: `whenAll` / `whenAny`

Conditions decide whether a process starts. They evaluate **once**, at startup (after env interpolation).

- `whenAll`: every condition must be true.
- `whenAny`: at least one condition must be true.
- If both are provided, both blocks must independently pass (AND).
- If neither is provided, the process always starts.

Each block is an **array** of single-key `{ type: value }` objects. Because it is an
array, you can repeat the same condition type as many times as you need:

```json
"whenAll": [
    { "env": "QUEUE_WORKER=true" },
    { "portOpen": "redis:6379@3s" },
    { "fileExists": "/etc/app/ready" }
]
```

```yaml
whenAll:
  - env: "QUEUE_WORKER=true"
  - portOpen: "redis:6379@3s"
  - fileExists: /etc/app/ready
```

`gonner validate` rejects unknown condition types and empty condition values, so
typos are caught before deployment.

### Built-in condition types

| Type | Value format | Description |
|---|---|---|
| `env` | `"KEY=VALUE"` or `"KEY"` | Compare env var to VALUE; or check it is set and non-empty. |
| `fileExists` | path | True if the path exists (file or directory). |
| `portOpen` | `[host:]port[@timeout]` | True if a TCP dial succeeds. Default host `127.0.0.1`, default timeout `1s`. Example: `db:5432@2s`. |
| `commandSucceeds` | shell command | True if `sh -c "<command>"` exits 0 within 10s. |

### Adding custom conditions

Custom condition types can be registered programmatically (`condition.Register(name, factory)`) when embedding gonner as a library.

---

## CLI

### `gonner run` (default)

Starts the process manager.

| Flag | Default | Description |
|---|---|---|
| `--config`, `-c` | auto-discover | Config file or directory. |
| `--health-port` | from config | Override health endpoint port. |
| `--health-bind` | from config | Override health endpoint bind address. |

### `gonner validate`

Loads, interpolates, parses, and validates the config without starting any process. Exits non-zero on any error. Warnings are printed but do not fail validation. Suitable for CI.

| Flag | Default | Description |
|---|---|---|
| `--config`, `-c` | auto-discover | Config file or directory. |

### `gonner status`

Queries a running gonner instance's `/status` endpoint and prints a formatted table.

| Flag | Default | Description |
|---|---|---|
| `--host` | `127.0.0.1` | Host to query. |
| `--port`, `-p` | `8089` | Port to query (overridden by `GONNER_HEALTH_PORT`). |
| `--token`, `-t` | from `GONNER_HEALTH_TOKEN` | Bearer token for authenticated endpoints. |
| `--tls` | `false` | Query over HTTPS instead of HTTP. |
| `--insecure` | `false` | Skip TLS certificate verification (use with `--tls` for self-signed certs). |

To query an authenticated endpoint, pass `--token` or set `GONNER_HEALTH_TOKEN` in the environment of the `gonner status` invocation:

```bash
gonner status --token "$GONNER_HEALTH_TOKEN"
gonner status --tls --token "$GONNER_HEALTH_TOKEN"        # HTTPS endpoint
gonner status --tls --insecure --token "$TOKEN"           # self-signed cert
```

### `gonner version`

Prints version, commit hash, build date, and Go version.

---

## Environment variable overrides

| Variable | Effect |
|---|---|
| `GONNER_HEALTH_PORT` | Overrides `health.port` for both `run` and `status`. |
| `GONNER_HEALTH_BIND` | Overrides `health.bindAddr` for `run`. |
| `GONNER_HEALTH_TOKEN` | Overrides `health.authToken` for `run`. **Preferred for secrets** — keeps them out of the config file. |
| `XDG_CONFIG_HOME` | Affects discovery priority 3. |

Order of precedence for these settings: **CLI flag > environment variable > config file > built-in default**.
