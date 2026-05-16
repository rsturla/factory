# Architecture

Factory architecture — imperative orchestration + workqueue execution.

## System Overview

**Execution Modes:**
- **Local dev** — `bun run pipeline.ts` on laptop (testing)
- **Production** — K8s Job spawned by controller (triggered)

Both modes execute the same pipeline.ts code.

```
┌──────────────────────────────────────────────────────────┐
│  Pipeline Execution Context                               │
│  (Local: dev laptop | Production: K8s Job)                │
│  ┌────────────────────────────────────────────────────┐  │
│  │  pipeline.ts (TypeScript program)                  │  │
│  │  - import { claude, judge, run } from SDK          │  │
│  │  - await claude("analyze", { prompt: "..." })      │  │
│  │  - if (analysis.output.fixable) { ... }            │  │
│  │  - const [test1, test2] = await Promise.all([...]) │  │
│  └────────────────────┬───────────────────────────────┘  │
│                       │ HTTP (create stage, poll status)  │
└───────────────────────┼───────────────────────────────────┘
                        │
                        ▼
┌──────────────────────────────────────────────────────────┐
│                    Factory API Service                    │
│  ┌────────────────────────────────────────────────────┐  │
│  │  POST /api/v1/imperative/runs                      │  │
│  │  POST /api/v1/imperative/runs/{id}/stages          │  │
│  │  GET  /api/v1/imperative/runs/{id}/stages/{id}     │  │
│  └────────────────────┬───────────────────────────────┘  │
│                       │ Create stage record              │
│  ┌────────────────────▼───────────────────────────────┐  │
│  │  PostgreSQL Run Store                              │  │
│  │  - pipeline_runs table                             │  │
│  │  - stage_runs table                                │  │
│  │  - outbox table (reliable enqueueing)              │  │
│  └────────────────────┬───────────────────────────────┘  │
│                       │ Outbox poller → enqueue          │
└───────────────────────┼───────────────────────────────────┘
                        │
                        ▼
┌──────────────────────────────────────────────────────────┐
│                   Workqueue Layer                         │
│  ┌────────────────────────────────────────────────────┐  │
│  │  sf-stage queue                                    │  │
│  │  - Stage execution requests                        │  │
│  │  - Priority ordering                               │  │
│  │  - At-least-once delivery                          │  │
│  └────────────────────┬───────────────────────────────┘  │
└───────────────────────┼───────────────────────────────────┘
                        │
                        ▼
┌──────────────────────────────────────────────────────────┐
│              Sandbox Manager (Reconciler)                 │
│  ┌────────────────────────────────────────────────────┐  │
│  │  1. Dequeue stage from sf-stage                    │  │
│  │  2. Provision sandbox (Docker / OpenShell)         │  │
│  │  3. Mount resources (git repos, S3, HTTP)          │  │
│  │  4. Copy prompt to /workspace/.prompt.md           │  │
│  │  5. Execute agent command                          │  │
│  │  6. Collect /output/output.json                    │  │
│  │  7. Update stage record with output                │  │
│  │  8. Enqueue to sf-output queue                     │  │
│  └────────────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────────────┘
                        │
                        ▼
┌──────────────────────────────────────────────────────────┐
│                   Output Processor                        │
│  ┌────────────────────────────────────────────────────┐  │
│  │  1. Dequeue from sf-output                         │  │
│  │  2. Run verification gates                         │  │
│  │     - Secrets scan                                 │  │
│  │     - Diff size check                              │  │
│  │     - Test coverage threshold                      │  │
│  │  3. Execute output handler                         │  │
│  │     - PR: create branch + PR via git-proxy         │  │
│  │     - Review: post comment                         │  │
│  │     - Report: upload to storage                    │  │
│  │     - Patch: commit changes                        │  │
│  └────────────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────────────┘
```

## Execution Modes

Pipelines run in two modes:

### Local Development

```bash
cd my-repo/.factory
bun run pipeline.ts
```

**Use case:** Testing pipeline logic, debugging prompts, validating changes before deploy.

**Environment:** Developer's laptop, Bun runtime, FACTORY_API_ENDPOINT → staging/prod API.

**Trigger:** Manual execution.

### Production Deployment

Pipeline deployed to K8s cluster with trigger definition:

```typescript
// pipeline.ts
export const trigger = triggers.jira({
  query: 'project = HUM AND labels = cve-needs-attention',
  poll: '5m',
  params: (issue) => ({ CVE_ID: issue.fields.customfield_10667.value }),
});
```

**How it works:**
1. Controller discovers pipeline (from ConfigMap)
2. Runs `bun run pipeline.ts --dry-run` → extracts trigger JSON
3. Starts trigger handler (Jira poller, webhook server, cron scheduler)
4. When trigger fires → spawns K8s Job with env vars from trigger params
5. Job runs `bun run pipeline.ts` → pipeline executes → stages run

**Use case:** Automated response to events (Jira issues, webhooks, schedules, GitHub events).

**Environment:** K8s Job, Bun container, FACTORY_API_ENDPOINT → prod API.

**Trigger:** Automatic (Jira poll, webhook, cron, GitHub event).

## Components

### 1. TypeScript Pipeline (User Code)

Imperative TypeScript program using Factory SDK.

**Responsibilities:**
- Define workflow logic (analyze → fix → test → PR)
- Control flow (`if`, `await`, `Promise.all`)
- Pass data between stages (`inputs: { analysis: analysis.output }`)
- Call SDK functions (`claude()`, `judge()`, `run()`)

**Location:** User's repository (e.g., `.factory/pipeline.ts`)

**Execution modes:**
- **Local dev** — `bun run pipeline.ts` on laptop (manual testing)
- **Production** — K8s Job spawned by controller when trigger fires (automated)

**Example:**
```typescript
const analysis = await claude("analyze", { prompt: "prompts/analyze.md" });
if (analysis.output.auto_fixable) {
  const patch = await claude("patch", { prompt: "prompts/patch.md" });
}
```

### 2. Factory SDK

TypeScript library for building pipelines.

**Functions:**
- `claude(name, opts)` — Claude Code stage with smart defaults
- `judge(name, opts)` — LLM verification with typed verdict
- `run(name, opts)` — Custom agent execution
- `stage(name, opts)` — Low-level builder for custom stage types
- `git()`, `http()`, `s3()` — Resource bindings
- `pr()` — Output action for PR creation

**Responsibilities:**
- Create run on first `claude()`/`judge()`/`run()` call
- Enqueue stages via API (`POST /api/v1/imperative/runs/{id}/stages`)
- Poll stage completion (`GET /api/v1/imperative/runs/{id}/stages/{id}`)
- Return typed results (`StageResult`, `JudgmentResult`)

**Location:** `sdk/typescript/`

**Published:** npm package `@hummingbird/factory-sdk`

### 3. Factory API Service

HTTP API for SDK interaction.

**Endpoints:**
- `POST /api/v1/imperative/runs` — Create run
- `POST /api/v1/imperative/runs/{id}/stages` — Create stage
- `GET /api/v1/imperative/runs/{id}/stages/{id}` — Get stage status/output
- `GET /api/v1/runs` — List runs (legacy)
- `GET /api/v1/runs/{id}` — Get run details (legacy)

**Responsibilities:**
- Accept stage creation requests from SDK
- Store stages in PostgreSQL
- Enqueue stages to workqueue via outbox pattern
- Return stage status + output for SDK polling

**Location:** `cmd/factory-api/`, `internal/api/`

**Deployment:** Kubernetes deployment, HTTP service on port 8080

### 4. PostgreSQL Run Store

Persistent storage for runs, stages, audit events.

**Tables:**
- `pipeline_runs` — Run metadata
- `stage_runs` — Stage metadata, output, status
- `audit_events` — Action log
- `outbox` — Reliable enqueueing buffer

**Responsibilities:**
- Store run/stage records
- Provide transactional outbox for reliable enqueueing
- Support polling queries from SDK via API
- Maintain audit trail

**Schema:** `internal/runstore/postgres/schema.sql`

**Deployment:** PostgreSQL 16+

### 5. Outbox Poller

Background process in API service.

**Responsibilities:**
- Poll `outbox` table every 1s
- Send pending entries to workqueue
- Mark sent entries
- Exponential backoff on errors

**Pattern:** Transactional outbox (guaranteed enqueueing)

**Location:** `internal/api/server.go` (`StartOutboxPoller`)

### 6. Workqueue

Message queue for stage execution.

**Queues:**
- `sf-stage` — Stage execution requests
- `sf-output` — Output processing requests

**Responsibilities:**
- Durable message storage
- Priority ordering
- At-least-once delivery
- Retry with backoff

**Implementation:** External (e.g., Redis Streams, NATS JetStream, AWS SQS)

**Integration:** Via workqueue SDK

### 7. Sandbox Manager

Reconciler consuming `sf-stage` queue.

**Lifecycle:**
1. **Dequeue** — Pull stage from `sf-stage`
2. **Provision** — Create isolated sandbox (Docker container or OpenShell VM)
3. **Mount resources** — Clone git repos, mount S3 buckets, fetch HTTP resources
4. **Copy inputs** — Write stage inputs to `/workspace/inputs/{name}/output.json`
5. **Capture git commit** — If `changeDetection=git|auto` and `/workspace` is git repo: `git rev-parse HEAD` → store in `stage.InitialGitCommit`
6. **Copy prompt** — Write prompt to `/workspace/.prompt.md`
7. **Execute** — Run agent command (e.g., `claude-code --print --prompt-file /workspace/.prompt.md`)
8. **Collect** — Read `/output/output.json`
9. **Detect changes** — Based on `changeDetection` mode:
   - `git`: `git diff {initial}..HEAD`, extract changed files
   - `explicit`: Copy `/output/changes/` directory
   - `auto`: Try `git` first, fall back to `explicit`
10. **Store** — Package changes as tar.gz, upload to artifact storage (S3/local), store URL + metadata in PostgreSQL
11. **Enqueue output** — Send to `sf-output` queue (just stage ID, not data)
12. **Cleanup** — Destroy sandbox

**Responsibilities:**
- Sandbox isolation (network, filesystem)
- Resource provisioning
- Agent execution
- Output collection
- Artifact upload for large outputs
- Retry on failure

**Location:** `cmd/factory-sandbox-manager/`

**Deployment:** Kubernetes deployment, scales horizontally

### 8. Output Processor

Reconciler consuming `sf-output` queue.

**Lifecycle:**
1. **Dequeue** — Pull stage ID from `sf-output` queue
2. **Fetch** — Retrieve stage record from PostgreSQL
3. **Download artifact** — If `_artifact_url` present:
   - Download tar.gz from S3
   - Extract files into memory
   - Merge into output (replaces `files` field)
4. **Verify** — Run verification gates
   - Secrets scan (detect leaked credentials)
   - Diff size check (5000 lines, 100 files max)
   - Test coverage threshold
5. **Execute handler** — Based on output type:
   - **PR** — Create branch, push changes, create PR via git-proxy
   - **Review** — Post comment with verdict
   - **Report** — Upload to storage
   - **Patch** — Commit changes to repo
   - **Changeset** — Bundle changes for later

**Responsibilities:**
- Artifact download for large outputs
- Verification gates (safety checks)
- Git operations (via git-proxy)
- PR creation (via GitHub/GitLab API)
- Error handling (reject unsafe outputs)

**Location:** `cmd/factory-output-processor/`

**Deployment:** Kubernetes deployment

### 9. Git Proxy

Secure git operations service.

**Responsibilities:**
- Authenticate with git providers (GitHub, GitLab, Gitea)
- Execute git operations (clone, push, create PR)
- Enforce policies (branch protection, signed commits)
- Rate limiting
- Audit logging

**Why separate:** Isolates credentials from sandboxes. Agents never see git tokens.

**Location:** External service (not in this repo)

**Integration:** Via HTTP API

### 10. Artifact Storage

Object storage for all stage outputs.

**Implementation:** S3, MinIO, or local filesystem

**What gets stored:**
- All outputs with file changes
- Packaged as tar.gz archives
- Path: `s3://bucket/artifacts/{stage-id}/output.tar.gz` or `file:///path/artifacts/{stage-id}/output.tar.gz`

**Lifecycle:**
1. **Upload** — Sandbox-manager packages `/output/changes/`, uploads to artifact storage
2. **Reference** — URL stored in stage record: `{ "_artifact_url": "s3://...", "_artifact_type": "tar.gz" }`
3. **Download** — Output-processor downloads when processing output
4. **Extract** — Files extracted into memory, merged into output
5. **Retention** — Artifacts persist after pipeline completion (audit trail)
6. **Cleanup** — Manual or automated retention policy

**Backends:**

| Backend | Use Case | URL Format |
|---------|----------|------------|
| `local` | Development, testing | `file:///tmp/factory-artifacts/artifacts/{stage-id}/output.tar.gz` |
| `s3` | Production (AWS) | `s3://bucket/artifacts/{stage-id}/output.tar.gz` |
| `minio` | Production (self-hosted) | `s3://bucket/artifacts/{stage-id}/output.tar.gz` |

**Why artifact storage:**
- Consistent behavior (all outputs stored same way)
- Simpler code (no threshold logic)
- Better durability (audit trail)
- Scales to TB-size outputs

**Configuration:**
```bash
# Local (default for dev)
ARTIFACT_BACKEND=local
ARTIFACT_LOCAL_DIR=/tmp/factory-artifacts

# S3 production
ARTIFACT_BACKEND=s3
ARTIFACT_BUCKET=factory-artifacts
ARTIFACT_ENDPOINT=https://s3.amazonaws.com
ARTIFACT_REGION=us-east-1
ARTIFACT_ACCESS_KEY=...
ARTIFACT_SECRET_KEY=...

# MinIO self-hosted
ARTIFACT_BACKEND=minio
ARTIFACT_ENDPOINT=https://minio.internal:9000
ARTIFACT_BUCKET=factory-artifacts
ARTIFACT_ACCESS_KEY=...
ARTIFACT_SECRET_KEY=...
```

**Location:** `internal/artifact/`

**Deployment:** 
- Local: filesystem (default)
- S3: AWS S3 service
- MinIO: Self-hosted object storage

**Change Detection:**

Artifacts contain changed files only, not entire workspaces. Two detection modes:

| Mode | How it works | When to use |
|------|-------------|-------------|
| `git` | Tracks git commit before agent starts, diffs after completion | Code changes in git repos |
| `explicit` | Agent copies files to `/output/changes/` manually | Non-git workspaces, selective outputs |
| `auto` (default) | Try `git` if repo detected, fall back to `explicit` | General purpose |

**Git-based detection:**
1. After resources loaded, capture: `git rev-parse HEAD` → `stage.InitialGitCommit`
2. Agent modifies files, commits changes
3. After agent completes: `git diff --name-only {initial}..HEAD` → list changed files
4. Copy changed files from sandbox → tar.gz → artifact storage
5. Output includes: `_changed_files`, `_initial_commit`, `_change_detection: "git"`

**Explicit detection:**
1. Agent copies files to `/output/changes/` directory
2. After agent completes: tar.gz `/output/changes/` → artifact storage
3. Output includes: `_change_detection: "explicit"`

**Benefits of git-based:**
- No manual file copying in prompts
- Automatic detection of all modified files
- Git history preserved in sandbox (can `git show`, `git log`)
- Smaller artifacts (only changed files, not entire workspace)

**SDK usage:**
```typescript
await claude("refactor", {
  prompt: "prompts/refactor.md",
  changeDetection: "git",  // or "explicit" or "auto"
  resources: [git("read-write")],
});
```

### 11. Pipeline Controller

Watches pipeline deployments and manages triggers.

**Responsibilities:**
- Discover pipeline deployments (from ConfigMap/CRD)
- Clone pipeline repos
- Extract trigger definitions (`bun run pipeline.ts --dry-run`)
- Subscribe to trigger sources:
  - Jira: poll API for matching issues
  - Webhook: expose HTTP endpoints
  - Schedule: cron scheduler
  - GitHub: webhook listener
- Spawn K8s Jobs on trigger events
- Pass trigger params as env vars to Jobs

**Lifecycle:**
1. **Load deployments** — Read ConfigMap with pipeline repos
2. **Extract triggers** — Clone repo, run `--dry-run`, parse JSON
3. **Start handlers** — For each trigger type, start poller/listener
4. **On trigger** — Create K8s Job with pipeline + params
5. **Job executes** — Job runs `bun run pipeline.ts` → SDK → stages execute

**Location:** `cmd/factory-pipeline-controller/`

**Deployment:** Kubernetes Deployment (1 replica)

**Why separate:** Decouples trigger logic from pipeline execution. Pipelines don't need to know about Jira API, webhooks, etc. Controller handles infrastructure concerns.

## Data Flow

### Stage Execution

```
1. User calls:
   const result = await claude("analyze", { prompt: "prompts/analyze.md" });

2. SDK:
   - Ensure run exists (create if first stage)
   - POST /api/v1/imperative/runs/{run_id}/stages
     Content-Type: application/json
     {
       "name": "analyze",
       "image": "quay.io/hummingbird/agent-claude-code:latest",
       "command": ["claude-code", "--print", "--prompt-file", "/workspace/.prompt.md"],
       "prompt": "prompts/analyze.md",
       "model": "sonnet",
       "credentials": [{ "name": "anthropic", "provider": "anthropic" }],
       "resources": [{ "type": "git", "access": "read-write" }],
       "timeout": "10m",
       "retry": 1
     }
   - Receive stage ID + status
   - Poll GET /api/v1/imperative/runs/{run_id}/stages/{stage_id}
   - Wait for status = "completed"

3. API:
   - Create stage record in PostgreSQL
   - Insert outbox entry { queue: "sf-stage", key: stage_id }
   - Return stage ID

4. Outbox Poller:
   - Read outbox entry
   - Enqueue to sf-stage workqueue
   - Mark sent

5. Sandbox Manager:
   - Dequeue from sf-stage
   - Provision sandbox
   - Clone resources
   - Copy prompt
   - Run: claude-code --print --prompt-file /workspace/.prompt.md
   - Collect /output/output.json
   - Update stage record with output
   - Enqueue to sf-output

6. SDK:
   - Sees status = "completed"
   - Returns { name, output, exitCode, duration }

7. User code:
   console.log(result.output.severity);  // "critical"
```

### Parallel Execution

```typescript
const [test1, test2, test3] = await Promise.all([
  run("unit-tests", { ... }),
  run("integration-tests", { ... }),
  run("e2e-tests", { ... }),
]);
```

SDK makes 3 parallel HTTP requests → 3 stages created → 3 outbox entries → 3 workqueue messages → sandbox-manager dispatches to 3 available sandboxes → SDK polls all 3 → all complete → Promise.all resolves.

### Conditional Execution

```typescript
if (analysis.output.auto_fixable) {
  const patch = await claude("patch", { ... });
}
```

TypeScript `if` — no special framework. Patch stage only created if condition true.

### Data Passing

```typescript
const analysis = await claude("analyze", { prompt: "prompts/analyze.md" });

const patch = await claude("patch", {
  prompt: "prompts/patch.md",
  inputs: { analysis: analysis.output },
});
```

SDK serializes `analysis.output` to JSON → sends in stage creation request → sandbox-manager writes to `/workspace/inputs/analysis/output.json` → agent reads it.

## Failure Handling

### Stage Failure

If agent exits non-zero:
- Sandbox-manager marks stage `status = "failed"`
- SDK sees `status = "failed"` → throws error
- User code catches or pipeline fails

Retry:
```typescript
await claude("flaky-stage", {
  prompt: "prompts/flaky.md",
  retry: 3,  // Retry up to 3 times
});
```

### Outbox Failure

If enqueue to workqueue fails:
- Outbox entry remains `sent = false`
- Outbox poller retries on next tick
- Exponential backoff on consecutive errors
- Guaranteed delivery (at-least-once)

### Sandbox Failure

If sandbox crashes during execution:
- Sandbox-manager marks stage `status = "failed"`
- Workqueue retry mechanism (if configured)
- Or user code catches error and handles

### Verification Gate Failure

If verification gate rejects (e.g., secrets detected):
- Output processor marks output `status = "rejected"`
- Audit event logged
- Stage marked failed
- Pipeline stops

## Scalability

**Horizontal scaling:**
- Sandbox-manager — scale to N replicas, workqueue distributes
- Output-processor — scale to N replicas
- API service — stateless, scale behind load balancer

**Bottlenecks:**
- PostgreSQL — read/write throughput
- Workqueue — message throughput
- Sandbox provisioning — depends on infrastructure (Kubernetes node pool, OpenShell capacity)

**Mitigation:**
- Read replicas for PostgreSQL (SDK polling)
- Workqueue partitioning by priority
- Pre-warmed sandbox pool

## Security

**Sandbox isolation:**
- Network — egress-only, no internet by default
- Filesystem — ephemeral, destroyed after execution
- Credentials — mounted read-only, never persisted

**Git operations:**
- All via git-proxy (sandboxes never see tokens)
- Branch protection enforced
- Signed commits required

**Verification gates:**
- Secrets scan before commit/PR
- Diff size limits (reject 10K+ line changes)
- Custom gates per project (e.g., test coverage)

**Audit trail:**
- All actions logged to `audit_events`
- Stage outputs persisted
- Immutable run records

## Observability

**Metrics:**
- Stage duration (p50, p99)
- Queue depth (`sf-stage`, `sf-output`)
- Success/failure rate
- Sandbox provisioning time
- Verification gate rejection rate

**Logs:**
- Structured JSON (slog)
- Stage execution logs persisted
- Agent stdout/stderr captured

**Traces:**
- OpenTelemetry spans (planned)
- Trace ID through SDK → API → workqueue → sandbox

## Deployment

**Prerequisites:**
- Kubernetes cluster
- PostgreSQL 16+
- Workqueue (Redis/NATS/SQS)
- Git-proxy service

**Components:**
- `factory-api` — Deployment + Service (port 8080)
- `factory-sandbox-manager` — Deployment (replicas: 3+)
- `factory-output-processor` — Deployment (replicas: 2+)

**Configuration:**
- `DATABASE_URL` — PostgreSQL connection string
- `ENQUEUE_ENDPOINT` — Workqueue API endpoint
- `GIT_PROXY_URL` — Git-proxy service URL

## Future Enhancements

**Planned:**
- SSE/WebSocket for real-time stage status (replace polling)
- Sandbox pre-warming pool
- Multi-tenancy (isolated runs per tenant)
- Workflow visualization UI
- Cost tracking per run
- Incremental verification (only changed files)
