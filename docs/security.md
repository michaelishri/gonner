# Security Model & Hardening Guide

This document describes gonner's security posture, the trust boundary, and the controls available to operators.

## Trust boundary

Gonner is a process manager: it reads a config file you supply and runs the commands it contains. **The config file is implicitly trusted** — anyone who can write to it can execute arbitrary commands with gonner's privileges. Treat `gonner.json` (and any file it includes via env interpolation) like an init script.

Gonner is not a sandbox. Use kernel features (Linux capabilities, seccomp, Kubernetes `securityContext`, `runuser`/`setcap`) to constrain what child processes can do.

---

## Built-in protections

| Surface | Default | Notes |
|---|---|---|
| HTTP health endpoint | Disabled | Opt-in via `health.port`. |
| Health endpoint timeouts | `ReadHeader=5s`, `Read=15s`, `Write=15s`, `Idle=60s`, `MaxHeaderBytes=64KiB` | Mitigates Slowloris and slow-request DoS. |
| `/status` and `/metrics` | Public unless `authToken` set | Bearer-token check uses constant-time comparison. |
| `/health` and `/ready` | Always public | Probe endpoints expose no secrets (liveness / readiness only). |
| Health endpoint TLS | Opt-in (`health.tls`) | Minimum protocol version is TLS 1.2; weaker versions are rejected. |
| Log file mode | `0o600` | Per-process override via `logFileMode`. |
| Log directory mode | `0o750` | Created by gonner when the parent directory does not exist. |
| Privilege drop | Opt-in (`user`/`group`) | Requires gonner to start as root. |
| Zombie reaping | PID 1 only | SIGCHLD-driven; coordinated with `exec.Cmd.Wait` via `ReapedStatuses`. Unclaimed statuses from orphaned descendants are pruned after 30s to bound memory. |
| Goroutine panics | Recovered | All long-running goroutines defer `logging.Recover`. |

---

## Required controls before production

### 1. Restrict the health endpoint

By default, the health server binds to `0.0.0.0`. This is the right choice when you need a Kubernetes/Docker probe but exposes `/status` if a network policy isn't in place.

- **Containerized + behind a load balancer:** set `health.bindAddr = "127.0.0.1"` and run a sidecar (e.g. envoy) if external access is required.
- **Behind a service mesh:** keep the default but rely on mTLS at the mesh level.
- **Standalone:** bind to localhost and tunnel via SSH for ad-hoc inspection.

### 2. Authenticate `/status` and `/metrics`

Set `GONNER_HEALTH_TOKEN` in the runtime environment. Tokens shorter than 16 characters generate a validation warning.

```yaml
# k8s deployment fragment
env:
  - name: GONNER_HEALTH_TOKEN
    valueFrom:
      secretKeyRef:
        name: gonner-secrets
        key: health_token
```

Prometheus scraping with auth:

```yaml
- job_name: gonner
  authorization:
    credentials_file: /etc/prom/gonner-token
  static_configs:
    - targets: ['gonner:8089']
```

### 3. Drop privileges

If your container's entrypoint runs as root (common with PID 1 + zombie reaping), drop per process:

```json
{
  "name": "web",
  "command": "nginx -g 'daemon off;'",
  "user": "www-data",
  "group": "www-data"
}
```

`user` accepts a username or numeric UID. `group` defaults to the user's primary group when omitted.

### 4. Pin the `commandsBefore`

`commandsBefore` runs with full privileges. Common pattern is to perform migrations as root and then drop privileges for the long-running process. To restrict pre-commands, set `user` on the parent process — `commandsBefore` inherits it.

### 5. Audit env interpolation sources

`{{env://VAR}}` substitutes string values **before parsing**. A `VAR` containing JSON metacharacters can change the structure of the resulting document. Only interpolate variables you control. For untrusted user input, sanitize before exporting.

### 6. Constrain log file access

Default mode is `0o600` (owner read/write only). Override per-process via `logFileMode` when you ship logs to a shared collector — but never relax it past `0o640`.

If you write logs to a tmpfs / shared volume, the directory itself should be `0o750` or stricter.

---

## Threat model (non-goals)

The following are explicitly **not** in scope and require external controls:

- Sandboxing the workload (use seccomp / capabilities / namespaces).
- Limiting CPU/memory (use cgroups / `--cpu` / `--memory` flags / Kubernetes resource limits).
- Network isolation between managed processes (use a network namespace or per-process firewall).
- Secret distribution (use Docker secrets / Vault / Kubernetes secrets and pass via env vars).
- TLS termination for managed processes (gonner only TLS-protects its own health endpoint).

---

## Reporting vulnerabilities

Please report security issues privately via [GitHub security advisories](https://github.com/michaelishri/gonner/security/advisories) rather than as public issues. Include reproduction steps, affected version, and impact.

---

## OWASP Top 10 mapping

| Item | Gonner posture |
|---|---|
| A01 — Broken Access Control | `/health` public; `/status` & `/metrics` gated by bearer token. |
| A02 — Cryptographic Failures | TLS supported via `health.tls.{certFile,keyFile}`, minimum TLS 1.2. No bundled cipher overrides; uses Go defaults. |
| A03 — Injection | Commands run via `sh -c`, which is intentional. **Config is trusted.** Env interpolation pre-parse can change structure if untrusted vars are used. |
| A04 — Insecure Design | Critical-process exit triggers full shutdown; failure modes prefer fail-fast. |
| A05 — Security Misconfiguration | Strict defaults (private health, secure log perms). Misconfigurations surface via `gonner validate` warnings. |
| A06 — Vulnerable Components | Minimal deps (`cobra`, `errgroup`, `yaml.v3`). Run `go list -m -u all` to audit. |
| A07 — Auth Failures | Bearer-token comparison uses `crypto/subtle.ConstantTimeCompare`. |
| A08 — Software & Data Integrity | Static binary builds with `CGO_ENABLED=0`; release binaries should be checksummed and signed. |
| A09 — Logging Failures | Process and gonner logs are timestamped (RFC3339) and structured per-line. |
| A10 — SSRF | N/A — no outbound HTTP. The `portOpen` condition only dials hosts named in the config. |
