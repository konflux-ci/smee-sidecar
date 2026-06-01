---
name: debug-instance
description: Use when a running smee-sidecar pod is unhealthy, restarting, not relaying events, or showing unexpected metrics
---

# Debugging a Running Instance

## Overview

The sidecar runs as container `sidecar` alongside `gosmee-client` in the same pod.
Debugging involves checking logs, health status files, Prometheus metrics, and
optionally pprof profiles.

## When to Use

- Pod is in CrashLoopBackOff or restarting unexpectedly
- `health_check` metric stuck at 0
- Events not reaching the downstream service
- Liveness probe failures

## Quick Reference

```bash
POD=$(kubectl get pod -l app=smee-sidecar -o jsonpath='{.items[0].metadata.name}')
```

| What | Command |
|------|---------|
| Sidecar logs | `kubectl logs "$POD" -c sidecar` |
| Client logs | `kubectl logs "$POD" -c gosmee-client` |
| Health file | `kubectl exec "$POD" -c sidecar -- cat /shared/health-status.txt` |
| Metrics | `kubectl port-forward "$POD" 9100:9100` then `curl localhost:9100/metrics` |
| Probe scripts | `kubectl exec "$POD" -c sidecar -- ls -la /shared/*.sh` |

## Key Log Messages

- `"Starting Smee instrumentation sidecar..."` — process started (logged before env var validation)
- `"Health check completed: success"` — healthy round-trip
- `"Health check completed: failure"` — broken connectivity
- `"Failed to POST to smee server"` — can't reach smee channel URL
- `"FATAL:"` — startup failure (missing env var, can't write scripts)

## Health Status File

The sidecar writes status atomically to `HEALTH_FILE_PATH` (default
`/shared/health-status.txt`). Expected healthy content:

```
status=success
message=Health check completed successfully
```

File should update every `HEALTH_CHECK_INTERVAL_SECONDS` (default 30s).

## Probe Scripts

Three scripts at `/shared/`: `check-smee-health.sh`, `check-sidecar-health.sh`,
`check-file-age.sh`. All mode `0555`. Run manually:

```bash
kubectl exec "$POD" -c sidecar -- /bin/bash /shared/check-smee-health.sh
kubectl exec "$POD" -c sidecar -- /bin/bash /shared/check-sidecar-health.sh
```

## Prometheus Metrics

Port-forward to 9100, then:

```bash
curl -s localhost:9100/metrics | grep '^health_check '           # 1=healthy, 0=unhealthy
curl -s localhost:9100/metrics | grep '^smee_events_relayed_total '  # event counter
```

## pprof Profiling

Requires `ENABLE_PPROF=true` in pod env. Port-forward to 9100, then:

```bash
curl -s localhost:9100/debug/pprof/heap > heap.prof && go tool pprof heap.prof
curl -s localhost:9100/debug/pprof/goroutine?debug=2    # goroutine dump
curl -s localhost:9100/debug/pprof/profile?seconds=30 > cpu.prof  # CPU
```

## Manual Event Testing

Port-forward to 8080:

```bash
# Real webhook (proxied to downstream)
curl -X POST -H "Content-Type: application/json" -d '{"test":true}' localhost:8080/

# Health check event (consumed internally)
curl -X POST -H "X-Health-Check-ID: manual-test" \
  -H "Content-Type: application/json" \
  -d '{"type":"health-check","id":"manual-test"}' localhost:8080/
```

## Common Failure Modes

| Symptom | Likely Cause | Fix |
|---|---|---|
| CrashLoopBackOff | Missing `DOWNSTREAM_SERVICE_URL` or `SMEE_CHANNEL_URL` | Check env vars in deployment spec |
| `health_check` stuck at 0 | Smee server unreachable or client not forwarding | Check smee-server pod, verify `SMEE_CHANNEL_URL` |
| Liveness probe restarting pod | Health file stale or status=failure | Check health checker goroutine logs |
| Events not reaching downstream | Wrong `DOWNSTREAM_SERVICE_URL` or service down | Verify URL, check downstream pod |
| `Failed to write probe scripts` | Shared volume not mounted or wrong permissions | Check mount at `SHARED_VOLUME_PATH`, verify `fsGroup: 65532` |
