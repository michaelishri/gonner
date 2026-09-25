# Private management protocol v1

Opt in with `control: {socket: /run/gonner/control.sock, restartGrace: 100ms}`.
The directory must be canonical, owned by Gonner's UID and mode 0700. The socket
is mode 0600 and every accepted connection is checked against the same effective
UID (Linux SO_PEERCRED or Darwin LOCAL_PEERCRED). Mount it only into trusted local
components. Management is never registered on the TCP health server.

`GET /v1/instances` returns `version: 1`, `shuttingDown` and individual `instances`:
`id`, random 128-bit `generation`, `state`, `controllable`, `previousGeneration`,
`previousReaped`. Identity uses the configured name (or `name/index` for copies).
Generation changes on every launch, including across supervisor restarts.

`POST /v1/restart` with Content-Type application/json and exactly
`{"instance":"worker-a","expectedGeneration":"<32 lowercase hex characters>"}`
returns 202 for accepted/idempotent current-generation requests, 409 for stale,
unknown, stopped or non-controllable targets, and 400 for malformed input.
Duplicate/unknown keys, extra documents and bodies over 1024 bytes are rejected.
No caller-supplied command, path, signal, timeout or PID is accepted.

202 confirms a restart request, **not cleanup**. Poll instances until
`previousGeneration` matches and `previousReaped` is true. This means the command
was waited on, its output readers finished and its original process group no
longer exists. A new generation is never started for a controllable process if
that check fails. The API retains only the immediately previous generation;
clients observing a later generation must reconcile their state, never replay
an earlier credential request. A stale generation can never stop a replacement.
Supervision covers the original process group; managed programs must not escape
it with setsid/setpgid. Containers should run Gonner as PID 1 for orphan reaping.

Process opt-ins:

```yaml
run:
  - name: worker-a
    command: exec /usr/local/bin/my-worker
    autoRestart: true
    restartOnSuccess: true
    controllable: true
    critical: false
```

`restartOnSuccess` permits intentional clean recycling; without it existing
clean-exit behavior remains unchanged. `controllable` requires autoRestart and
critical=false. All restart paths retain the configured backoff and maxRetries.
A targeted restart uses control.restartGrace; global shutdown retains each
process's stopTimeout/global shutdownTimeout. Idle siblings keep running.
Gonner supplies reserved non-secret GONNER_INSTANCE_ID/GONNER_GENERATION variables
after configured environment values; these cannot be overridden by configuration.

A running process is not application readiness. Consumers must independently
check worker readiness and verify matching generation/protocol before routing.
This socket grants lifecycle authority to its UID; it is not a tenant-facing API.

The control feature is built and audited with Go 1.26.6, matching the warm-service
consumer. The older local Go 1.26 runtime reported reachable standard-library
advisories; rerunning with 1.26.6 reports no reachable vulnerabilities. CI now
runs module verification, formatting, vet, the race suite and govulncheck. The
new x/sys v0.40.0 dependency is used only for peer credentials on Unix sockets.

Credential workers should set `clearEnv: true` and provide only an explicit
non-secret Env allowlist. The default preserves inherited environment for existing
services. Reserved instance/generation bootstrap values are added independently.
