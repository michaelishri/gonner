# Codebase bug and security audit — 2026-09-25

**Reviewed commit:** `a6720c79e4d703d4705233a29081303887f8d49c`

**Audit branch:** `audit/codebase-bugs-security-2026-09-25`

**Status:** Findings for review. No application fixes or implementation plans are included.

The audit identified **25 findings: 6 high, 15 medium, and 4 low priority**. Four concern security boundaries or vulnerable builds (S1, S10, S11, S12). The remaining findings concern availability, data loss, lifecycle behavior, observability, or CLI correctness. Each numbered finding is independently referenceable for later planning; multiple reproductions of the same underlying issue are grouped together.

## Scope and evidence

Reviewed all production packages, their tests, CLI entry points, configuration examples, documentation, dependencies, build tasks, and both GitHub Actions workflows. The existing documentation supplies the behavioral specification and security model. The two review axes are kept separate below: **Standards** covers security and implementation correctness; **Spec** covers documented runtime behavior.

Validation performed:

- `go test -race -count=1 -cover ./...` — passed on Linux/amd64 with Go 1.26.0. Runner coverage was 59.2%; CLI coverage was 30.7%. Existing passing tests do not exercise the failure cases below.
- `go vet ./...` — passed.
- `CGO_ENABLED=0` builds for Linux and Darwin, each on amd64 and arm64 — passed. Darwin and arm64 binaries were compiled, not executed.
- Temporary focused Go tests, CLI/HTTP reproductions, and isolated Alpine 3.21 Docker containers — reproduced the application findings. Containers had no network access for the PID 1 and privilege tests. All temporary repository test files were removed afterward.
- `govulncheck` v1.8.0, database last modified `2026-09-24T20:07:49Z` — 18 symbol-level standard-library advisory IDs with the local Go 1.26.0 toolchain; **26** with the Go 1.25.0 toolchain selected by CI. See S12 and the appendix for interpretation.

The review treats configuration and interpolated environment variables as trusted, as specified in [security.md](../security.md). Intentional `sh -c` execution, opt-in authentication/TLS, public probe endpoints, and configurable outbound port checks are not reported as vulnerabilities. No deployed service or published release binary was inspected, and no external exploit was attempted. Severity is an engineering assessment, not a CVSS score: **high** means a privilege boundary violation, exposed known vulnerability, or substantial lifecycle failure; **medium** means a material defect requiring a particular configuration or runtime condition; **low** means narrower correctness or observability impact.

## Standards

### S1 — A workload-controlled log symlink lets the root supervisor modify privileged files

**High · Security · Reproduced across UIDs.** Locations: [writer.go](../../internal/logging/writer.go#L77), lines 77–90; rotation reopens the same path at line 161.

`openLogFile` follows symlinks when opening a log and then changes permissions by pathname. If a lower-privileged workload can write the configured log directory, it can replace the log path with a symlink before startup or reopening. The supervisor subsequently appends that workload's output to the symlink target using the supervisor's privileges. The configuration itself does not need to be writable by the attacker.

**Reproduction:** In an isolated container, create a root-owned `0600` file and a `0750` log directory owned by UID 65534. As UID 65534, verify direct writes to the root-owned file fail, then create `app.log` as a symlink to it. Run root gonner with `user: "65534"`, that `logFile`, and a command printing `AUDIT_APPEND`. Direct writing failed with permission denied; the target nevertheless acquired `AUDIT_APPEND` through gonner. A separate test also confirmed the target's mode is changed to `0600`.

**Impact/preconditions:** Arbitrary privileged-file append and permission modification, potentially enabling privilege escalation depending on the target. Requires a supervisor with greater privileges and attacker control of the log directory/path. Default directories created and owned by the supervisor block that access provided the workload cannot replace an ancestor directory; an existing directory owned by the workload remains vulnerable even with mode `0750`.

### S2 — One oversized log line stops draining output and can hang the workload

**Medium · Availability · Reproduced.** Location: [writer.go](../../internal/logging/writer.go#L227), lines 227–234.

The scanner stops permanently when a token exceeds 1 MiB. Its error is ignored, and the pipe remains open without a reader. Once further output fills the kernel pipe, the child blocks on writes while gonner waits for it to exit. This also affects workloads without `logFile` configured. The [troubleshooting guide](../troubleshooting.md) instead says oversized output is dropped up to the next newline.

**Reproduction:** Run `head -c 16777216 /dev/zero; printf done > /tmp/completed` as a managed command in an isolated environment. The completion marker was never written; cancelling after 300 ms was required to terminate the blocked command. An `io.Pipe` test independently confirmed the scanner had returned while its producer remained blocked. The hang requires enough output to fill the remaining pipe capacity; lost output occurs as soon as scanning stops.

### S3 — Multiple log rotations in one second overwrite earlier backups

**Medium · Data loss · Reproduced with and without compression.** Locations: [writer.go](../../internal/logging/writer.go#L142), lines 142–145 and 198.

Backup filenames contain only whole seconds. A second rotation within that second reuses the same filename: `Rename` replaces the previous plain backup, or gzip creation truncates the previous `.gz` file. This discards data even when retention is unlimited.

**Reproduction:** Set `maxSizeMB: 1`, leave `maxBackups: 0`, and write three distinguishable 600 KiB chunks within one wall-clock second. Expected one active log plus two backups; observed one active log plus only one backup. Both `compress: false` and `compress: true` lost the first chunk.

### S4 — Multi-instance processes lose output when their shared log rotates

**Medium · Data loss · Reproduced.** Locations: [process.go](../../internal/runner/process.go#L139), lines 139–154; [writer.go](../../internal/logging/writer.go#L138), lines 138–161.

Every instance opens an independent writer, file descriptor, mutex, and size counter for the same configured log path. When one writer rotates and compresses the file, other writers retain descriptors for the now-unlinked original file. Their writes can succeed while becoming inaccessible through any log filename.

**Reproduction:** Open two writers for one path with a 1 MiB limit and compression enabled. Have writer A write two 600 KiB chunks, causing one rotation, then have writer B write a unique marker. B returned success, but the marker appeared in neither the active log nor the decompressed backup. This needs only one rotation and is independent of S3. It directly represents `instances: 2` with a shared `logFile`; separate process definitions sharing a path are also affected.

### S5 — Successful commands can lose their final stdout/stderr

**Medium · Data loss · Reproduced for main commands and precommands.** Locations: [process.go](../../internal/runner/process.go#L266), lines 266–270 and 356–384.

Both command paths call `cmd.Wait()` before joining their pipe-reading goroutines. `Wait` closes the pipes when the command exits, even if a scanner is still processing earlier output. The subsequent scanner join cannot recover unread trailing bytes. This ordering violates the documented [StdoutPipe/StderrPipe contract](https://pkg.go.dev/os/exec#Cmd.StdoutPipe).

**Reproduction:** Use a FIFO log destination whose reader pauses for 300 ms to simulate a slow sink. The child prints a 64 KiB line, sleeps 100 ms, prints `TAIL-MARKER`, and exits successfully. Both main-command and `commandsBefore` tests captured exactly 65,537 bytes and omitted the marker, although both command paths returned success. The line is below the scanner limit, so this is independent of S2.

### S6 — Backup pruning deletes unrelated files with a matching prefix

**Medium · Data loss · Reproduced.** Location: [writer.go](../../internal/logging/writer.go#L164), lines 164–188.

Pruning considers every entry beginning with `<logFile>.` to be a backup. It does not check the documented timestamp format or ownership of the file. Consequently another process's log or a sibling data file can count toward retention and be deleted.

**Reproduction:** Create `app.log.1` containing unrelated data. Configure `app.log` with `maxSizeMB: 1` and `maxBackups: 1`, then write two 600 KiB chunks. The first rotation deleted `app.log.1`, even though it was not a gonner timestamp backup. Two explicitly configured logs such as `app.log` and `app.log.1` can therefore interfere with each other.

### S7 — A log-file write failure silently suppresses stdout as well

**Medium · Observability/data loss · Reproduced.** Location: [writer.go](../../internal/logging/writer.go#L108), lines 108–121 and 230–232.

The file sink is written first. If it fails, `Write` returns before sending the same line to stdout, and `LineScanner` discards the error. A full disk therefore removes both the file record and the container's stdout record without an operational error for the lost line.

**Reproduction:** Inject a directly opened `/dev/full` descriptor into a test writer, keeping stdout backed by an ordinary buffer, and scan a normal line. The file write returned ENOSPC internally; stdout received zero bytes. The test bypassed `NewWriter`, so it did not chmod or otherwise modify the device.

### S8 — Startup conditions escape cancellation and descendant cleanup

**Medium · Lifecycle/availability · Reproduced.** Locations: [commandsucceeds.go](../../internal/condition/commandsucceeds.go#L25), lines 25–30; [manager.go](../../internal/runner/manager.go#L52), lines 52–64.

Command conditions use a context derived from `context.Background()`, and their default cancellation kills only the shell. They neither inherit supervisor cancellation nor use managed process-group cleanup. Condition evaluation also continues through the configuration without checking the manager context.

**Reproductions:** A condition `(sleep 0.2; printf child > <marker>) & wait`, with its test timeout shortened to 50 ms, returned false at timeout but its descendant subsequently wrote the marker. The production 10-second timeout uses the same cancellation path. Separately, with a normal `commandSucceeds: "sleep 2"` and `shutdownTimeout: "100ms"`, SIGTERM during evaluation took approximately 1.97 seconds to finish. Additional conditions can extend that delay.

**Impact:** Timed-out conditions can retain resources or perform delayed side effects after evaluation returns. Startup shutdown can exceed the configured process timeout, and unmanaged condition descendants can survive while other workloads keep gonner alive.

### S9 — Invalid TLS credentials leave startup successful and the listener unusable

**Medium · Availability · Reproduced.** Location: [server.go](../../internal/health/server.go#L84), lines 84–112.

Certificate loading happens asynchronously inside `ServeTLS`, after `Start` has returned success. A missing, unreadable, malformed, or mismatched credential can stop that goroutine without returning an error to the CLI. The raw listener also remains bound when certificate loading fails before the server starts serving.

**Reproduction:** Start the health server with nonempty paths to nonexistent certificate/key files. `Start` returned nil, an HTTPS handshake timed out, and attempting to bind the address again failed with `address already in use`. The application proceeds with its workloads even though its configured probe/status endpoint cannot serve requests. This does not downgrade the server to plaintext; it strands the listener.

### S10 — PR label text is evaluated as shell syntax in the release workflow

**Medium · Release correctness / conditional security · Local reproduction.** Location: [release.yml](../../.github/workflows/release.yml#L44), line 44; workflow permissions at lines 10–11.

The expression `labels='${{ toJson(github.event.pull_request.labels.*.name) }}'` pastes JSON directly into a single-quoted Bash string. JSON serialization does not escape apostrophes for Bash. A label such as `can't-release` breaks parsing; a crafted label can terminate the quoted string and execute shell commands.

**Reproduction:** Locally substitute the JSON array containing `can't-release` into the workflow statement and run it with Bash: parsing exits with status 2. Substituting the label `x'; printf AUDIT_LABEL_EXECUTED; labels='` prints the marker with exit status 0. Only a harmless local marker was executed; GitHub was not modified.

**Impact/preconditions:** The release job runs on a merged PR with `contents: write`. Exploitation requires the ability to introduce or attach the relevant repository label to a PR that gets merged. That permission path was not verified, so this is not claimed to be an unauthenticated fork-PR exploit. Ordinary apostrophes still cause a release failure. The mechanism matches GitHub's documented [script-injection boundary](https://docs.github.com/en/actions/reference/security/secure-use#script-injections).

### S11 — Numeric user IDs select the wrong primary group

**Medium · Privilege correctness/security · Reproduced across UIDs.** Location: [process.go](../../internal/runner/process.go#L538), lines 538–550.

`applyCredential` looks up a numeric UID as a username. When that lookup fails, it assigns both UID and GID to the supplied number instead of looking up the account's primary group. This contradicts [the documented default](../security.md#3-drop-privileges) when `group` is omitted.

**Reproduction:** In an isolated container, define `audituser` with UID 1234 and primary GID 2345, plus an unrelated group with GID 1234. Run the same `id` command under gonner first with `user: "1234"`, then with `user: "audituser"`, without a `group` override. The numeric form produced `uid=1234(audituser) gid=1234(unrelated)`; the named form correctly produced GID 2345.

**Impact/preconditions:** Numeric-UID deployments can lose access to intended group-owned resources or gain access through an unrelated group. Requires root gonner, an omitted group override, and an account whose UID and primary GID differ. No automatic root-group escalation is claimed.

### S12 — CI selects an unpatched Go toolchain with reachable security advisories

**High · Build/dependency security · Scanner and source evidence; no exploit attempted.** Locations: [go.mod](../../go.mod#L3), line 3; [go.yml](../../.github/workflows/go.yml#L18), lines 18–21; [release.yml](../../.github/workflows/release.yml#L29), lines 29–32; [server.go](../../internal/health/server.go#L94), TLS serving.

Both workflows use `go-version-file: go.mod`, whose directive specifies `go 1.25.0`. A patch-qualified directive selects that exact version, as documented by [setup-go](https://github.com/actions/setup-go/blob/main/docs/advanced-usage.md#using-the-go-version-file-input). It does not merely establish a minimum patched compiler for those workflows.

**Evidence:** Running `GOTOOLCHAIN=go1.25.0 govulncheck -json ./...` with govulncheck v1.8.0 reported 26 distinct standard-library advisory IDs at symbol level. The optional TLS health server reaches affected TLS operations, including [GO-2026-4870](https://pkg.go.dev/vuln/GO-2026-4870), a TLS 1.3 KeyUpdate connection deadlock fixed in Go 1.25.9, and [GO-2026-6090](https://pkg.go.dev/vuln/GO-2026-6090), excessive post-handshake KeyUpdate processing fixed in Go 1.25.13. Their server-side exposure precedes application bearer-token authorization.

**Qualification:** The 26 scanner results are not 26 proven exploits. Some need unsupported platforms or unused features; the appendix preserves the distinction. All symbol-level results were in the standard library, not third-party application modules. Published artifacts were not downloaded, so their embedded toolchain is not established. The defect here is the checked-in CI toolchain selection and the resulting exposure when vulnerable builds enable the affected features. The local Go 1.26.0 installation also returned 18 advisory IDs and is separately unpatched.

### S13 — Health requests race with manager startup when reading uptime

**Low · Concurrency/observability · Race detector reproduction.** Locations: [manager.go](../../internal/runner/manager.go#L44), lines 44 and 229–232; [run.go](../../cmd/gonner/cmd/run.go#L86), lines 86–104.

`Run` writes the multiword `startedAt` value without synchronization, while `Uptime` reads it without synchronization. The CLI starts accepting health requests before invoking `Run`; `/status` and `/metrics` can therefore read it concurrently with startup.

**Reproduction:** Start a goroutine repeatedly calling `Uptime` on a new manager, then call `Run` with a simple successful command. `go test -race` reported the write at `manager.go:44` racing with the read at `manager.go:231`. Existing health tests never run their manager concurrently. Impact is an actual data race and potentially invalid uptime reporting around startup; no process crash was demonstrated.

## Spec

### B1 — Failed or transitively skipped dependencies can leave startup blocked forever

**High · Lifecycle · Reproduced.** Location: [manager.go](../../internal/runner/manager.go#L131), lines 131–144.

Parallel dependency waiting observes only readiness and global cancellation. It never observes a dependency's terminal failure. In the skipped-dependency path, the process is marked skipped without running its lifecycle or closing its `Done` channel. Downstream dependants can therefore wait forever even though nothing can make their dependency ready.

**Reproductions:** Define A with `commandsBefore: [{"command":"exit 7"}]`, and B with `dependsOn: ["A"]`. A becomes failed, B remains pending, and `Manager.Run` stays blocked with no live child. Separately, make A's condition false, B depend on A, and C depend on B. B becomes skipped but C remains pending forever. Both cases stopped only after explicit cancellation. The [architecture](../architecture.md#concurrency-model) describes readiness/done coordination and lifecycle completion.

### B2 — Failure of a critical precommand does not shut down the other processes

**High · Lifecycle · Reproduced.** Location: [process.go](../../internal/runner/process.go#L162), lines 162–171, compared with the main-command critical path at 197–203.

A failed `commandsBefore` command marks its process failed and returns nil, bypassing critical cancellation. The [configuration reference](../configuration.md#run--process-definitions) promises full shutdown for critical failure, and [troubleshooting](../troubleshooting.md#container-exits-immediately) explicitly lists failed precommands as such a cause.

**Reproduction:** Configure a critical process with precommand `exit 7` and another process running `exec sleep 30`. The critical process became failed, but `IsShuttingDown()` remained false and the background process continued running. This can leave a container serving from a partially initialized application after a required migration or setup step fails.

### B3 — Multi-instance startup only waits for instance zero

**Medium · Startup ordering · Reproduced in both modes.** Location: [manager.go](../../internal/runner/manager.go#L112), lines 112–120 and 139–144.

Both startup paths inspect `procs[0]` instead of accepting any ready instance. [The specification](../configuration.md#multi-instance-processes) says any one running instance satisfies readiness for dependencies.

**Reproduction:** Configure two instances whose precommand races to create a marker directory: the winner completes, the loser waits. When instance 1 wins, it runs while instance 0 remains starting; the dependent process remains pending despite a healthy dependency instance. This was observed in both parallel dependency waiting and sequential startup. A permanently blocked first instance prevents useful progress indefinitely.

### B4 — Backoff downtime is mistaken for a stable running period

**Medium · Restart policy · Reproduced in a real crash loop.** Location: [backoff.go](../../internal/runner/backoff.go#L34), lines 34–43.

`RecordStart` resets the counter using time elapsed since the previous start. That interval includes the backoff wait itself. The [specified stability reset](../configuration.md#backoff) requires the process to have stayed running longer than `maxDelay`.

**Reproduction:** Run `exit 7` with initial delay 10 ms, maximum 100 ms, multiplier 2, and 24 allowed retries. Observed start intervals included approximately `13, 20, 39, 74, 93, 101, 11, 22, 40, 79, 110, 12 ms`. Every process exited immediately, yet the delay repeatedly reset after reaching the cap. Persistent failures therefore periodically return to aggressive retry rates instead of remaining backed off.

### B5 — A crashed process is reported as running throughout its restart delay

**Medium · Status/metrics correctness · Reproduced.** Locations: [process.go](../../internal/runner/process.go#L389), lines 389–394 and 211–225; [manager.go](../../internal/runner/manager.go#L197), lines 197–200, aggregation of running instances.

After a main command exits, the command pointer is cleared but the running state is retained unless group cleanup happened to change it. A noncritical process waiting to restart can consequently remain `running` while it has no PID. The [metrics contract](../operations.md#metrics-prometheus) describes currently running instances and whether a process is up.

**Reproduction:** Run `exit 7` with automatic restart and a five-second initial delay. During the delay, status reported `status: "running"`, `runningInstances: 1`, and no PID. This can hide an outage from status consumers and `gonner_process_up` alerts. Critical processes currently take the shutdown path rather than automatic restart, so this reproduction is not a claim about critical readiness during backoff.

### B6 — Restart counts include attempts that never happen

**Low · Metrics correctness · Reproduced.** Location: [process.go](../../internal/runner/process.go#L211), lines 211–218.

The counter increments before waiting and before checking the retry limit. It therefore includes the final rejected attempt and also counts a restart even if shutdown cancels its backoff. This differs from [the documented aggregate restart count](../operations.md#metrics-prometheus).

**Reproduction:** Allow 24 retries for an immediately failing command. There were 24 actual restarts after the initial start, but the final reported counter was 25. During the very first backoff interval, it already reported one restart before a second command had started.

### B7 — The PID 1 reaper still steals child exit statuses

**High · Container lifecycle · Reproduced with gonner as PID 1.** Locations: [signals.go](../../internal/runner/signals.go#L118), lines 118–125; [process.go](../../internal/runner/process.go#L266), lines 266–280 and 360–373.

The reaper's `wait4` and subsequent map store are not synchronized with `exec.Cmd.Wait` and its single map lookup. A main-command waiter can receive ECHILD before the status is stored. Precommands do not consult the map at all. This contradicts the [documented guarantee](../architecture.md#pid-1-reaping-coordination) that reaping is coordinated with command waiting.

**Reproductions:** In a fresh Alpine container with gonner as the entrypoint, configure 32 instances, each with 20 precommands containing only `true`, followed by an ordinary main command. **28 of 32 instances** falsely failed in precommands with `waitid: no child processes`. Separately, run 300 instances of the main command `true`: three trials produced **1, 2, and 0** false main-command failures, respectively. A log entry from the reaper can follow the failed lookup, so the issue is timing-sensitive.

**Impact:** Successful setup commands can prevent service startup; successful main commands can be restarted or treated as critical failures. Ordinary non-PID-1 tests do not activate the competing reaper. These are two manifestations of the same exit-status ownership problem.

### B8 — A fatal critical-process failure exits gonner with success

**High · Supervisor integration · Reproduced through the CLI.** Locations: [process.go](../../internal/runner/process.go#L197), lines 197–203; [manager.go](../../internal/runner/manager.go#L153), lines 153–157; [main.go](../../cmd/gonner/main.go#L11).

Critical failure cancels the manager but returns nil. Shutdown of the other processes also returns nil, so the errgroup and CLI report success. The documented [systemd deployment](../deployment.md#systemd) uses `Restart=on-failure`, which will not restart a supervisor that exits with code 0.

**Reproduction:** Run this configuration and inspect the gonner exit code:

```json
{"run":[{"name":"essential","command":"exit 42","critical":true}]}
```

Gonner logged `CRITICAL ... triggering full shutdown` and then exited with **0**. The desired distinction is fatal workload failure versus operator-requested clean shutdown; preserving the exact child's exit code is a separate design choice. Docker `on-failure` policies are affected for the same reason. This is distinct from B2: main-command critical shutdown happens here, but its result is misreported.

### B9 — Readiness succeeds before critical processes exist, and after they are skipped

**Medium · Traffic routing/readiness · Reproduced over HTTP.** Locations: [run.go](../../cmd/gonner/cmd/run.go#L86), lines 86–104; [manager.go](../../internal/runner/manager.go#L52), lines 52–80 and 260–277.

The health server starts before the manager evaluates conditions or constructs process instances. `Ready` checks only constructed instances. An empty instance list therefore looks like a configuration without critical processes. Critical configurations removed by false conditions remain absent from readiness checks permanently. [The public contract](../operations.md#get-ready) says all configured critical processes must have a running instance.

**Reproductions:** Configure a critical sleeper gated by `commandSucceeds: "sleep 2"`, plus a noncritical sleeper, and query the loopback endpoint during condition evaluation. `/ready` returned **200 ready** while `/status` had `processes: null`. Change the critical condition to an unset environment variable: only the noncritical process started, but `/ready` still returned 200. Traffic can therefore be routed while a required service is absent. Skipped configurations are also missing from detailed status rather than appearing as skipped.

### B10 — The documented container capability set prevents shutdown of dropped-UID children

**Medium · Deployment/lifecycle · Reproduced in a container.** Locations: [deployment.md](../deployment.md#kubernetes), capability example at lines 110–114; [process.go](../../internal/runner/process.go#L422), lines 422–428.

The Kubernetes example drops all capabilities and adds only `CHOWN`, `SETUID`, and `SETGID` for privilege dropping. After a child changes UID, the supervisor also needs permission to signal that other UID. Without `CAP_KILL`, group signaling returns EPERM; `stopProcessGroup` returns immediately, and the custom command cancellation cannot terminate the child. `cmd.Wait` can then block beyond the configured timeout.

**Reproduction:** Run gonner in Docker with `--cap-drop ALL --cap-add CHOWN --cap-add SETUID --cap-add SETGID`, a child `exec sleep 30` under UID 65534, and `shutdownTimeout: "100ms"`. Send SIGTERM to gonner. After 600 ms the container was still running, having logged the shutdown request without stopping the child. A daemon-issued SIGKILL cleaned up the test container. This concerns the supplied deployment recipe and signal-permission handling, not containers retaining their normal capabilities.

### B11 — The documented bare-port condition format always fails

**Low · Conditional startup · Reproduced.** Location: [portopen.go](../../internal/condition/portopen.go#L18), lines 18–32 and 39.

The [condition reference](../configuration.md#built-in-condition-types) permits `[host:]port` with localhost as the default. The constructor only adds a host if the string begins with `:`, so a bare numeric port reaches `net.DialTimeout` without the required address separator. The error becomes a false condition, silently skipping the workload.

**Reproduction:** Listen on an ephemeral loopback TCP port. Use its decimal number as the `portOpen` value: the process is skipped. Use the same number with a leading colon: the process runs. The listener and every other configuration field were unchanged.

### B12 — An explicit status port of 8089 loses to the environment override

**Low · CLI correctness · Reproduced.** Location: [status.go](../../cmd/gonner/cmd/status.go#L121), lines 121–131.

The resolver infers whether `--port` was supplied by comparing its value with the default. Supplying the default explicitly is indistinguishable from omitting the flag, violating [the documented CLI > environment precedence](../configuration.md#environment-variable-overrides).

**Reproduction:** Serve a distinctive fake `/status` response on an ephemeral loopback port, export that number as `GONNER_HEALTH_PORT`, and run `gonner status --port 8089`. The command successfully printed the fake server's response from the environment-selected port. This can query the wrong instance despite an explicit operator choice.

## Vulnerability scan appendix

The following are the **26 distinct symbol-level advisory IDs** returned using Go 1.25.0. Fixed versions are the scanner's Go 1.25-series fixes, not a claim that that series is currently supported. To reproduce the scan without adding application dependencies, install `golang.org/x/vuln/cmd/govulncheck@v1.8.0` into a separate tools directory, then invoke its binary from the repository with `GOTOOLCHAIN=go1.25.0` and arguments `-json ./...`.

| Advisory | Area reported | Fixed in Go 1.25 series |
|---|---|---|
| [GO-2025-4007](https://pkg.go.dev/vuln/GO-2025-4007) | X.509 name-constraint complexity | 1.25.3 |
| [GO-2025-4008](https://pkg.go.dev/vuln/GO-2025-4008) | TLS ALPN error content | 1.25.2 |
| [GO-2025-4009](https://pkg.go.dev/vuln/GO-2025-4009) | PEM parsing complexity | 1.25.2 |
| [GO-2025-4010](https://pkg.go.dev/vuln/GO-2025-4010) | URL IPv6 hostname validation | 1.25.2 |
| [GO-2025-4011](https://pkg.go.dev/vuln/GO-2025-4011) | ASN.1 memory exhaustion | 1.25.2 |
| [GO-2025-4012](https://pkg.go.dev/vuln/GO-2025-4012) | HTTP cookie parsing limits | 1.25.2 |
| [GO-2025-4013](https://pkg.go.dev/vuln/GO-2025-4013) | X.509 DSA certificate panic | 1.25.2 |
| [GO-2025-4155](https://pkg.go.dev/vuln/GO-2025-4155) | X.509 error formatting resources | 1.25.5 |
| [GO-2025-4175](https://pkg.go.dev/vuln/GO-2025-4175) | X.509 wildcard name constraints | 1.25.5 |
| [GO-2026-4337](https://pkg.go.dev/vuln/GO-2026-4337) | TLS session resumption | 1.25.7 |
| [GO-2026-4340](https://pkg.go.dev/vuln/GO-2026-4340) | TLS handshake encryption level | 1.25.6 |
| [GO-2026-4601](https://pkg.go.dev/vuln/GO-2026-4601) | URL IPv6 literal parsing | 1.25.8 |
| [GO-2026-4602](https://pkg.go.dev/vuln/GO-2026-4602) | `os.Root` file-info escape | 1.25.8 |
| [GO-2026-4870](https://pkg.go.dev/vuln/GO-2026-4870) | TLS 1.3 KeyUpdate deadlock | 1.25.9 |
| [GO-2026-4918](https://pkg.go.dev/vuln/GO-2026-4918) | HTTP/2 client settings loop | 1.25.10 |
| [GO-2026-4946](https://pkg.go.dev/vuln/GO-2026-4946) | X.509 policy-validation complexity | 1.25.9 |
| [GO-2026-4947](https://pkg.go.dev/vuln/GO-2026-4947) | X.509 chain-building work | 1.25.9 |
| [GO-2026-4971](https://pkg.go.dev/vuln/GO-2026-4971) | Windows network NUL-byte panic | 1.25.10 |
| [GO-2026-5026](https://pkg.go.dev/vuln/GO-2026-5026) | IDNA/Punycode validation | 1.25.13 |
| [GO-2026-5037](https://pkg.go.dev/vuln/GO-2026-5037) | X.509 hostname parsing complexity | 1.25.11 |
| [GO-2026-5039](https://pkg.go.dev/vuln/GO-2026-5039) | Text-protocol error escaping | 1.25.11 |
| [GO-2026-5856](https://pkg.go.dev/vuln/GO-2026-5856) | TLS ECH privacy behavior | 1.25.12 |
| [GO-2026-5972](https://pkg.go.dev/vuln/GO-2026-5972) | ASN.1 recursion depth | 1.25.13 |
| [GO-2026-6089](https://pkg.go.dev/vuln/GO-2026-6089) | Unencrypted HTTP/2 header timeout | 1.25.13 |
| [GO-2026-6090](https://pkg.go.dev/vuln/GO-2026-6090) | TLS post-handshake message processing | 1.25.13 |
| [GO-2026-6218](https://pkg.go.dev/vuln/GO-2026-6218) | URL path-resolution complexity | 1.25.13 |

Manual applicability limits: gonner's release matrix does not include Windows; it does not use `os.Root`; it does not configure ECH; and it does not explicitly enable unencrypted HTTP/2. The corresponding scanner matches should not be promoted to confirmed application exposures without further evidence. Other advisories can depend on optional TLS, the outbound status client, certificates, or parsing paths. S12 is counted as **one build-security finding**, not 26 separate application defects.

## Review disposition

**Standards:** 13 findings (2 high, 10 medium, 1 low); greatest security impact is S1's conditional privileged-file modification, alongside S12's vulnerable CI toolchain. **Spec:** 12 findings (4 high, 5 medium, 3 low); B7 directly undermines PID 1 startup, while B1/B2/B8 break failure handling. All findings remain open for user review; no fixes, issue-tracker entries, PRs, or separate implementation plans were created.

## Resolution matrix 2026-09-26

The evidence above describes the audited baseline and is retained unchanged. The remediation is implemented on `fix/codebase-audit-2026-09-26` for v0.1.0. Each finding below has a permanent regression check. This matrix records implementation validation; publication status is tracked in GitHub Releases.

| Finding | Resolution | Permanent regression coverage |
|---|---|---|
| S1 | Verified directory descriptors, no-follow leaf opens, regular-file/single-link/ownership checks, descriptor chmod, ACL checks; the same protection covers PID files. | [safefile tests](../../internal/safefile/directory_test.go): `TestRejectLeafSymlinkHardlinkFIFOWithoutTouchingVictim`, `TestRejectWritableAncestorAndAllowTrustedSymlinks`, `TestStickyAncestorAndDescriptorSurviveRename`; platform ACL tests; [Docker](../../internal/integration/docker_linux_test.go) `/files` runs as root against workload-owned paths. |
| S2 | Stream arbitrarily long output in bounded 64 KiB chunks without discarding raw bytes. | [logging tests](../../internal/logging/regression_test.go): `TestOversizedAndUnterminatedOutputIsByteExact`; [execution tests](../../internal/execution/service_test.go): `TestLargeOutputDrainsWithoutDeadlock`. |
| S3 | Nanosecond/random backup names and exclusive publication prevent overwrites during rapid rotation. | Logging: `TestRapidRotationAndSharedSinkPreserveEveryByte`, with plain and compressed backups. |
| S4 | Shared path registry owns one descriptor, mutex, byte counter and reference count; conflicting settings fail preflight. | Logging: `TestRapidRotationAndSharedSinkPreserveEveryByte`, `TestSharedPathRejectsConflictingSettings`. |
| S5 | Explicit pipe descriptors outlive the sole child wait; drain to EOF after group cleanup. | Execution: `TestWaitDoesNotCloseUndrainedPipes` deliberately delays reading until after child exit. |
| S6 | Prune only reserved-format regular backups, excluding active configured log paths. | Logging: `TestPruningPreservesUnrelatedLegacyAndActivePaths`. |
| S7 | Both destinations are attempted; errors are rate-limited, future writes retry, and rotation failures retain a usable descriptor. | Logging: `TestFileErrorStillWritesConsoleAndRecovers`, `TestRotationFailureKeepsOpenSinkUsable`. |
| S8 | Context-aware conditions use the shared executor; command timeout/cancellation and normal completion clean up the process group. | [condition tests](../../internal/condition/new_conditions_test.go): `TestCommandConditionCancellationAndTimeout`; Docker `/conditions` checks normal and cancelled probes plus pre/main descendants. |
| S9 | Load certificates synchronously before binding; serving failures cancel supervision and propagate to exit status. | [health tests](../../internal/health/regression_test.go): `TestTLSValidationIsSynchronousAndDoesNotLeakListener`, `TestUnexpectedServeFailureIsReported`; [CLI tests](../../cmd/gonner/cmd/shutdown_linux_test.go): `TestInvalidTLSPreventsWorkloadStartup`. |
| S10 | Pass labels as environment JSON and choose exact labels in Python, with major > minor > patch precedence. | [release tests](../../scripts/test_release_bump.py): exact membership, priority, invalid shape and subprocess injection payload cases. |
| S11 | Numeric UID lookup uses the account's primary GID; unknown UID requires a group; validate IDs and clear supplementary groups. | Execution: `TestCredentialRejectsInvalidAndUnknownIDs`; Docker `/credentials` checks UID 2001 / GID 3001, override GID, unknown numeric UID and empty supplementary groups. |
| S12 | Require Go 1.26.7; pin govulncheck v1.8.0 and gate publication on source and all release-binary scans. | [CI](../../.github/workflows/go.yml), [release workflow](../../.github/workflows/release.yml), [artifact checks](../../scripts/check-artifacts.sh). |
| S13 | Guard manager start time and report zero uptime before Run. | [runner tests](../../internal/runner/regression_test.go): `TestReadinessIncludesPendingAndSkippedCriticalAndUptimeRace`, exercised with `-race`. |
| B1 | Stable first-start gates and exactly-once lifecycle completion propagate unavailable dependencies through the graph. | Runner: `TestFailedAndSkippedDependencyChainsTerminate`, `TestCriticalUnavailableDependencyIsFatal`, `TestReadyChannelSurvivesActualRestarts`. |
| B2 | Critical required-precommand failure cancels all work and returns an error. | Runner: `TestCriticalPrecommandFailureCancelsAndReturnsError` also checks `continueOnError`. |
| B3 | Both startup modes consider every instance; dependencies persist after a successful one-shot start. | Runner: `TestAnyInstanceStartGateAndShortLivedPrerequisite`, `TestDependencyRunsWhenInstanceZeroCannotStart`; [config tests](../../internal/config/validation_test.go): `TestSequentialRejectsForwardDependencies`. |
| B4 | Reset escalation from actual runtime at exit; exclude downtime and cleanup. | Runner: `TestBackoffExcludesDowntimeAndCleanup` uses virtual time; [backoff tests](../../internal/runner/backoff_test.go): `TestBackoff_RecordExit_ResetAfterStability`. |
| B5 | Clear PID/running status immediately after the direct-child wait; show `starting` during retry backoff. | Runner: `TestBackoffStatePIDAndCancelledRetryCount`. |
| B6 | Retry budget counts extra attempts; public restarts count successful launches after the first successful launch. | Runner: `TestRetryLimitCountsOnlySuccessfulRelaunches`, `TestBackoffStatePIDAndCancelledRetryCount`. |
| B7 | Managed PID registry synchronizes spawn/registration with PID-specific orphan reaping; remove wildcard waits and status cache. | Docker `/stress`: 3,584 short-lived command/probe/precommand shells, explicit nonzero exit checks and no remaining children; execution cancellation tests. |
| B8 | Permanent failures determine eventual exit 1; critical/supervisor failures cancel all; unrelated noncritical work continues. | Runner: `TestCriticalAndNoncriticalPermanentExitsReturnError`, `TestFailedAndSkippedDependencyChainsTerminate`; CLI `TestRunShutdown/critical_exit`; execution `TestLaterCancellationPreservesObservedExitFailure`. |
| B9 | Build every configured instance before Run; pending/skipped critical processes remain unready and visible. | Health: `TestReadyAndStatusIncludePendingAndSkippedCritical`; runner readiness/uptime test checks configured counts. |
| B10 | Check capabilities before any probe or command; include KILL in the deployment example and propagate signal errors. | Docker `/credentials` verifies cross-UID shutdown; `/missing-kill` verifies fail-fast startup without any workload or condition execution. |
| B11 | Normalize bare and colon-prefixed ports to loopback and use context-aware dialing. | Condition: `TestPortOpenBareAndColonPort`, including hostname and cancelled-context cases. |
| B12 | Resolve status port using Cobra flag presence, so explicit 8089 wins over the environment. | [CLI status tests](../../cmd/gonner/cmd/status_test.go): `TestExplicitDefaultStatusPortOverridesEnvironment`. |

### Validation and remaining platform checks

- Linux: `go test -race -count=1 -cover ./...`, `go vet ./...`, and the Python release-helper tests passed.
- Isolated Docker tests passed with a read-only filesystem, no network, temporary `/tmp`, explicit capabilities and the test process actually running as PID 1. They cover fast-exit stress, group cleanup, privilege drop, cross-UID signaling, missing capabilities and unsafe paths. Containers are removed after testing.
- Go 1.26.7 source scanning and binary scanning of Linux/Darwin × amd64/arm64 static builds with govulncheck v1.8.0 reported no vulnerabilities.
- Native macOS execution is unavailable in this Linux workspace. macOS tests, including the Darwin ACL regression, are configured in both PR CI and the release verification matrix; release publication depends on that matrix passing. Cross-compilation succeeded for both Darwin architectures.
- [Operations migration notes](../operations.md#upgrade-from-the-audited-baseline) document strict paths, numeric UID/group changes, capabilities, exit statuses, readiness, dependency semantics, console chunking, legacy backups and the context-aware condition API. No configuration keys, HTTP keys or lifecycle states were added.
