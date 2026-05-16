# Factory SDK

Imperative TypeScript SDK for Factory — orchestrate AI agents to build, test, and ship code.

## Installation

```bash
npm install @hummingbird/factory-sdk
# or
bun add @hummingbird/factory-sdk
```

## Quick Start

```typescript
import { claude, judge, run, git, pr } from "@hummingbird/factory-sdk";

const repo = git("read-write");

// Analyze issue
const analysis = await claude("analyze", {
  prompt: "prompts/analyze.md",
  resources: [repo],
});

// Generate fix
const fix = await claude("fix", {
  prompt: "prompts/fix.md",
  model: "opus",
  resources: [repo],
  inputs: { analysis: analysis.output },
});

// Verify fix
const review = await judge("review", {
  prompt: "prompts/review.md",
  resources: [repo],
  inputs: { fix: fix.output },
});

if (review.verdict !== "APPROVE") {
  console.log(`Fix rejected: ${review.reasoning}`);
  process.exit(1);
}

// Run tests in parallel
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

// Create PR
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

## Features

- **Imperative** — Use `if`, `await`, `Promise.all` for control flow
- **Type-safe** — `review.verdict` typed as `"APPROVE" | "VETO" | "UNCERTAIN"`
- **Smart defaults** — `claude()` auto-fills image, command, credentials, model
- **Parallel execution** — `Promise.all` runs stages concurrently
- **Pluggable** — Extend with custom stage types (GPT-4, Gemini, custom agents)

## API

### `claude(name, opts)`

Execute Claude Code stage.

```typescript
const result = await claude("analyze", {
  prompt: "prompts/analyze.md",
  model: "opus",
  resources: [repo],
  timeout: "30m",
});
```

### `judge(name, opts)`

Execute LLM verification with typed verdict.

```typescript
const verdict = await judge("security-review", {
  prompt: "prompts/review.md",
  resources: [repo],
  inputs: { patch: patch.output },
});
// verdict.verdict = "APPROVE" | "VETO" | "UNCERTAIN"
```

### `run(name, opts)`

Execute custom agent.

```typescript
const tests = await run("unit-tests", {
  image: "quay.io/example/test-runner:latest",
  command: ["./run-tests.sh", "unit"],
  resources: [repo],
});
```

### `stage(name, req)`

Low-level builder for custom stage types.

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
    timeout: opts.timeout || "10m",
  });
}
```

### Resources

```typescript
const repo = git("read-write");      // Can commit/push
const upstream = git();               // Read-only (default)
const advisory = http("https://...");
const data = s3("bucket-name", "read-write");
```

### Output Actions

```typescript
await claude("create-pr", {
  prompt: "prompts/pr.md",
  resources: [repo],
  output: pr({
    branch: "factory/auto-fix",
    labels: ["automated"],
    reviewers: ["platform-team"],
    draft: false,
  }),
});
```

## Patterns

### Parallel Execution

```typescript
const [scan1, scan2, scan3] = await Promise.all([
  claude("security-scan", { prompt: "prompts/security.md" }),
  claude("performance-scan", { prompt: "prompts/perf.md" }),
  claude("style-scan", { prompt: "prompts/style.md" }),
]);
```

### Conditional Execution

```typescript
if (analysis.output.severity === "critical") {
  const patch = await claude("patch", { model: "opus", timeout: "60m" });
} else {
  await createIssue(analysis.output);
}
```

### Fan-In Synthesis

```typescript
const synthesis = await claude("synthesize", {
  prompt: "prompts/synthesize.md",
  inputs: {
    security: scan1.output,
    performance: scan2.output,
    style: scan3.output,
  },
});
```

### Multiple Judges

```typescript
const [judge1, judge2] = await Promise.all([
  judge("judge-1", { prompt: "prompts/judge.md", model: "opus" }),
  judge("judge-2", { prompt: "prompts/judge.md", model: "sonnet" }),
]);

const calibration = await judge("calibrate", {
  prompt: "prompts/calibrate.md",
  inputs: { judge1: judge1.output, judge2: judge2.output },
});
```

## Configuration

```typescript
import { setConfig } from "@hummingbird/factory-sdk";

setConfig({
  apiEndpoint: "https://factory-api.example.com",
  pollIntervalMs: 500,
  maxPollIntervalMs: 10000,
});
```

## Environment Variables

```bash
FACTORY_API_ENDPOINT=http://localhost:8080  # API URL
```

## Examples

See [examples/](../../examples/) for complete working pipelines:

- **cve-auto-fix** — CVE patching with LLM judge and parallel tests (65 lines)
- **security-audit** — Multi-judge security scan with calibration (~100 lines)
- **multi-repo-refactor** — Cross-repo API migration (~80 lines)

## Documentation

- [Full SDK Guide](../../SDK.md) — Complete API reference and advanced patterns
- [Architecture](../../ARCHITECTURE.md) — System components and data flow
- [Contributing](../../CONTRIBUTING.md) — Development setup and guidelines

## License

Apache 2.0
