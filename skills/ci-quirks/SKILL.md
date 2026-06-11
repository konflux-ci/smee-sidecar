---
name: ci-quirks
description: Documents merge-blocking CI pipelines and non-obvious constraints (AGENTS.md line limit, serial tests, multi-arch builds). Use when CI fails unexpectedly, adding CI-visible files or env vars, or checking what blocks merge.
---

# CI/CD Quirks

## Overview

Six merge-blocking CI pipelines plus one advisory AI-assist workflow. Each has
non-obvious constraints that cause surprising failures.

## When to Use

- CI failed and the error isn't obvious
- Adding files or env vars that CI validates
- Checking which pipelines must pass before merge

## Quick Reference

| Pipeline | File | Blocks merge? |
|----------|------|---------------|
| Unit Tests & Linting | `.github/workflows/unit-tests-and-linting.yaml` | Yes |
| Go Lint | `.github/workflows/go-lint.yaml` | Yes |
| Differential ShellCheck | `.github/workflows/differential-shellcheck.yaml` | Yes |
| YAML Lint | `.github/workflows/yaml-lint.yaml` | Yes |
| System Test on Kind | `.github/workflows/system-test.yaml` | Yes |
| Konflux / Tekton | `.tekton/smee-sidecar-*.yaml` | Yes |
| Fullsend | `.github/workflows/fullsend.yaml` | No (advisory) |

## Pipeline Details

### 1. Unit Tests & Linting (GitHub Actions)

Triggers: push to `main`, PRs to `main`, merge queue. Runs:
- `go test -p 1` with coverage (uploaded to Codecov)
- `test/scripts/test-health-scripts.sh`
- **AGENTS.md line count** — hard limit of **300 lines**

### 2. Go Lint (GitHub Actions)

Triggers: PRs and merge queue only (no push to `main`). Path-filtered — runs
only when Go files, `go.mod`, `go.sum`, or golangci config change. Uses
golangci-lint with config in `.golangci.yml`.

### 3. Differential ShellCheck (GitHub Actions)

Triggers: PRs and merge queue only. Lints only **changed** `.sh` files via
`redhat-plumbers-in-action/differential-shellcheck`. Requires `fetch-depth: 0`
for diff against the base branch. Covers `cmd/scripts/` (embedded probes) and
`test/scripts/`.

### 4. YAML Lint (GitHub Actions)

Triggers: PRs and merge queue only. Path-filtered — runs when `**/*.yaml`,
`**/*.yml`, `.yamllint`, or the workflow itself changes. Uses `yamllint` with
rules in `.yamllint` (GitHub Actions `on:` keys are excluded via
`truthy.check-keys: false`).

### 5. System Test on Kind (GitHub Actions)

Triggers: push to `main`, PRs to `main`, merge queue. Builds with `docker`
(not podman — GitHub Actions runners), loads into Kind via
`helm/kind-action@v1.14.0`, runs full system test.

### 6. Konflux / Tekton

Builds multi-arch images (`linux/x86_64`, `linux/arm64`). Pushes to:
- PR: `quay.io/.../smee-sidecar:on-pr-{{revision}}` (expires after 5 days)
- Main: `quay.io/.../smee-sidecar:{{revision}}`

PR pipeline cancels in-progress runs; push pipeline does not.

### 7. Fullsend (AI-Assisted) — not merge-blocking

Dispatches to `konflux-ci/.fullsend` workflows. Uses `pull_request_target` for
security. `/fs-fix-stop` comment disables the fix agent via `fullsend-no-fix` label.

## Common Mistakes

- **AGENTS.md over 300 lines**: CI hard-fails. Check with `wc -l < AGENTS.md`.
- **Architecture-specific code**: Tekton builds for x86_64 and arm64 — no
  arch-specific syscalls or CGo.
- **Merge queue**: All merge-blocking GitHub Actions workflows trigger on
  `merge_group` events. PRs go through a merge queue before landing on `main`.
- **Renovate + Mintmaker**: Automated dependency PRs from both systems. Generally
  safe to merge after CI passes.
