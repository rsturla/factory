# Konflux Pipelines

Factory monorepo CI/CD with CEL path filtering.

**Repo:** `github.com/hummingbird-org/factory` (monorepo root)

## Monorepo Structure

```
factory/
├── .tekton/              # All pipelines (root)
│   ├── workqueue-pull-request.yaml
│   ├── workqueue-push.yaml
│   ├── workqueue-fuzz.yaml
│   ├── codegen-*.yaml    # Future components
│   └── rpm-*.yaml
└── workqueue/            # Component subdirectory
    ├── Containerfile
    └── ...
```

**CEL filtering:** Pipelines trigger only on their component paths.

## Workqueue Pipelines

### 1. Pull Request (`workqueue-pull-request.yaml`)

**Trigger:** PR to main, `workqueue/` changed

```
init → clone → prefetch → ┬─ lint ─────┐
                          ├─ test-full ┤
                          └─ fuzz-quick┘
                                        ├→ 4x build → testing-farm
```

**Images:** `pr-{number}-{sha}`

### 2. Push (`workqueue-push.yaml`)

**Trigger:** Push to main, `workqueue/` changed

```
init → clone → prefetch → ┬─ lint ─────┐
                          ├─ test-full ┤
                          └─ fuzz-quick┘
                                        ├→ 4x build → tag-latest → testing-farm
```

**Images:** `{sha}` + `:latest`

### 3. Fuzz (`workqueue-fuzz.yaml`)

**Trigger:** Push to main with `workqueue/` changes

```
clone → fuzz-long (30min/target)
```

## CEL Path Filtering

```yaml
pipelinesascode.tekton.dev/on-cel-expression: |
  event == "pull_request" &&
  target_branch == "main" &&
  ("workqueue/".pathChanged() || ".tekton/workqueue-".pathChanged())
```

Triggers on:
- `workqueue/` source changes
- `.tekton/workqueue-*.yaml` pipeline changes

## Adding Components

1. Create `{component}-pull-request.yaml` in `.tekton/`
2. CEL filter: `"{component}/".pathChanged()`
3. Update paths: `path: {component}`, `CONTEXT: ./{component}`
4. Register in Konflux

## test-full Task

Sidecars: postgres, dynamodb, rustfs  
Tests: Go (all backends), Python, Rust, benchmarks

## Hermetic Builds

All builds: `HERMETIC=true`
- Deps prefetched from `workqueue/`
- Network disabled during build

## Shared Tasks

- init, git-clone, prefetch-dependencies
- buildah, oci-copy

## Testing Farm

PR and push both trigger Testing Farm.

## Onboarding

1. Import to Konflux
2. Grant quay.io push
3. Configure Testing Farm token
4. Auto-triggers (path filtered)
