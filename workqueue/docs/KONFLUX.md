# Konflux Integration

Factory Workqueue uses Konflux for automated builds and Testing Farm integration.

## What Happens on Push

1. **Unit Tests** - `go test ./...` runs first (blocks build if fails)
2. **Parallel Builds** - All 4 images build simultaneously:
   - `quay.io/hummingbird/factory-receiver`
   - `quay.io/hummingbird/factory-dispatcher`
   - `quay.io/hummingbird/factory-admin`
   - `quay.io/hummingbird/echo-reconciler`
3. **Image Tagging** - Each image tagged with commit SHA + `latest`
4. **Testing Farm** - All FMF tests run with built images

## Images

All images published to `quay.io/hummingbird/` namespace.

**Platform binaries** (receiver, dispatcher, admin):
- Built from root `Containerfile` with `BINARY` arg
- Base: `quay.io/hummingbird/go:1.26` (build) → `quay.io/hummingbird-community/static:latest` (runtime)
- Static binaries, no runtime dependencies
- **Hermetic builds** - Network isolated, deps prefetched

**Echo reconciler** (example/testing):
- Built from `examples/echo-reconciler/Containerfile`
- Base: `quay.io/hummingbird/go:1.26` (build) → `quay.io/hummingbird/core-runtime:latest` (runtime)
- **Hermetic builds** enabled

## Hermetic Builds

Pipeline runs all builds with `HERMETIC=true`:
- Network disabled during container build
- Dependencies fetched before build (`go mod download`)
- Prevents supply chain attacks via network access
- Reproducible builds (same inputs = same outputs)

## Testing Farm

After images build, Konflux triggers Testing Farm with these scenarios:

| Test | FMF File | Description |
|------|----------|-------------|
| Unit | `tests/unit.fmf` | In-process Go tests, no containers |
| Integration | `tests/integration/main.fmf` | In-process integration tests (no containers) |
| Container | `tests/container/main.fmf` | End-to-end with postgres |
| Authz | `tests/container/authz.fmf` | Cedar policy enforcement |
| Standalone | `tests/container/standalone.fmf` | Pull-model worker API |
| Stress | `tests/container/stress.fmf` | 10k items, high concurrency |

Tests discover images via env vars (auto-set by Konflux):
- `FACTORY_RECEIVER_IMAGE`
- `FACTORY_DISPATCHER_IMAGE`
- `FACTORY_ADMIN_IMAGE`
- `ECHO_RECONCILER_IMAGE`

## Pipeline Structure

Main pipeline: `.tekton/workqueue-push.yaml`

```
init → clone → fetch-deps → ┬─ unit-tests ─┐
                            └─ fuzz-tests ─┤
                                           ├→ build-receiver ───→ tag-latest ─┐
                                           ├→ build-dispatcher ─→ tag-latest ─┤
                                           ├→ build-admin ──────→ tag-latest ─┼→ testing-farm
                                           └─ build-reconciler ─→ tag-latest ─┘
```

- **Hermetic builds** - Network disabled during build (reproducible, secure)
- **Deps fetched once** - `go mod download` runs before tests/builds
- **Tests run in parallel** - Unit + fuzz both use cached deps
- **Builds run in parallel** - All 4 images build simultaneously
- **Testing Farm** - Waits for all images tagged

## Manual Testing

Test pipelines locally with `tkn` CLI:

```bash
tkn pipeline start workqueue-push \
  --param git-url=https://github.com/rsturla/factory \
  --param revision=main \
  --workspace name=workspace,claimName=workspace-pvc \
  --showlog
```

Or trigger via web console: **Konflux → factory-workqueue → Pipelines → Run**

## Secrets Required

Configured in Konflux console:

- **Quay.io robot account** - Push access to `quay.io/hummingbird/` org
- **Testing Farm API token** - For triggering test runs

## Onboarding Checklist

- [x] `.tekton/workqueue-push.yaml` created
- [x] FMF test plans defined
- [x] Test scripts expect Konflux images
- [ ] Import repo into Konflux
- [ ] Grant quay.io push access
- [ ] Configure Testing Farm token
- [ ] Verify first pipeline run succeeds

## Troubleshooting

**Build fails with "unauthorized" pushing to quay.io:**
- Check robot account has write access to `quay.io/hummingbird/*`
- Verify secret configured in Konflux namespace

**Testing Farm not triggered:**
- Check Testing Farm token configured
- Verify `testing-farm` ClusterTask exists in cluster
- Check pipeline logs for task skip reasons

**Tests fail with "image not found":**
- Ensure builds completed before Testing Farm task
- Check `runAfter` dependencies in pipeline
- Verify image tags match between build and test env vars
