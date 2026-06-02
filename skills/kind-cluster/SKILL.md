---
name: kind-cluster
description: Use when setting up, running, or tearing down a Kind cluster for smee-sidecar system tests, or when system tests fail in CI
---

# Kind Cluster for System Tests

## Overview

System tests deploy the sidecar into a Kind cluster and verify event relaying,
health check round-trips, liveness probe failure/recovery, and file-based health
status across six phases.

## When to Use

- Running system tests locally
- Debugging system test failures in CI
- Iterating on sidecar behavior with a real Kubernetes environment

## Prerequisites

`kind`, `kubectl`, `podman` (or `docker`), and `curl` (used by `run-system-test.sh`).

## Quick Setup

```bash
kind create cluster --name smee-test

# Prefer podman (repo convention). kind load docker-image reads the docker daemon only.
podman build -t smee-sidecar-test:latest .
podman save smee-sidecar-test:latest -o /tmp/smee-sidecar.tar
kind load image-archive /tmp/smee-sidecar.tar --name smee-test

# With docker instead:
# docker build -t smee-sidecar-test:latest .
# kind load docker-image smee-sidecar-test:latest --name smee-test

kubectl apply -f test/config/system-test-setup.yaml
kubectl wait --for=condition=Available deployment/smee-server --timeout=60s
kubectl wait --for=condition=Available deployment/smee-client --timeout=60s

test/scripts/run-system-test.sh
```

## What Gets Deployed

`test/config/system-test-setup.yaml` creates:

1. **smee-server** — gosmee server on port 3333, exposed via service on port 80
2. **smee-client + sidecar** — gosmee client forwarding to `localhost:8080`
   (the sidecar). Shared `emptyDir` volume at `/shared`
3. **dummy-downstream** — echo server logging received requests on port 8080

Pod runs as non-root `65532`, read-only root filesystem, `fsGroup: 65532`.

## Test Phases

The test (`test/scripts/run-system-test.sh`) runs six phases (0 through 5):

0. Verify probe scripts exist and are executable
1. Initial health (0 restarts, metric=1, file `success`) and event relaying to dummy-downstream
2. Scale down smee-server to break communication
3. Health degrades (metric=0, file `failure`); **gosmee-client** restarts (its liveness probe runs `check-smee-health.sh` on the shared health file — the test watches `containerStatuses[0]`, not the sidecar container)
4. Restore smee-server
5. Recovery, stability, and ongoing health-file updates

## Tear Down

```bash
kind delete cluster --name smee-test
```

## Common Mistakes

- **Using `kind load docker-image` with podman**: This reads from the docker
  daemon's image store. Use `podman save` + `kind load image-archive` instead.
- **Stale port-forwards**: If tests fail mid-run, leftover port-forwards block
  re-runs. Kill with `pkill -f "kubectl port-forward"`.
- **Mismatched timing**: `system-test-setup.yaml` sets `HEALTH_CHECK_INTERVAL_SECONDS=10`
  and `HEALTH_CHECK_TIMEOUT_SECONDS=8` (production defaults are 30/20). If you change
  timing, update those env vars and the sleep/retry loops in `run-system-test.sh`.
- **CI cluster name**: GitHub Actions uses `cluster_name: kind` via `helm/kind-action`;
  local setup above uses `--name smee-test` — only matters if you run `kind` commands manually.
- **Faster iteration**: After initial setup, rebuild and reload without
  recreating the cluster:
  ```bash
  podman build -t smee-sidecar-test:latest . && \
    podman save smee-sidecar-test:latest -o /tmp/smee-sidecar.tar && \
    kind load image-archive /tmp/smee-sidecar.tar --name smee-test && \
    kubectl rollout restart deployment/smee-client
  ```
