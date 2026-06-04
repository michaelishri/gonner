# Operations: Health, Status, Metrics

## Health endpoint

Enabled by setting `health.port` in the config, or by passing `--health-port` to `gonner run`, or by exporting `GONNER_HEALTH_PORT`.

### `GET /health`

A liveness probe. Always unauthenticated.

| State | Response |
|---|---|
| Running normally | `200 OK` with `{"status":"healthy"}` |
| Shutting down | `503 Service Unavailable` with `{"status":"shutting_down"}` |

Use this for Kubernetes liveness probes and Docker `HEALTHCHECK`.

### `GET /ready`

A readiness probe. Always unauthenticated.

| State | Response |
|---|---|
| Not shutting down and all `critical` processes running | `200 OK` with `{"status":"ready"}` |
| Shutting down, or a `critical` process is not running | `503 Service Unavailable` with `{"status":"not_ready"}` |

Use this for Kubernetes **readiness** probes so traffic is only routed once critical dependencies are up. If no `critical` processes are defined, `/ready` mirrors `/health`.

### `GET /status`

Detailed per-process status. Requires `Authorization: Bearer <token>` if `health.authToken` is set.

```json
{
  "uptime": "2h30m12s",
  "mode": "parallel",
  "pid": 1,
  "processes": [
    {
      "name": "web",
      "status": "running",
      "pid": 42,
      "instances": 1,
      "runningInstances": 1,
      "restarts": 0,
      "uptime": "2h30m0s",
      "critical": true
    },
    {
      "name": "queue",
      "status": "running",
      "pid": 67,
      "instances": 8,
      "runningInstances": 8,
      "restarts": 2,
      "uptime": "1h15m0s",
      "critical": false
    }
  ]
}
```

### `gonner status` CLI

Pretty-prints `/status` as a table. Pass `--token` (or set `GONNER_HEALTH_TOKEN`) for authenticated endpoints, and `--tls` (optionally `--insecure`) for HTTPS.

```
$ gonner status --port 8089
Gonner PID 1 | Mode: parallel | Uptime: 2h30m12s

NAME       STATUS    PID   INSTANCES   RESTARTS   UPTIME
web        running   42    1/1         0          2h30m0s
queue      running   67    8/8         2          1h15m0s
```

---

## `/metrics` (Prometheus)

Opt-in via `health.metrics: true`. Authenticated if `authToken` is set. Text exposition format v0.0.4.

Exposed metrics:

| Metric | Type | Description |
|---|---|---|
| `gonner_uptime_seconds` | gauge | Seconds since gonner started. |
| `gonner_shutting_down` | gauge | 1 if shutdown is in progress. |
| `gonner_ready` | gauge | 1 if gonner is ready (all critical processes running). |
| `gonner_process_running_instances{name}` | gauge | Currently-running instance count. |
| `gonner_process_configured_instances{name}` | gauge | Configured instance count. |
| `gonner_process_restarts_total{name}` | counter | Aggregate restarts across all instances. |
| `gonner_process_up{name,critical}` | gauge | 1 if at least one instance is running. |

Sample scrape:

```
# HELP gonner_uptime_seconds Time since gonner started.
# TYPE gonner_uptime_seconds gauge
gonner_uptime_seconds 9012
# HELP gonner_process_up 1 if the process is in the running state.
# TYPE gonner_process_up gauge
gonner_process_up{name="web",critical="true"} 1
gonner_process_up{name="queue",critical="false"} 1
```

### Alerting suggestions

```
- alert: GonnerCriticalProcessDown
  expr: gonner_process_up{critical="true"} == 0
  for: 1m
  labels: { severity: page }
- alert: GonnerHighRestartRate
  expr: rate(gonner_process_restarts_total[5m]) > 0.1
  for: 10m
  labels: { severity: warn }
- alert: GonnerShuttingDown
  expr: gonner_shutting_down == 1
  for: 2m
```

---

## Logs

Process output is written to **two** sinks:

1. **gonner's stdout**, with the line prefixed `[<RFC3339 UTC>] [<process>] <line>`. Multi-instance processes use `[name/index]`.
2. **The configured `logFile`**, raw (no prefix), append-only, mode `0o600` by default. Rotated by size if `logRotate` is configured.

Gonner's own operational events (startup, condition results, restarts, shutdown) go to **stderr** with the prefix `[gonner]`.

### Log lifecycle

- The line scanner buffers up to 1 MiB per line.
- Each `Write` is atomic per Writer (mutex-protected).
- Rotation closes and renames the file, optionally gzips it, prunes excess backups, then reopens.

### Shipping logs

For Docker, the docker logging driver receives both gonner's stdout and stderr — no extra wiring is needed for `docker logs`. For Kubernetes, the kubelet captures the same streams.

If you want to forward logs from the configured `logFile`, mount it onto a sidecar (Fluent Bit, Vector) and have that read & rotate independently. In that case set `logRotate: null` on gonner.
