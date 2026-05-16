# Factory SDK Guide

Complete guide to building pipelines with Factory SDK.

## Installation

```bash
npm install @hummingbird/factory-sdk
# or
bun add @hummingbird/factory-sdk
```

## Quick Start

```typescript
import { claude, git, pr } from "@hummingbird/factory-sdk";

const repo = git("read-write");

const analysis = await claude("analyze", {
  prompt: "prompts/analyze.md",
  resources: [repo],
});

console.log(analysis.output);
```

## Core Concepts

### Stages

Stages are units of work. Each stage:
- Runs in isolated sandbox
- Has inputs (previous stage outputs)
- Produces output (JSON)
- Can access resources (git repos, S3, HTTP)

Three stage types:

**1. Claude Code stage** — `claude()`
```typescript
const result = await claude("analyze", {
  prompt: "prompts/analyze.md",
  model: "sonnet",
  resources: [repo],
});
```

**2. LLM judge** — `judge()`
```typescript
const verdict = await judge("security-review", {
  prompt: "prompts/review.md",
  resources: [repo],
  inputs: { patch: patch.output },
});
// verdict.verdict = "APPROVE" | "VETO" | "UNCERTAIN"
```

**3. Custom agent** — `run()`
```typescript
const tests = await run("unit-tests", {
  image: "quay.io/example/test-runner:latest",
  command: ["./run-tests.sh", "unit"],
  resources: [repo],
});
```

### Resources

Resources are mounted into sandbox.

**Git repository:**
```typescript
const repo = git("read-write");  // Can commit/push
const upstream = git();          // Read-only (default)
```

**HTTP resource:**
```typescript
const advisory = http("https://nvd.nist.gov/vuln/detail/CVE-2026-1234");
```

**S3 bucket:**
```typescript
const data = s3("my-bucket", "read-write");
```

### Outputs

Stages produce JSON output collected from `/output/output.json` in sandbox.

**Access output:**
```typescript
const analysis = await claude("analyze", { prompt: "prompts/analyze.md" });
console.log(analysis.output.severity);  // "critical"
console.log(analysis.output.auto_fixable);  // true
```

**Pass to next stage:**
```typescript
const patch = await claude("patch", {
  prompt: "prompts/patch.md",
  inputs: { analysis: analysis.output },
});
```

In sandbox, available at `/workspace/inputs/analysis/output.json`.

### Output Actions

Special outputs trigger actions (PR, commit, report).

**Create PR:**
```typescript
await claude("create-pr", {
  prompt: "prompts/pr-description.md",
  resources: [repo],
  output: pr({
    branch: "factory/auto-fix",
    labels: ["automated"],
    reviewers: ["platform-team"],
    draft: false,
  }),
});
```

## API Reference

### `claude(name, opts)`

Execute Claude Code stage.

**Parameters:**
- `name` (string) — Stage name
- `opts` (ClaudeOptions):
  - `prompt` (string, required) — Prompt file path
  - `model` ("sonnet" | "opus" | "haiku", default: "sonnet") — Claude model
  - `resources` (Resource[], optional) — Resource bindings
  - `env` (Record<string, string>, optional) — Environment variables
  - `timeout` (string, default: "10m") — Max execution time
  - `retry` (number, default: 1) — Max retry attempts
  - `inputs` (Record<string, any>, optional) — Data from previous stages
  - `output` (OutputAction, optional) — Output action (PR, etc.)
  - `changeDetection` ("git" | "explicit" | "auto", default: "auto") — How to detect file changes

**Returns:** `Promise<StageResult>`

**Defaults:**
- Image: `quay.io/hummingbird/agent-claude-code:latest`
- Command: `["claude-code", "--print", "--prompt-file", "/workspace/.prompt.md"]`
- Credentials: `[{ name: "anthropic", provider: "anthropic" }]`
- Model: `"sonnet"`
- Timeout: `"10m"`

**Example:**
```typescript
const analysis = await claude("analyze", {
  prompt: "prompts/analyze.md",
  model: "opus",
  resources: [repo, upstream],
  env: {
    CVE_ID: process.env.CVE_ID,
    SEVERITY: "critical",
  },
  timeout: "30m",
  retry: 3,
});
```

### `judge(name, opts)`

Execute LLM verification stage with typed verdict.

**Parameters:**
- `name` (string) — Stage name
- `opts` (JudgeOptions):
  - `prompt` (string, required) — Prompt file path
  - `model` ("opus" | "sonnet", default: "opus") — Claude model
  - `resources` (Resource[], optional) — Resource bindings
  - `env` (Record<string, string>, optional) — Environment variables
  - `timeout` (string, default: "10m") — Max execution time
  - `retry` (number, default: 1) — Max retry attempts
  - `inputs` (Record<string, any>, optional) — Data from previous stages
  - `output` (OutputAction, optional) — Output action
  - `changeDetection` ("git" | "explicit" | "auto", default: "auto") — How to detect file changes

**Returns:** `Promise<JudgmentResult>`

**JudgmentResult fields:**
- `verdict` ("APPROVE" | "VETO" | "UNCERTAIN") — Judge decision
- `reasoning` (string) — Explanation
- `criteria` (Record<string, CriterionResult>) — Individual criterion results
- `output` (Record<string, any>) — Raw output
- `exitCode`, `duration`, `logs` — Execution metadata

**Example:**
```typescript
const review = await judge("security-review", {
  prompt: "prompts/security-judge.md",
  resources: [repo],
  inputs: { patch: patch.output },
});

if (review.verdict !== "APPROVE") {
  console.log(`Rejected: ${review.reasoning}`);
  process.exit(1);
}
```

### `run(name, opts)`

Execute custom agent.

**Parameters:**
- `name` (string) — Stage name
- `opts` (RunOptions):
  - `image` (string, required) — Container image
  - `command` (string[], required) — Command to execute
  - `resources` (Resource[], optional) — Resource bindings
  - `env` (Record<string, string>, optional) — Environment variables
  - `timeout` (string, default: "10m") — Max execution time
  - `retry` (number, default: 1) — Max retry attempts
  - `inputs` (Record<string, any>, optional) — Data from previous stages
  - `changeDetection` ("git" | "explicit" | "auto", default: "auto") — How to detect file changes

**Returns:** `Promise<StageResult>`

**Example:**
```typescript
const tests = await run("unit-tests", {
  image: "quay.io/example/test-runner:latest",
  command: ["./run-tests.sh", "unit"],
  resources: [repo],
  env: {
    TEST_ENV: "ci",
  },
  timeout: "15m",
});

console.log(`Passed: ${tests.output.passed}`);
console.log(`Failed: ${tests.output.failed}`);
```

### `stage(name, req)`

Low-level stage builder for custom stage types.

**Parameters:**
- `name` (string) — Stage name
- `req` (CreateStageRequest) — Full stage specification

**Returns:** `Promise<StageResult>`

**Use case:** Build custom wrappers around specific tools/models.

**Example:**
```typescript
import { stage } from "@hummingbird/factory-sdk";

export async function gpt4(name, opts) {
  return stage(name, {
    image: "quay.io/example/gpt4-agent:latest",
    command: ["gpt4-cli", "--prompt-file", "/workspace/.prompt.md"],
    prompt: opts.prompt,
    model: opts.model || "gpt-4-turbo",
    credentials: [{ name: "openai", provider: "openai" }],
    resources: opts.resources,
    environment: opts.env,
    timeout: opts.timeout || "10m",
    output: opts.output,
  });
}

// Use it
const result = await gpt4("analyze", {
  prompt: "prompts/analyze.md",
  resources: [repo],
});
```

### `git(access?)`

Create git resource binding.

**Parameters:**
- `access` ("read-only" | "read-write", default: "read-only") — Access mode

**Returns:** `Resource`

**Example:**
```typescript
const repo = git("read-write");  // Can commit/push
const upstream = git();          // Read-only
```

### `http(url)`

Create HTTP resource binding.

**Parameters:**
- `url` (string) — Resource URL

**Returns:** `Resource`

**Example:**
```typescript
const advisory = http("https://nvd.nist.gov/feeds/json/cve/1.1/nvdcve-1.1-2026.json");
```

### `s3(bucket, access?)`

Create S3 bucket binding.

**Parameters:**
- `bucket` (string) — Bucket name
- `access` ("read-only" | "read-write", default: "read-only") — Access mode

**Returns:** `Resource`

**Example:**
```typescript
const input = s3("input-data");              // Read-only
const output = s3("output-data", "read-write");  // Can write
```

### `pr(opts)`

Create PR output action.

**Parameters:**
- `opts` (PROptions):
  - `branch` (string, required) — Branch name
  - `labels` (string[], optional) — PR labels
  - `reviewers` (string[], optional) — Reviewer usernames
  - `draft` (boolean, optional) — Create as draft PR

**Returns:** `OutputAction`

**Example:**
```typescript
await claude("create-pr", {
  prompt: "prompts/pr.md",
  resources: [repo],
  output: pr({
    branch: "factory/cve-fix",
    labels: ["security", "automated"],
    reviewers: ["security-team"],
    draft: false,
  }),
});
```

### `setConfig(config)`

Configure SDK behavior.

**Parameters:**
- `config` (Partial<SDKConfig>):
  - `apiEndpoint` (string) — API URL (default: `http://localhost:8080`)
  - `pollIntervalMs` (number) — Initial poll interval (default: 1000)
  - `maxPollIntervalMs` (number) — Max poll interval (default: 5000)
  - `pollBackoffMultiplier` (number) — Backoff multiplier (default: 1.5)

**Example:**
```typescript
import { setConfig } from "@hummingbird/factory-sdk";

setConfig({
  apiEndpoint: "https://factory-api.example.com",
  pollIntervalMs: 500,
  maxPollIntervalMs: 10000,
});
```

## Advanced Patterns

### Parallel Execution

Use `Promise.all` for concurrent stages:

```typescript
const [unit, integration, e2e] = await Promise.all([
  run("unit-tests", { image: "test-runner", command: ["./test.sh", "unit"] }),
  run("integration-tests", { image: "test-runner", command: ["./test.sh", "integration"] }),
  run("e2e-tests", { image: "test-runner", command: ["./test.sh", "e2e"] }),
]);

const allPassed = [unit, integration, e2e].every(t => t.output.failed === 0);
```

### Conditional Execution

Use `if` for branching:

```typescript
const analysis = await claude("analyze", { prompt: "prompts/analyze.md" });

if (analysis.output.severity === "critical") {
  // Critical path: use Opus, notify immediately
  const patch = await claude("generate-patch", {
    prompt: "prompts/patch.md",
    model: "opus",
    timeout: "60m",
  });
  
  await notifySlack("#security", "Critical CVE patched");
} else {
  // Normal path: use Sonnet, file issue
  await createIssue(analysis.output);
}
```

### Dynamic Stage Generation

Use loops for variable workload:

```typescript
const repos = JSON.parse(process.env.REPOS || "[]");

const analyses = await Promise.all(
  repos.map(repo =>
    claude(`analyze-${repo}`, {
      prompt: "prompts/analyze.md",
      resources: [git("read-write")],
      env: { REPO: repo },
    })
  )
);

console.log(`Analyzed ${analyses.length} repos`);
```

### Fan-In Synthesis

Combine multiple stage outputs:

```typescript
const [security, performance, style] = await Promise.all([
  claude("security-scan", { prompt: "prompts/security.md" }),
  claude("performance-scan", { prompt: "prompts/perf.md" }),
  claude("style-scan", { prompt: "prompts/style.md" }),
]);

const synthesis = await claude("synthesize", {
  prompt: "prompts/synthesize.md",
  inputs: {
    security: security.output,
    performance: performance.output,
    style: style.output,
  },
});

console.log(synthesis.output.overall_score);
```

### Multiple LLM Judges

Bias mitigation via multiple independent reviews:

```typescript
const [judge1, judge2, judge3] = await Promise.all([
  judge("judge-1", {
    prompt: "prompts/judge.md",
    model: "opus",
    env: { PROMPT_VARIATION: "0" },
  }),
  judge("judge-2", {
    prompt: "prompts/judge.md",
    model: "sonnet",
    env: { PROMPT_VARIATION: "1" },
  }),
  judge("judge-3", {
    prompt: "prompts/judge.md",
    model: "opus",
    env: { PROMPT_VARIATION: "2" },
  }),
]);

// Calibration stage
const calibration = await judge("calibrate", {
  prompt: "prompts/calibrate.md",
  inputs: {
    judge1: judge1.output,
    judge2: judge2.output,
    judge3: judge3.output,
  },
});

if (calibration.verdict === "APPROVE") {
  console.log("Consensus reached — APPROVED");
} else if (calibration.output.consensus === "split") {
  console.log("Judges disagree — manual review required");
}
```

### Error Handling

```typescript
try {
  const patch = await claude("generate-patch", {
    prompt: "prompts/patch.md",
    retry: 3,
  });
} catch (err) {
  console.error(`Patch generation failed: ${err.message}`);
  
  // Fallback: create issue instead
  await claude("create-issue", {
    prompt: "prompts/issue.md",
    inputs: { error: err.message },
  });
}
```

### Retry with Backoff

```typescript
const unstableStage = await claude("flaky-operation", {
  prompt: "prompts/flaky.md",
  retry: 5,  // Retry up to 5 times
  timeout: "5m",
});
```

Sandbox-manager handles retries with exponential backoff.

### Timeout Tuning

```typescript
// Quick analysis
const quickScan = await claude("quick-scan", {
  prompt: "prompts/quick.md",
  timeout: "2m",
});

// Slow refactor
const refactor = await claude("refactor", {
  prompt: "prompts/refactor.md",
  timeout: "120m",  // 2 hours
});
```

### Change Detection

Control how the system detects file changes produced by agents.

**Three modes:**

| Mode | Behavior | Use When |
|------|----------|----------|
| `"git"` | Tracks git commits, extracts changed files via `git diff` | Code changes in git repos |
| `"explicit"` | Agent manually copies files to `/output/changes/` | Non-git workspaces, selective outputs |
| `"auto"` | Try git-based first, fall back to explicit | General purpose (default) |

**Git-based detection:**

```typescript
const refactor = await claude("refactor", {
  prompt: "prompts/refactor.md",
  changeDetection: "git",
  resources: [git("read-write")],
});
```

How it works:
1. System captures `git rev-parse HEAD` before agent starts
2. Agent modifies files, commits changes to git
3. After agent completes: `git diff --name-only {initial}..HEAD`
4. Changed files extracted, packaged as tar.gz, uploaded to artifact storage
5. Output includes: `_changed_files`, `_initial_commit`, `_change_detection: "git"`

**Benefits:**
- No manual file copying in prompts
- Automatic detection of all modified files
- Git history preserved (can `git show`, `git log` in sandbox)
- Smaller artifacts (only changed files)

**Explicit detection:**

```typescript
const analysis = await claude("analyze", {
  prompt: "prompts/analyze.md",
  changeDetection: "explicit",
});
```

Prompt must include:
```markdown
Copy generated files to `/output/changes/`:

mkdir -p /output/changes
cp path/to/modified/file.py /output/changes/
```

System tars `/output/changes/`, uploads to artifact storage.

**Auto mode (default):**

```typescript
const stage = await claude("fix", {
  prompt: "prompts/fix.md",
  // changeDetection: "auto" is implicit
  resources: [git("read-write")],
});
```

Tries git-based first (if `/workspace` is git repo), falls back to explicit if git detection fails.

**When to use each:**
- **git**: Code changes, refactors, patches (most common)
- **explicit**: Generated assets (images, binaries), non-git outputs
- **auto**: Unknown workspace type, mixed pipelines

## Prompts

Prompts are markdown files describing agent's task.

**Location:** Relative to pipeline (e.g., `prompts/analyze.md`)

**Example:** `prompts/analyze.md`
```markdown
# Analyze CVE

You have access to:
- Target repository at `/workspace/resources/repo`
- CVE advisory at `/workspace/resources/advisory`

Task:
1. Read CVE description
2. Search codebase for vulnerable code
3. Assess fix complexity

Output JSON to `/output/output.json`:
{
  "severity": "critical" | "high" | "medium" | "low",
  "affected_files": ["path/to/file.c"],
  "auto_fixable": true | false,
  "complexity": "trivial" | "moderate" | "complex",
  "reasoning": "explanation"
}
```

**Accessing inputs:**

Inputs mounted at `/workspace/inputs/{name}/output.json`.

```markdown
# Generate Patch

Analysis from previous stage available at:
`/workspace/inputs/analysis/output.json`

Read the analysis, then generate a patch.
```

**In prompt:**
```markdown
Read `/workspace/inputs/analysis/output.json` for context.
```

## Testing Pipelines

### Local Testing

```bash
# Set API endpoint
export FACTORY_API_ENDPOINT=http://localhost:8080

# Run pipeline
bun run pipeline.ts
```

### Mock Stages (Development)

```typescript
const MOCK = process.env.MOCK === "true";

const analysis = MOCK
  ? { output: { severity: "critical", auto_fixable: true } }
  : await claude("analyze", { prompt: "prompts/analyze.md" });
```

### Unit Testing SDK Logic

```typescript
import { describe, test, expect } from "bun:test";

describe("pipeline", () => {
  test("calculates risk score", () => {
    const findings = [
      { severity: "critical" },
      { severity: "high" },
      { severity: "medium" },
    ];
    
    const score = calculateRiskScore(findings);
    expect(score).toBeGreaterThan(80);
  });
});
```

## Triggers (Deployed Pipelines)

Triggers define when pipelines execute in Kubernetes deployments.

### Trigger Types

**Jira trigger** — Poll Jira API for matching issues:
```typescript
import { triggers, isDryRun, exitDryRun } from "@hummingbird/factory-sdk";

export const trigger = triggers.jira({
  query: 'project = HUM AND labels = cve-needs-attention AND status != Closed',
  poll: '5m',
  params: (issue) => ({
    CVE_ID: issue.fields.customfield_10667?.value,
    SEVERITY: issue.fields.priority.name.toLowerCase(),
    ISSUE_KEY: issue.key,
  }),
});

// Exit dry-run for trigger extraction
if (isDryRun()) {
  exitDryRun(trigger);
}

// Pipeline logic uses env vars from params
const cveId = process.env.CVE_ID;
```

**Schedule trigger** — Cron-based execution:
```typescript
export const trigger = triggers.schedule({
  cron: '0 */6 * * *',  // Every 6 hours
  params: () => ({
    RUN_ID: Date.now().toString(),
  }),
});
```

**GitHub trigger** — GitHub webhook events:
```typescript
export const trigger = triggers.github({
  event: 'pull_request',
  filter: (payload) => payload.action === 'opened',
  params: (payload) => ({
    PR_NUMBER: payload.pull_request.number.toString(),
    REPO: payload.repository.full_name,
    SHA: payload.pull_request.head.sha,
  }),
});
```

**Webhook trigger** — Generic HTTP webhook:
```typescript
export const trigger = triggers.webhook({
  path: '/webhook/custom',
  secret: 'webhook-secret',
  params: (payload) => ({
    EVENT_ID: payload.id,
    DATA: JSON.stringify(payload.data),
  }),
});
```

**Manual trigger** — API/CLI invocation:
```typescript
export const trigger = triggers.manual();
```

### Multiple Triggers

```typescript
export const pipelineTriggers = [
  triggers.schedule({ cron: '0 */6 * * *' }),
  triggers.jira({ query: 'project = HUM AND labels = urgent', poll: '1m' }),
  triggers.manual(),
];

if (isDryRun()) {
  exitDryRun(pipelineTriggers);
}
```

### Deployment Workflow

**1. Define trigger in pipeline.ts:**
```typescript
export const trigger = triggers.jira({
  query: 'labels = cve-needs-attention',
  poll: '5m',
});

if (isDryRun()) {
  exitDryRun(trigger);
}

// Pipeline logic...
```

**2. Push to git repo:**
```bash
git add pipeline.ts
git commit -m "Add CVE auto-fix pipeline"
git push
```

**3. Register in ConfigMap:**
```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: pipeline-deployments
data:
  deployments.json: |
    [
      {
        "name": "cve-auto-fix",
        "repo_url": "https://github.com/org/pipelines",
        "branch": "main",
        "path": "cve-auto-fix"
      }
    ]
```

**4. Controller extracts triggers:**
```bash
# Controller runs on cluster
bun run pipeline.ts --dry-run
# Outputs: [{"type": "jira", "query": "...", ...}]
```

**5. Controller spawns Jobs on trigger:**
```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: cve-auto-fix-1726534890
spec:
  template:
    spec:
      containers:
      - name: pipeline
        image: oven/bun:latest
        command: ["bun", "run", "pipeline.ts"]
        env:
        - name: CVE_ID
          value: "CVE-2026-1234"
```

### Testing Triggers Locally

**Dry-run mode:**
```bash
bun run pipeline.ts --dry-run
# Outputs trigger JSON, doesn't execute pipeline
```

**Normal run:**
```bash
export CVE_ID=CVE-2026-1234
bun run pipeline.ts
# Executes pipeline with params
```

## Deployment

### Environment Variables

```bash
FACTORY_API_ENDPOINT=https://factory-api.example.com
CVE_ID=CVE-2026-1234
SEVERITY=critical
```

### CI Integration

**GitHub Actions:**
```yaml
name: Run Factory Pipeline
on:
  workflow_dispatch:
    inputs:
      cve_id:
        required: true

jobs:
  pipeline:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: oven-sh/setup-bun@v1
      - run: bun install
      - run: bun run .factory/pipeline.ts
        env:
          FACTORY_API_ENDPOINT: ${{ secrets.FACTORY_API_ENDPOINT }}
          CVE_ID: ${{ inputs.cve_id }}
```

### Scheduled Runs

```yaml
on:
  schedule:
    - cron: "0 */6 * * *"  # Every 6 hours
```

## Best Practices

**1. Keep prompts focused**
```typescript
// Good: specific task
await claude("scan-secrets", { prompt: "prompts/scan-secrets.md" });

// Bad: monolithic
await claude("do-everything", { prompt: "prompts/do-everything.md" });
```

**2. Use typed outputs**
```typescript
// Prompt specifies schema
const analysis = await claude("analyze", { prompt: "prompts/analyze.md" });

// Access with confidence
if (analysis.output.auto_fixable) {
  // TypeScript knows auto_fixable exists
}
```

**3. Fail fast**
```typescript
if (review.verdict !== "APPROVE") {
  console.log(`Review failed: ${review.reasoning}`);
  process.exit(1);  // Don't continue
}
```

**4. Parallelize aggressively**
```typescript
// Run all scans concurrently
const scans = await Promise.all([
  claude("secrets-scan", { ... }),
  claude("injection-scan", { ... }),
  claude("auth-scan", { ... }),
]);
```

**5. Use judges for verification**
```typescript
// Don't trust output blindly
const patch = await claude("generate-patch", { ... });

const review = await judge("security-review", {
  prompt: "prompts/review.md",
  inputs: { patch: patch.output },
});

if (review.verdict !== "APPROVE") {
  throw new Error("Patch rejected");
}
```

**6. Set appropriate timeouts**
```typescript
// Quick scan: 2 minutes
await claude("quick-scan", { timeout: "2m" });

// Complex refactor: 1 hour
await claude("refactor", { timeout: "60m" });
```

**7. Log progress**
```typescript
console.log("Step 1: Analyzing...");
const analysis = await claude("analyze", { ... });

console.log(`Step 2: Severity ${analysis.output.severity}`);
const patch = await claude("patch", { ... });

console.log("Step 3: Running tests...");
```

## API Reference

### HTTP Communication

SDK talks to Factory API via HTTP. Understanding payloads helps debug, extend SDK, or build custom clients.

### Endpoints

**Create run:**
```
POST /api/v1/imperative/runs
Content-Type: application/json

{}
```

Response:
```json
{
  "id": "01HQXYZ123",
  "status": "pending",
  "created_at": "2026-05-15T10:00:00Z",
  "updated_at": "2026-05-15T10:00:00Z"
}
```

**Create stage:**
```
POST /api/v1/imperative/runs/{run_id}/stages
Content-Type: application/json

{
  "name": "analyze",
  "image": "quay.io/hummingbird/agent-claude-code:latest",
  "command": ["claude-code", "--print", "--prompt-file", "/workspace/.prompt.md"],
  "prompt": "prompts/analyze.md",
  "model": "sonnet",
  "credentials": [
    { "name": "anthropic", "provider": "anthropic" }
  ],
  "resources": [
    { "type": "git", "access": "read-write" }
  ],
  "environment": {
    "DEBUG": "1"
  },
  "timeout": "10m",
  "retry": 1,
  "inputs": {
    "previous_stage": { "key": "value" }
  },
  "output": {
    "type": "pr",
    "branch": "factory/auto-fix",
    "labels": ["automated"],
    "reviewers": ["team"]
  }
}
```

Response:
```json
{
  "id": "01HQXYZ456",
  "run_id": "01HQXYZ123",
  "name": "analyze",
  "status": "pending",
  "created_at": "2026-05-15T10:00:01Z",
  "updated_at": "2026-05-15T10:00:01Z"
}
```

**Get stage status:**
```
GET /api/v1/imperative/runs/{run_id}/stages/{stage_id}
```

Response (completed):
```json
{
  "id": "01HQXYZ456",
  "run_id": "01HQXYZ123",
  "name": "analyze",
  "status": "completed",
  "output": {
    "severity": "critical",
    "auto_fixable": true
  },
  "exit_code": 0,
  "duration": 45.3,
  "logs": "Agent stdout/stderr...",
  "created_at": "2026-05-15T10:00:01Z",
  "updated_at": "2026-05-15T10:00:46Z"
}
```

### Request Types

**CreateStageRequest:**
```typescript
{
  name: string;                      // Stage name
  image: string;                     // Container image
  command: string[];                 // Command to run
  prompt?: string;                   // Prompt file path (optional)
  model?: string;                    // LLM model (optional)
  credentials?: CredentialBinding[]; // Auth credentials
  resources?: Resource[];            // Git/HTTP/S3 resources
  environment?: Record<string, string>; // Env vars
  timeout?: string;                  // e.g. "10m", "1h"
  retry?: number;                    // Max attempts (default: 1)
  inputs?: Record<string, any>;      // Data from previous stages
  output?: OutputAction;             // Output action (PR, review, etc.)
}
```

**CredentialBinding:**
```typescript
{
  name: string;      // e.g. "anthropic"
  provider: string;  // e.g. "anthropic", "github"
}
```

**Resource:**
```typescript
{
  type: "git" | "http" | "s3";
  access?: "read-only" | "read-write";
  url?: string;        // For http
  bucket?: string;     // For s3
  ref?: string;        // Git ref
  description?: string;
}
```

**OutputAction:**
```typescript
{
  type: "pr" | "review" | "report" | "patch" | "changeset";
  branch?: string;     // PR branch
  labels?: string[];   // PR labels
  reviewers?: string[]; // PR reviewers
  draft?: boolean;     // Draft PR
}
```

### Response Types

**StageRecord:**
```typescript
{
  id: string;
  run_id: string;
  name: string;
  status: "pending" | "running" | "completed" | "failed";
  output?: Record<string, any>;  // Agent's /output/output.json
  exit_code?: number;
  duration?: number;             // Seconds
  logs?: string;                 // stdout/stderr
  created_at: string;
  updated_at: string;
}
```

**RunRecord:**
```typescript
{
  id: string;
  status: "pending" | "running" | "completed" | "failed";
  created_at: string;
  updated_at: string;
}
```

### SDK Implementation

How `claude()` builds request:

```typescript
// User calls:
await claude("analyze", {
  prompt: "prompts/analyze.md",
  model: "sonnet",
  resources: [git("read-write")],
  env: { DEBUG: "1" },
  timeout: "15m",
});

// SDK sends:
POST /api/v1/imperative/runs/{run_id}/stages
{
  "name": "analyze",
  "image": "quay.io/hummingbird/agent-claude-code:latest",
  "command": ["claude-code", "--print", "--prompt-file", "/workspace/.prompt.md"],
  "prompt": "prompts/analyze.md",
  "model": "sonnet",
  "credentials": [{ "name": "anthropic", "provider": "anthropic" }],
  "resources": [{ "type": "git", "access": "read-write" }],
  "environment": { "DEBUG": "1" },
  "timeout": "15m",
  "retry": 1
}

// SDK polls:
GET /api/v1/imperative/runs/{run_id}/stages/{stage_id}

// Until status = "completed", then returns:
{
  name: "analyze",
  output: { /* agent's output.json */ },
  exitCode: 0,
  duration: 45.3,
  logs: "..."
}
```

### Custom Clients

Build client in other languages:

1. POST to create run → get run ID
2. POST stages with run ID → get stage IDs
3. Poll stage endpoints until status = "completed" or "failed"
4. Parse output field for results

All communication is JSON over HTTP. No SDK required — any HTTP client works.

## Output Storage

All stage outputs stored as artifacts for consistency and durability.

### How It Works

**Agents write output:**
```
/output/output.json           # Metadata (PR title, description, etc.)
/output/changes/src/main.go   # Changed files
/output/changes/src/auth.go
```

**System automatically:**
1. Packages `/output/changes/` as tar.gz
2. Uploads to artifact storage (S3/MinIO/local filesystem)
3. Stores URL in PostgreSQL: `{"_artifact_url": "s3://..."}`
4. Output-processor downloads when processing

**Benefits:**
- **Consistent** — All outputs stored same way
- **Scalable** — No size limits (handles 10K+ line refactors)
- **Durable** — Artifacts persist after pipeline (audit trail)
- **Simple** — No threshold logic, one code path

### Verification Limits

Verification gates enforce safety limits on all outputs:

| Limit | Value | Reason |
|-------|-------|--------|
| Total lines changed | 5000 | Prevents massive unreviewed changes |
| Files modified | 100 | Keeps PRs reviewable |
| File size | 1MB per file | Binary/generated file protection |

**Large refactors:**
- Split into multiple stages (each <5000 lines)
- Or configure custom verification gates per project

**Note:** Artifact storage has no size limits, but verification gates still apply. Gates run after download, before PR creation.

### Example: Large Implementation

```typescript
// Agent generates 10,000 line refactor across 200 files

// This gets rejected by verification gates:
await claude("massive-refactor", {
  prompt: "prompts/refactor-all.md",
  output: pr({ branch: "refactor" }),
});
// Error: too many lines changed (10,000 > 5000)

// Solution: Split into stages
const phase1 = await claude("refactor-phase-1", {
  prompt: "prompts/refactor-auth.md",  // 3000 lines
  output: pr({ branch: "refactor-phase-1" }),
});

const phase2 = await claude("refactor-phase-2", {
  prompt: "prompts/refactor-api.md",   // 4000 lines
  output: pr({ branch: "refactor-phase-2" }),
});

const phase3 = await claude("refactor-phase-3", {
  prompt: "prompts/refactor-ui.md",    // 3000 lines
  output: pr({ branch: "refactor-phase-3" }),
});
// Each phase passes verification (under 5000 lines)
```

### Artifact Storage Details

**What gets uploaded:**
- All `/output/changes/` directory trees
- Compressed as tar.gz
- Preserves paths and permissions

**Artifact URL formats:**
```
# S3/MinIO
s3://factory-artifacts/artifacts/{stage-id}/output.tar.gz

# Local filesystem
file:///tmp/factory-artifacts/artifacts/{stage-id}/output.tar.gz
```

**Storage duration:**
- Artifacts persist after pipeline completion
- Enables audit trail and debugging
- Retention policy configurable (default: 30 days)

**Size limits:**
- No hard limit on artifact storage
- Practical limit: ~10GB per stage (S3 multipart upload)
- Verification gates limit changes (5000 lines), not artifact size

### Configuration

Artifact storage configured via environment variables:

**Local development (default):**
```bash
ARTIFACT_BACKEND=local
ARTIFACT_LOCAL_DIR=/tmp/factory-artifacts
```

**S3 production:**
```bash
ARTIFACT_BACKEND=s3
ARTIFACT_BUCKET=factory-artifacts
ARTIFACT_ENDPOINT=https://s3.amazonaws.com
ARTIFACT_REGION=us-east-1
ARTIFACT_ACCESS_KEY=AKIAIOSFODNN7EXAMPLE
ARTIFACT_SECRET_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY
```

**MinIO self-hosted:**
```bash
ARTIFACT_BACKEND=minio
ARTIFACT_ENDPOINT=https://minio.internal:9000
ARTIFACT_BUCKET=factory-artifacts
ARTIFACT_ACCESS_KEY=minioadmin
ARTIFACT_SECRET_KEY=minioadmin
```

**Local development — MinIO in Docker:**
```bash
docker run -p 9000:9000 -p 9001:9001 \
  -e MINIO_ROOT_USER=minioadmin \
  -e MINIO_ROOT_PASSWORD=minioadmin \
  minio/minio server /data --console-address ":9001"
```

## Troubleshooting

**Stage fails immediately:**
- Check prompt file exists
- Verify resource bindings correct
- Check timeout not too short

**Stage hangs:**
- Agent might be waiting for input
- Check agent command correct
- Increase timeout

**Output empty:**
- Agent must write `/output/output.json`
- Check prompt instructs agent correctly
- Verify JSON syntax valid

**Authentication errors:**
- Credentials not configured in sandbox-manager
- Check credential provider (anthropic, github, etc.)

**API connection errors:**
- Verify `FACTORY_API_ENDPOINT` set
- Check network connectivity
- API service might be down

## Examples

See [examples/](./examples/) for complete working pipelines:

- [cve-auto-fix-imperative](./examples/cve-auto-fix-imperative/) — CVE patching workflow
- [security-audit-imperative](./examples/security-audit-imperative/) — Multi-judge security scan
- [multi-repo-refactor-imperative](./examples/multi-repo-refactor-imperative/) — Cross-repo changes

Each includes:
- Complete pipeline code
- Prompt files
- README with explanation
