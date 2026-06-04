# Troubleshooting

## Startup

### "no config file found"

Gonner searched every location in the [discovery order](configuration.md#config-discovery) and found nothing. Either:

- Pass `--config /path/to/gonner.json`, or
- Put `gonner.json` in your CWD, or
- Bind-mount it at `/etc/gonner/gonner.json`.

Use `gonner validate --config <path>` to confirm gonner can read the file before deploying.

### "missing required environment variables: …"

Your config uses `{{env://VAR}}` for a variable that is unset and has no default. Either provide the env var, or add a default with `{{env://VAR:fallback}}`.

### "config validation failed"

Gonner refuses to start with an invalid config. Run `gonner validate --config <path>` locally to see the same error list. Common causes:

- Duplicate `name` values in `run[]`.
- `dependsOn` pointing at a process that doesn't exist (or to itself).
- A dependency cycle.
- `instances < 1`.
- Invalid `stopSignal` (must be one of `SIGTERM`, `SIGINT`, `SIGHUP`, `SIGQUIT`, `SIGUSR1`, `SIGUSR2`, `SIGKILL`).
- `group` set without `user`.

### Process is "skipped" unexpectedly

A `whenAll` / `whenAny` block evaluated false. Gonner logs the reason:

```
[gonner] Skipping "Worker": condition not met (env WORKER_ENABLED=TRUE)
```

Re-check the env var value or file path. Conditions are evaluated **once at startup**, not periodically.

---

## Runtime

### Process keeps restarting

- Inspect process stdout / `logFile` for the real error.
- Set `maxRetries` to bound the restart loop and fail fast in CI / staging.
- Check `gonner_process_restarts_total` if you're scraping `/metrics`.

If restarts only happen during deploys, check that your reverse proxy isn't health-checking too aggressively before the process is ready.

### Process exits cleanly but you expected it to restart

A clean exit (code 0) does **not** trigger restart, even with `autoRestart: true`. This is intentional — a successful one-shot command should not loop. Use a script that never exits cleanly if you want continuous restart on success.

### Critical process triggers shutdown but you didn't want that

Remove `critical: true`. The "critical" flag is for processes whose loss should bring down the whole container so an orchestrator can restart everything cleanly. For best-effort services use `autoRestart: true` without `critical`.

### Signals not reaching children

`gonner` forwards SIGHUP / SIGUSR1 / SIGUSR2 to children via their process groups (`Setpgid`). If a child re-execs into another shell that ignores signals, gonner can't help — fix the wrapper script.

To check what gonner is doing, watch its stderr; every forwarded signal is logged:

```
[gonner] Forwarding signal hangup to child processes
```

### Zombies accumulating

This only matters when gonner is PID 1 (containers). Confirm:

```
[gonner] Running as PID 1, starting zombie reaper
```

If you see `Not PID 1 (pid=N), skipping zombie reaper`, gonner is not actually the container's entrypoint. Common causes: `ENTRYPOINT ["/bin/sh", "-c", "gonner"]` (use exec form: `ENTRYPOINT ["gonner"]`), or a shell wrapper.

---

## Health endpoint

### "connection refused" on `/health`

- Confirm `health.port` is set in the config (or `--health-port`).
- Look for `[gonner] Health endpoint listening on http://...:N` in stderr.
- If `bindAddr` is `127.0.0.1`, you can only reach it from inside the container.

### `401 unauthorized` on `/status` or `/metrics`

`health.authToken` is set. Send `Authorization: Bearer <token>`. Set `GONNER_HEALTH_TOKEN` in the environment of the `gonner status` invocation, or use `curl`:

```bash
curl -H "Authorization: Bearer $TOKEN" http://localhost:8089/status
```

### Slowloris or DoS concern

The health server has built-in timeouts (`ReadHeaderTimeout=5s`, `Read/Write=15s`, `Idle=60s`, `MaxHeaderBytes=64KiB`). If you front gonner with a reverse proxy, you can keep those defaults; nothing further is needed.

---

## Logs

### Process output not appearing on stdout

- Per-line scanning is used. If your child writes binary or extremely long lines (> 1 MiB), the scanner drops to the next newline. Increase your line-flush frequency in the child if possible.
- `gonner.json` parse errors go to stderr, not stdout.

### Log file is empty

Check that `logFile`'s parent directory is writable. If the path is a directory, gonner errors at startup. If permissions are wrong (e.g. mounted read-only), gonner logs:

```
[gonner] creating log directory /var/log: permission denied
```

### Logs are not being rotated

`logRotate.maxSizeMB` must be > 0 and `logFile` must be set. Rotation happens **during writes** — a quiet process won't rotate. Use `logrotate(8)` plus a reload signal for time-based rotation.

---

## Container exits immediately

A `critical` process is failing at startup. Check:

```bash
docker logs <container>
```

Look for `[gonner] CRITICAL: Process "<name>" failed — triggering full shutdown`. The process's own stdout/stderr appears just before that line.

Common causes:

- A `commandsBefore` step failed (e.g. migrations) — set `continueOnError: true` only if you've handled the failure mode.
- Missing dependency the process expects (DB not reachable, etc.) — add a `portOpen` or `commandSucceeds` condition so gonner waits.
- Env var interpolation produced an invalid command.

---

## Performance

### High CPU when no children are running

Most likely a tight restart loop on a failing process. Either:

- Increase `backoff.initialDelay` and `backoff.multiplier`.
- Set `maxRetries` so gonner stops trying after N attempts.

### High memory

Gonner itself uses ~10 MB. Memory growth is almost always from child processes. Use `gonner status` (or `ps`) to identify the offending PID.

---

## Filing an issue

When reporting, include:

1. `gonner version` output.
2. Sanitized `gonner.json` (redact secrets).
3. Relevant log lines (gonner's stderr **and** the affected process's output).
4. `docker inspect` / `kubectl describe pod` output if container-related.
5. `os.Getpid()`-style context (PID 1 vs not, host OS, kernel).
