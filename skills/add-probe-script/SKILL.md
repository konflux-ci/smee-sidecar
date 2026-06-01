---
name: add-probe-script
description: Use when adding, modifying, or removing an embedded probe script in cmd/scripts/, or when a liveness/readiness probe needs a new health check shell script
---

# Adding or Modifying a Probe Script

## Overview

Probe scripts are shell scripts embedded into the Go binary via `//go:embed` and
written to the shared volume at startup. Kubernetes liveness and readiness probes
execute them. Changes touch multiple files — missing any step causes silent failures
in production.

## Checklist

```
- [ ] cmd/scripts/<name>.sh — the script itself
- [ ] cmd/main.go — //go:embed directive + writeScriptsToVolume map entry
- [ ] cmd/health_checker_test.go — Go unit tests for writeScriptsToVolume
- [ ] test/scripts/test-health-scripts.sh — shell-level tests (run from repo root)
- [ ] test/config/system-test-setup.yaml — wire as probe if applicable
- [ ] test/scripts/run-system-test.sh — verify_probe_scripts() if the script must exist in-cluster
- [ ] AGENTS.md / README.md — update if user-visible behavior or probe examples change
```

## Step 1: Write the script

Create `cmd/scripts/<name>.sh`. Follow existing patterns:

- `set -euo pipefail` and `#!/bin/bash` shebang
- Use `SCRIPT_DIR` for sibling scripts on the shared volume (e.g. `check-file-age.sh`)
- Exit 0 on success, exit 1 on failure — Kubernetes probes use exit codes
- Accept configurable thresholds as positional args with defaults
- Use `HEALTH_FILE_PATH` (default `/shared/health-status.txt`), not hardcoded paths
- Write diagnostic messages to stdout/stderr for `kubectl exec` debugging

**Existing probe roles** (pick the pattern that matches the container):

| Script | Container | Behavior |
|--------|-----------|----------|
| `check-smee-health.sh` | gosmee-client liveness | File age + requires `status=success` |
| `check-sidecar-health.sh` | sidecar liveness | File age only (ignores success/failure) |
| `check-file-age.sh` | utility | Age check only; called by the probes above |

## Step 2: Embed and register in main.go

Add the `//go:embed` directive alongside the existing ones in `cmd/main.go`:

```go
//go:embed scripts/<name>.sh
var myNewScript []byte
```

Add the script to the `writeScriptsToVolume` map:

```go
scripts := map[string][]byte{
    "check-smee-health.sh":    smeeHealthScript,
    "check-sidecar-health.sh": sidecarHealthScript,
    "check-file-age.sh":       fileAgeScript,
    "<name>.sh":               myNewScript,  // add here
}
```

Scripts are written with mode `0755`, then set to `0555` (read-only + executable).
The overwrite logic handles container restarts where read-only files persist.

## Step 3: Go unit tests

In `cmd/health_checker_test.go`, the `writeScriptsToVolume` tests verify:
- Script file exists after write
- Permissions are `0555`
- Content contains expected strings (e.g. `#!/bin/bash`)
- Overwriting read-only files on restart works

Add assertions for the new script in the existing test cases — check file existence,
permissions, and at least one content marker.

## Step 4: Shell tests

`test/scripts/test-health-scripts.sh` invokes scripts as `cmd/scripts/<name>.sh` from the
repo root (not from `/shared`). It sets `HEALTH_FILE_PATH` to a temp file. Add cases covering:
- Missing input files (should exit 1)
- Valid input (should exit 0)
- Invalid/edge-case input (empty file, malformed content)
- Custom threshold parameters if applicable

Run from the repo root: `test/scripts/test-health-scripts.sh`

## Step 5: Wire as Kubernetes probe (if applicable)

In `test/config/system-test-setup.yaml`, add to the **correct** container (`gosmee-client`
or `sidecar`). Match timings to the container you attach to:

```yaml
livenessProbe:
  exec:
    command: ["/bin/bash", "/shared/<name>.sh"]
  initialDelaySeconds: 20   # gosmee-client (existing probe)
  # initialDelaySeconds: 15 # sidecar (existing probe)
  periodSeconds: 5
  timeoutSeconds: 3
  failureThreshold: 2
```

The shared volume is mounted at `/shared` on both `gosmee-client` and `sidecar`
containers. Scripts are available to either container.

## Common mistakes

| Mistake | Fix |
|---------|-----|
| Forgot `//go:embed` directive | Build succeeds but script is empty bytes; add directive directly above the var |
| Script not in `writeScriptsToVolume` map | Binary embeds it but never writes to disk; add map entry |
| Permissions not tested | Probe fails silently in K8s; assert `0555` in Go tests |
| Script uses `bash` features without `#!/bin/bash` | UBI minimal may default to `sh`; always use bash shebang |
| Only tested in Go, not shell | `go test` validates embedding; shell tests validate script logic |
| Hardcoded paths instead of env vars | Use `HEALTH_FILE_PATH` with a default of `/shared/health-status.txt` |
| Smee vs sidecar semantics | Smee probe fails on `status=failure`; sidecar probe only cares that the file is fresh |
| Forgot `verify_probe_scripts` | System test phase 0 fails even if unit/shell tests pass |
