# Gonner

A lightweight, PID-1-aware process manager for containers. Single static Go binary, no runtime dependencies. Designed to be a Docker `ENTRYPOINT` that orchestrates multiple long-running services, but works equally well for local development.

[![Go Version](https://img.shields.io/badge/go-1.25%2B-blue)](go.mod)
[![License](https://img.shields.io/badge/license-MIT-green)](LICENSE)

---

## Highlights

- **PID 1 aware** — auto-detects container context, performs SIGCHLD-driven zombie reaping without racing with `os/exec`.
- **Signal forwarding** — SIGTERM/SIGINT trigger graceful shutdown; SIGHUP/SIGUSR1/SIGUSR2 are forwarded to children.
- **Per-process privilege dropping** — `user`/`group` (when started as root).
- **Conditional startup** — environment variables, file existence, TCP port reachability, or arbitrary shell commands.
- **Dependency graph** — `dependsOn` with cycle detection; `parallel` or `sequential` startup modes.
- **Multi-instance processes** — run N identical copies of a worker.
- **Configurable restart policy** — exponential backoff with jitter, max-retry caps, post-stability reset.
- **Customizable shutdown** — per-process `stopSignal` and `stopTimeout`.
- **Built-in log multiplexing** — process stdout/stderr prefixed on gonner's stdout and (optionally) appended to a private log file with size-based rotation.
- **Health endpoint** — opt-in HTTP API exposing `/health` (liveness), `/ready` (readiness), `/status`, and optional `/metrics` (Prometheus text format) with bearer-token auth and TLS support (min TLS 1.2).
- **Container-friendly defaults** — secure log file mode `0o600`, HTTP timeouts, panic-safe goroutines.

---

## Documentation

| Topic | Document |
|-------|----------|
| Install & quickstart | this file |
| Full configuration reference | [docs/configuration.md](docs/configuration.md) |
| Deployment patterns | [docs/deployment.md](docs/deployment.md) |
| Security model & hardening | [docs/security.md](docs/security.md) |
| Operations / health / metrics | [docs/operations.md](docs/operations.md) |
| Architecture overview | [docs/architecture.md](docs/architecture.md) |
| Troubleshooting | [docs/troubleshooting.md](docs/troubleshooting.md) |

---

## Quickstart

### Install

Download a prebuilt binary from [Releases](https://github.com/michaelishri/gonner/releases), or build from source:

```bash
# requires Go 1.25+
go install github.com/michaelishri/gonner/cmd/gonner@latest
```

Or from a checkout (requires [Task](https://taskfile.dev/)):

```bash
task build         # writes bin/gonner
```

### Minimal config

Create `gonner.json` in your project directory:

```json
{
  "run": [
    { "name": "web",       "command": "nginx",                      "critical": true },
    { "name": "scheduler", "command": "php artisan schedule:work",  "autoRestart": true },
    { "name": "queue",     "command": "php artisan queue:work",     "autoRestart": true, "instances": 4, "dependsOn": ["web"] }
  ]
}
```

### Run

```bash
gonner
```

Gonner auto-discovers `gonner.json` in the working directory, starts the processes, and forwards container signals.

### Docker

```dockerfile
FROM alpine
COPY gonner /usr/local/bin/gonner
COPY gonner.json /etc/gonner/gonner.json
ENTRYPOINT ["gonner"]
```

Gonner picks up `/etc/gonner/gonner.json` automatically (see [discovery order](docs/configuration.md#config-discovery)).

---

## CLI Reference

```
gonner                              # alias for `gonner run`
gonner run    [--config PATH] [--health-port N] [--health-bind ADDR]
gonner validate [--config PATH]    # validate without starting anything
gonner status [--host H] [--port N] [--token T] [--tls] [--insecure]
gonner version
```

See `gonner <cmd> --help` for full flag listings, or [docs/configuration.md](docs/configuration.md).

---

## Production Checklist

Before shipping a container with gonner:

- [ ] Set `health.bindAddr` to `127.0.0.1` (or behind a reverse proxy) unless the endpoint is needed externally.
- [ ] If exposing the health endpoint, set `health.authToken` (via `GONNER_HEALTH_TOKEN` env var; min 16 chars).
- [ ] Enable `/metrics` only on a private network.
- [ ] Drop privileges per process with `user`/`group` if gonner runs as root.
- [ ] Configure `logRotate` for any `logFile` you write.
- [ ] Mark exactly one process `critical: true` so a crash takes down the container (lets your orchestrator restart it cleanly).
- [ ] Set realistic `shutdownTimeout` / per-process `stopTimeout` for slow-draining services.
- [ ] Run `gonner validate` in CI.

See [docs/security.md](docs/security.md) for the full hardening guide.

---

## License

MIT
