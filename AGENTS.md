# AGENTS.md

## Project Overview

smee-sidecar is a Go sidecar container for Smee deployments. It provides health
checking and webhook event relay between a Smee client and a downstream service
(e.g. Pipelines as Code). It runs inside a Kubernetes pod alongside a Smee client.

## Code Structure

All application code lives in `cmd/main.go` (single-file, single package `main`).
Shell scripts in `cmd/scripts/` are embedded via `go:embed` directives.

- `cmd/main.go` -- Main application: HTTP handlers, health checker, metrics
- `cmd/scripts/` -- Embedded health check scripts for Kubernetes liveness probes
- `test/` -- Integration test infrastructure

## Key Concepts

The sidecar runs two HTTP servers:
- **Relay server** (`:8080`): Receives events from the Smee client. Health check
  events (identified by `X-Health-Check-ID` header) are consumed internally.
  All other events are reverse-proxied to the downstream service.
- **Management server** (`:9100`): Prometheus metrics at `/metrics`.

A background goroutine (`runHealthChecker`) periodically sends health check events
to the Smee server channel, waits for them to round-trip back through the client,
and writes results to a shared file for Kubernetes liveness probes.

## Build and Test

```bash
# Run unit tests (must be serial -- tests share global state)
go test -p 1 ./... -v

# Build container
podman build -t smee-sidecar:latest .
```

Tests use Ginkgo v2 / Gomega. Test files are in `cmd/` alongside `main.go`.
Tests reset global state in `BeforeEach` blocks -- do not run tests in parallel.

## Environment Variables

Required: `DOWNSTREAM_SERVICE_URL`, `SMEE_CHANNEL_URL`
Optional: `HEALTH_CHECK_INTERVAL_SECONDS` (default 30),
`HEALTH_CHECK_TIMEOUT_SECONDS` (default 20), `SHARED_VOLUME_PATH` (default /shared),
`HEALTH_FILE_PATH`, `INSECURE_SKIP_VERIFY`, `ENABLE_PPROF`

## Conventions

- All Go code is in the `cmd/` directory with `package main`
- Use `podman` instead of `docker` for container operations
- Health status files are written atomically (write to `.tmp`, then rename)
- Prometheus metrics: `smee_events_relayed_total` (counter), `health_check` (gauge)
- `sync.Once` is used for lazy initialization of HTTP client and reverse proxy
