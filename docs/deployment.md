# Deployment Patterns

Gonner is a single static binary. Deployment is a matter of getting the binary and a config file onto the target.

## Table of contents

- [Docker / OCI containers](#docker--oci-containers)
- [Kubernetes](#kubernetes)
- [systemd](#systemd)
- [Local development](#local-development)
- [Multi-architecture builds](#multi-architecture-builds)

---

## Docker / OCI containers

The canonical use case. Gonner runs as PID 1, reaps zombies, forwards signals.

### Minimal Dockerfile

```dockerfile
FROM alpine:3.20
COPY --chmod=0755 gonner /usr/local/bin/gonner
COPY gonner.json /etc/gonner/gonner.json
ENTRYPOINT ["gonner"]
```

`/etc/gonner/gonner.json` is discovered automatically (priority 4). No `CMD` is needed because `gonner run` is the default.

### Multi-stage build

```dockerfile
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -ldflags '-s -w' -o /out/gonner ./cmd/gonner

FROM alpine:3.20
RUN apk add --no-cache nginx php82 php82-fpm
COPY --from=build /out/gonner /usr/local/bin/gonner
COPY gonner.json /etc/gonner/gonner.json
ENTRYPOINT ["gonner"]
```

### Health check

```dockerfile
HEALTHCHECK --interval=10s --timeout=2s --start-period=15s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8089/health >/dev/null || exit 1
```

Bind the health endpoint to localhost to keep it off the host network:

```json
{ "health": { "port": 8089, "bindAddr": "127.0.0.1" } }
```

### Graceful shutdown

`docker stop` sends SIGTERM, then SIGKILL after `--time` (default 10s). Set `shutdownTimeout` slightly below that:

```bash
docker run --stop-timeout 30 myimage
```

```json
{ "shutdownTimeout": "25s" }
```

---

## Kubernetes

Gonner as a sidecar or as the container's main process.

### Pod spec

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: web
spec:
  terminationGracePeriodSeconds: 35
  containers:
    - name: app
      image: myorg/web:1.2.3
      env:
        - name: GONNER_HEALTH_TOKEN
          valueFrom:
            secretKeyRef: { name: gonner, key: token }
      livenessProbe:
        httpGet:
          path: /health
          port: 8089
        initialDelaySeconds: 5
        periodSeconds: 10
      readinessProbe:
        httpGet:
          path: /ready
          port: 8089
        initialDelaySeconds: 5
        periodSeconds: 10
      ports:
        - containerPort: 8089
          name: gonner
      resources:
        requests: { cpu: 100m, memory: 128Mi }
        limits:   { cpu: 1,    memory: 512Mi }
      securityContext:
        runAsNonRoot: false        # gonner needs root for the zombie reaper if PID 1
        capabilities:
          drop: ["ALL"]
          add:  ["CHOWN", "SETUID", "SETGID"]  # if you use user/group privilege drop
```

Match `terminationGracePeriodSeconds` to (`shutdownTimeout` + buffer).

### Prometheus scrape

```yaml
- job_name: gonner
  authorization:
    type: Bearer
    credentials_file: /etc/prom/gonner-token
  static_configs:
    - targets: ['web:8089']
```

Make sure `health.metrics: true` and the token is wired through.

---

## systemd

For bare-metal or VM deployments.

```ini
# /etc/systemd/system/gonner.service
[Unit]
Description=Gonner process manager
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/gonner run --config /etc/gonner/gonner.json
Restart=on-failure
RestartSec=5
KillSignal=SIGTERM
TimeoutStopSec=45

# Run as root only if you need privilege dropping for children;
# otherwise drop to a dedicated user.
User=gonner
Group=gonner

# Hardening
ProtectSystem=strict
ProtectHome=yes
NoNewPrivileges=true
ReadWritePaths=/var/log/gonner

[Install]
WantedBy=multi-user.target
```

`Type=simple` is correct — gonner is a foreground process. Do **not** use `Type=forking`.

---

## Local development

Most teams use a separate `gonner.dev.json` and select it explicitly:

```bash
gonner run --config gonner.dev.json
```

Recommended dev settings:

- `mode: "sequential"` for predictable startup order during debugging
- `autoRestart: false` so process crashes surface immediately
- `logFile: ""` so output stays on stdout

---

## Multi-architecture builds

The provided `Taskfile.yml` builds for `linux/{amd64,arm64}` and `darwin/{amd64,arm64}`:

```bash
task release
ls bin/
# gonner-darwin-amd64
# gonner-darwin-arm64
# gonner-linux-amd64
# gonner-linux-arm64
```

For multi-arch Docker images, use `docker buildx`:

```bash
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  -t myorg/app:1.0.0 \
  --push .
```
