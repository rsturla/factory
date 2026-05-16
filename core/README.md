# Factory

Agentic software factory — orchestrate AI agents to build, test, and ship code.

## What Is This

Factory executes multi-stage pipelines where each stage runs an AI agent (Claude Code, custom tools) in an isolated sandbox. Agents read code, make changes, run tests, create PRs — all automated.

**Use cases:**
- CVE auto-patching
- Cross-repo refactoring
- Security audits with LLM judges
- Data processing with quality gates
- Any workflow needing AI agents + code execution

## Quick Start

### Write a Pipeline

```typescript
// pipeline.ts
import { claude, judge, run, git, pr } from "@hummingbird/factory-sdk";

const repo = git("read-write");

// Step 1: Analyze issue
const analysis = await claude("analyze", {
  prompt: "prompts/analyze.md",
  resources: [repo],
});

// Step 2: Generate fix
const fix = await claude("fix", {
  prompt: "prompts/fix.md",
  model: "opus",
  resources: [repo],
  inputs: { analysis: analysis.output },
  timeout: "30m",
});

// Step 3: Verify fix
const review = await judge("review", {
  prompt: "prompts/review.md",
  resources: [repo],
  inputs: { fix: fix.output },
});

if (review.verdict !== "APPROVE") {
  console.log(`Fix rejected: ${review.reasoning}`);
  process.exit(1);
}

// Step 4: Run tests in parallel
const [unit, integration] = await Promise.all([
  run("unit-tests", {
    image: "quay.io/example/test-runner:latest",
    command: ["./run-tests.sh", "unit"],
    resources: [repo],
  }),
  run("integration-tests", {
    image: "quay.io/example/test-runner:latest",
    command: ["./run-tests.sh", "integration"],
    resources: [repo],
  }),
]);

if (unit.output.failed > 0 || integration.output.failed > 0) {
  console.log("Tests failed");
  process.exit(1);
}

// Step 5: Create PR
await claude("create-pr", {
  prompt: "prompts/pr-description.md",
  resources: [repo],
  inputs: { fix: fix.output, tests: { unit: unit.output, integration: integration.output } },
  output: pr({
    branch: "factory/auto-fix",
    labels: ["automated"],
    reviewers: ["platform-team"],
  }),
});

console.log("Done — PR created");
```

### Run It

```bash
bun run pipeline.ts
```

## Features

**Imperative pipelines** — TypeScript programs, not YAML configs. Use `if`, `await`, `Promise.all` for control flow.

**Type safety** — `review.verdict` typed as `"APPROVE" | "VETO" | "UNCERTAIN"`. Autocomplete in IDE.

**Smart defaults** — `claude()` auto-fills image, command, credentials, model. Override when needed.

**Parallel execution** — `Promise.all` runs stages concurrently. Workqueue dispatches to available sandboxes.

**LLM judges** — Verification stages with typed verdicts. Multiple judges + calibration for bias mitigation.

**Pluggable** — Extend SDK with custom stage types (GPT-4, Gemini, custom agents). See `examples/cve-auto-fix-imperative/README.md`.

**Production-ready** — Workqueue-based execution, PostgreSQL persistence, outbox pattern for reliability, git-proxy for safe operations.

**Triggers** — Deploy pipelines to Kubernetes with Jira, webhook, cron, or GitHub triggers. Controller spawns Jobs automatically.

## Deployment

Pipelines deploy to Kubernetes clusters with triggers defined in code:

```typescript
import { triggers, isDryRun, exitDryRun } from "@hummingbird/factory-sdk";

// Jira trigger: poll every 5m for CVEs needing attention
export const trigger = triggers.jira({
  query: 'project = HUM AND labels = cve-needs-attention',
  poll: '5m',
  params: (issue) => ({
    CVE_ID: issue.fields.customfield_10667?.value,
    ISSUE_KEY: issue.key,
  }),
});

if (isDryRun()) {
  exitDryRun(trigger);
}

// Pipeline logic (runs when triggered)
const analysis = await claude("analyze", { ... });
```

Controller discovers pipelines, extracts triggers, spawns K8s Jobs on events.

**Trigger types:** Jira polls, webhooks, cron schedules, GitHub events, manual invocation.

See [SDK.md#Triggers](./SDK.md#triggers-deployed-pipelines) for details.

## Architecture

```
┌─────────────────────────────────────┐
│  TypeScript Pipeline (orchestrator) │  ← Your code
│  - await claude()                   │
│  - if/Promise.all                   │
└──────────────┬──────────────────────┘
               │ SDK enqueues stages
               ▼
┌─────────────────────────────────────┐
│  sf-stage queue (workqueue)         │
│  factory-sandbox-manager            │  ← Execution layer
│  - Provision sandbox                │
│  - Run agent                        │
│  - Collect output                   │
└──────────────┬──────────────────────┘
               │ Output ready
               ▼
┌─────────────────────────────────────┐
│  sf-output queue (workqueue)        │
│  factory-output-processor           │  ← Output handlers
│  - Verification gates               │
│  - Create PR / file issue / commit  │
└─────────────────────────────────────┘
```

TypeScript orchestrates. Workqueue executes. Clean separation.

See [ARCHITECTURE.md](./ARCHITECTURE.md) for details.

## Documentation

- **[ARCHITECTURE.md](./ARCHITECTURE.md)** — System components, data flow
- **[SDK.md](./SDK.md)** — SDK guide, API reference, advanced patterns
- **[examples/](./examples/)** — Working pipelines (CVE auto-fix, security audit, multi-repo refactor)
- **[CONTRIBUTING.md](./CONTRIBUTING.md)** — Development setup, testing, code structure

## Examples

| Example | Pattern | Lines |
|---------|---------|-------|
| [cve-auto-fix-imperative](./examples/cve-auto-fix-imperative/) | Conditionals, dynamic model selection, parallel tests, LLM judge | 65 |
| [security-audit](./examples/security-audit-imperative/) | Multiple judges, calibration, adversarial review | ~100 |
| [multi-repo-refactor](./examples/multi-repo-refactor-imperative/) | Dynamic resources, cross-repo orchestration | ~80 |

Compare to declarative equivalents (67% reduction in boilerplate).

## Why TypeScript?

Declarative pipeline DSLs hit limits fast:
- Conditionals become template string hacks
- Dynamic stage generation impossible
- Type safety requires code generation
- IDE support is poor

TypeScript gives:
- `if`/`await`/`Promise.all` — language primitives
- Type checking at compile time
- Autocomplete, jump-to-definition, refactoring
- Reusable functions, loops, imports
- Familiar tooling (VS Code, Bun, TypeScript)

We tried YAML. We tried declarative TypeScript. Imperative won.

## Status

Production-ready execution layer. SDK is new (v0.1).

**Works:**
- Stage execution via workqueue
- Sandbox provisioning (Docker, OpenShell)
- Output processing (PR, review, report)
- Git operations via git-proxy
- Verification gates (secrets scan, diff size)
- PostgreSQL persistence, outbox pattern

**New:**
- Imperative SDK (`claude`, `judge`, `run`)
- TypeScript-based orchestration
- API endpoints for SDK

## License

Apache 2.0
