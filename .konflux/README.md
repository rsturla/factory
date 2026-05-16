# Konflux Onboarding

Infrastructure-as-code definitions for Konflux CI/CD.

## Prerequisites

1. Konflux GitHub App installed on `github.com/rsturla/factory`
2. Access to Konflux workspace/namespace
3. Quay.io robot account with push access to `quay.io/hummingbird/` or your org
4. (Optional) Testing Farm API token for integration tests

## Setup

### 1. Update Namespace

Replace `REPLACE_WITH_YOUR_NAMESPACE` in all YAML files with your Konflux namespace:

```bash
# Find your namespace
kubectl config view --minify -o jsonpath='{.contexts[0].context.namespace}'

# Update files
sed -i 's/REPLACE_WITH_YOUR_NAMESPACE/your-namespace/g' .konflux/*.yaml
sed -i 's/REPLACE_WITH_YOUR_NAMESPACE/your-namespace/g' .tekton/integration/*.yaml
```

### 2. Create Application

```bash
kubectl apply -f .konflux/application.yaml
```

### 3. Create Workqueue Component

```bash
kubectl apply -f .konflux/component-workqueue.yaml
```

This triggers Konflux to:
- Detect `.tekton/workqueue-*.yaml` pipelines
- Configure Pipelines-as-Code on your repo
- Start building on PRs and pushes

### 4. Configure Quay.io Push Secret

Konflux needs credentials to push images to Quay.io:

```bash
# Create secret
kubectl create secret docker-registry quay-push-secret \
  --docker-server=quay.io \
  --docker-username=<quay-username> \
  --docker-password=<quay-robot-token> \
  -n <your-namespace>

# Link to pipeline service account
kubectl patch serviceaccount pipeline \
  -n <your-namespace> \
  -p '{"secrets":[{"name":"quay-push-secret"}]}'
```

Get Quay.io robot token:
1. Go to https://quay.io/organization/hummingbird (or your org)
2. Settings → Robot Accounts → Create Robot Account
3. Grant "Write" permission to repositories
4. Use username (`hummingbird+robot_name`) and token

### 5. Configure Testing Farm (Optional)

For integration testing with Testing Farm:

```bash
# Create secret
kubectl create secret generic testing-farm-secret \
  --from-literal=testing-farm-token=<your-token> \
  -n <your-namespace>

# Apply IntegrationTestScenario
kubectl apply -f .tekton/integration/testing-farm-group.yaml
```

Get Testing Farm token: https://api.dev.testing-farm.io/

## Verify

### Check Resources

```bash
kubectl get applications -n <your-namespace>
kubectl get components -n <your-namespace>
kubectl get pipelineruns -n <your-namespace>
```

### Trigger Pipeline

**Via PR:** Open pull request → Konflux auto-runs `workqueue-pull-request.yaml`

**Via Push:** Push to main → Konflux auto-runs `workqueue-push.yaml`

**Manual:**
```bash
# Find pipeline
kubectl get pipelineruns -n <your-namespace>

# View logs
tkn pipelinerun logs <pipelinerun-name> -n <your-namespace> -f
```

### Monitor

- Konflux UI: Applications → factory → Components → workqueue → Pipeline runs
- GitHub: PR checks show Konflux pipeline status
- Quay.io: Images appear at `quay.io/hummingbird/factory-*:pr-N-SHA` or `:latest`

## Pipelines

| Pipeline | Trigger | Duration | Builds |
|----------|---------|----------|--------|
| `workqueue-pull-request.yaml` | PRs touching `workqueue/` | ~8min | 4 images (`:pr-N-SHA`) |
| `workqueue-push.yaml` | Push to main, `workqueue/` changed | ~10min | 4 images (`:SHA` + `:latest`) |
| `workqueue-fuzz.yaml` | Push to main, `workqueue/` changed | ~2h | None (long fuzz testing) |

## Images Built

All tagged to Quay.io:

- `quay.io/hummingbird/factory-receiver`
- `quay.io/hummingbird/factory-dispatcher`
- `quay.io/hummingbird/factory-admin`
- `quay.io/hummingbird/echo-reconciler`

## Testing Farm Integration Tests

After images build, Testing Farm runs:
- `workqueue/tests/container/main.fmf` - End-to-end tests
- `workqueue/tests/container/authz.fmf` - Cedar policy tests
- `workqueue/tests/container/standalone.fmf` - Pull-model worker tests
- `workqueue/tests/container/stress.fmf` - 10k items throughput

Results appear in Konflux pipeline runs under "Testing Farm" task.

## Troubleshooting

**No pipelines run:**
- Check PipelinesAsCode annotations: `kubectl get pipelineruns -n <namespace>`
- Verify GitHub App installed and has repo access
- Check CEL path filtering matches changed files

**Build fails with "permission denied" pushing to Quay:**
- Verify `quay-push-secret` exists and is linked to `pipeline` service account
- Check Quay.io robot account has write permission

**Testing Farm not running:**
- Check secret exists: `kubectl get secret testing-farm-secret -n <namespace>`
- Verify IntegrationTestScenario applied: `kubectl get integrationtestscenario -n <namespace>`
- Check namespace matches in `testing-farm-group.yaml`

**Hermetic build fails:**
- Ensure `go mod download` removed from Containerfile (handled by cachi2)
- Check prefetch-dependencies task succeeded
- Verify `HERMETIC: 'true'` set on buildah tasks

## Adding Future Components

When adding `codegen/` or `rpm-builder/`:

1. Create component YAML in `.konflux/component-<name>.yaml`
2. Create pipelines in `.tekton/<name>-*.yaml` with CEL filtering
3. Update `testing-farm-group.yaml` contexts if needed
4. Apply: `kubectl apply -f .konflux/component-<name>.yaml`
