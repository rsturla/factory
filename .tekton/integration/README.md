# Integration Testing via Testing Farm

IntegrationTestScenario for group snapshot testing in Konflux.

## How It Works

1. **PR opens** → Components build → Individual snapshots created
2. **Group snapshot** created when multiple components share branch name
3. **IntegrationTestScenario triggers** → Testing Farm pipeline runs
4. **Testing Farm** clones repo, runs FMF tests with all component images

## Files

- `testing-farm-group.yaml` - IntegrationTestScenario definition

## Environment Variables

Testing Farm Konflux integration automatically sets:

**Single component snapshot:**
- `IMAGE_URL` - The component image
- `IMAGE_NAME` - Component name

**Group snapshot:**
- `IMAGE_URL_RECEIVER` - receiver component image
- `IMAGE_URL_DISPATCHER` - dispatcher component image
- `IMAGE_URL_ADMIN` - admin component image
- `IMAGE_URL_RECONCILER` - reconciler component image
- `IMAGE_NAMES` - Space-separated component names

**All snapshots:**
- `SNAPSHOT_b64` - Full snapshot JSON (base64)
- `AUTHFILE_b64` - Pull secret (base64)

## FMF Plans

Two plans defined:

- **`workqueue/plans/workqueue.fmf`** - Workqueue component tests (run for component snapshots)
- **`workqueue/plans/factory-integration.fmf`** - Factory-wide tests (run for group snapshots)

Currently identical since workqueue is the only factory component. When factory grows (codegen, rpm-builder), factory-integration.fmf adds cross-component scenarios.

IntegrationTestScenario uses `plans/factory-integration` which derives test env vars:

```yaml
environment+:
    FACTORY_RECEIVER_IMAGE: ${IMAGE_URL_RECEIVER:-${IMAGE_URL}}
    FACTORY_DISPATCHER_IMAGE: ${IMAGE_URL_DISPATCHER:-${IMAGE_URL}}
    # ...
```

Fallback: single component snapshots use `IMAGE_URL` for all vars.

## Tests Run

All tests in `tests/container/`:
- `main.fmf` - End-to-end container tests
- `authz.fmf` - Cedar policy enforcement
- `standalone.fmf` - Pull model worker tests
- `stress.fmf` - 10k item throughput

Only run when `initiator == konflux` (via adjust+).

## Deployment

**After Konflux onboarding:**

1. Update namespace in `testing-farm-group.yaml`
2. Create secret with Testing Farm token:
   ```bash
   kubectl create secret generic testing-farm-secret \
     --from-literal=testing-farm-token=YOUR_TOKEN \
     -n factory-workspace
   ```
3. Apply IntegrationTestScenario:
   ```bash
   kubectl apply -f .tekton/integration/testing-farm-group.yaml
   ```

## Contexts

- `group` - Runs on group snapshots (multi-component PRs)
- `component_workqueue` - Runs on single workqueue builds

## Parameters

- `COMPOSE: Fedora-43` - Test environment OS
- `ARCH: x86_64` - Architecture
- `HARDWARE` - VM requirements (4GB RAM, 2 CPU)
- `TMT_PATH: workqueue` - Subdirectory for FMF discovery
- `TMT_PLAN: plans/factory-integration` - Which plan to run (relative to TMT_PATH)
- `TIMEOUT: 120` - Max runtime (minutes)

## Triggering

**Automatic:**
- Every PR that modifies workqueue component
- Group snapshots for multi-component PRs

**Manual rerun:**
```bash
kubectl label snapshot SNAPSHOT_NAME \
  test.appstudio.openshift.io/run=factory-testing-farm
```

Or comment `/retest` on PR.

## Viewing Results

1. Click snapshot link in PR
2. Navigate to "Pipeline runs"
3. Find `factory-testing-farm-*` run
4. Click "Logs" or "Results"
5. Testing Farm artifacts URL in results

## Troubleshooting

**No tests run:**
- Check `initiator: konflux` in plan context
- Verify FMF plan path: `workqueue/plans/factory-integration.fmf`
- Verify FMF plan path: `plans/factory-integration.fmf`

**Image pull fails:**
- Ensure pull secret exists: `kubectl get secret -n factory-workspace`
- Testing Farm uses `AUTHFILE_b64` automatically

**Tests fail:**
- Check Testing Farm artifacts URL in pipeline results
- View full logs and screenshots
