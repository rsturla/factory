# Factory Pipeline Examples

Working examples showing Factory SDK patterns.

## Quick Comparison

| Example | Lines | Key Features |
|---------|-------|--------------|
| [cve-auto-fix-imperative](#cve-auto-fix) | 65 | Conditionals, parallel tests, LLM judge, dynamic model selection |
| [security-audit-imperative](#security-audit) | ~100 | Multiple judges, calibration, adversarial review, fan-in synthesis |
| [multi-repo-refactor-imperative](#multi-repo-refactor) | ~80 | Dynamic resources, cross-repo validation, dependency ordering |
| [data-pipeline-imperative](#data-pipeline) | ~70 | Dynamic parallelism, deterministic aggregation, quality gates |

All examples use **imperative SDK** (TypeScript `await`/`if`/`Promise.all`).

Compare to declarative versions in `typescript-pipelines/` (67% fewer lines).

## CVE Auto-Fix

**Path:** [cve-auto-fix-imperative/](./cve-auto-fix-imperative/)

**Use case:** Automatically patch security vulnerabilities.

**Workflow:**
1. Analyze CVE impact and complexity
2. Generate patch (Opus for critical, Sonnet otherwise)
3. Security review by LLM judge
4. Run 3 test suites in parallel
5. Create PR if tests pass

**Key features:**

**Conditional execution:**
```typescript
if (!analysis.output.auto_fixable) {
  console.log("Not auto-fixable, exiting");
  process.exit(0);
}
```

**Dynamic model selection:**
```typescript
const patch = await claude("generate-patch", {
  prompt: "prompts/patch.md",
  model: analysis.output.severity === "critical" ? "opus" : "sonnet",
  // ...
});
```

**LLM judge:**
```typescript
const review = await judge("security-review", {
  prompt: "prompts/judge.md",
  resources: [repo],
  inputs: { patch: patch.output },
});

if (review.verdict !== "APPROVE") {
  console.log(`Patch rejected: ${review.reasoning}`);
  process.exit(1);
}
```

**Parallel tests:**
```typescript
const [unit, integration, regression] = await Promise.all([
  run("unit-tests", { ... }),
  run("integration-tests", { ... }),
  run("regression-tests", { ... }),
]);
```

**PR output:**
```typescript
await claude("create-pr", {
  prompt: "prompts/pr.md",
  resources: [repo],
  output: pr({
    branch: `factory/cve-${process.env.CVE_ID}`,
    labels: ["security", "automated-fix"],
    reviewers: analysis.output.severity === "critical" 
      ? ["security-team"] 
      : ["maintainers"],
  }),
});
```

**Run:**
```bash
export CVE_ID=CVE-2026-1234
export FACTORY_API_ENDPOINT=http://localhost:8080
bun run examples/cve-auto-fix-imperative/pipeline.ts
```

## Security Audit

**Path:** [security-audit-imperative/](./security-audit-imperative/) (to be created)

**Use case:** Comprehensive security scan with multiple LLM judges.

**Workflow:**
1. Run 7 specialized scans in parallel
   - Secrets scan
   - Injection vulnerabilities
   - Auth/session issues
   - Crypto weaknesses
   - Dependency vulnerabilities
   - Misconfigurations
   - Data handling
2. Aggregate findings
3. Multiple independent judges review critical findings
4. Calibration stage reconciles disagreements
5. Generate fixes
6. Adversarial review (red team testing)
7. Create PR if approved

**Key features:**

**Parallel scans:**
```typescript
const scanners = [
  { name: "secrets", prompt: "prompts/scan-secrets.md", model: "opus" },
  { name: "injection", prompt: "prompts/scan-injection.md", model: "sonnet" },
  // ... 5 more
];

const scans = await Promise.all(
  scanners.map(s =>
    claude(`scan-${s.name}`, {
      prompt: s.prompt,
      model: s.model,
      resources: [repo, policies],
      timeout: "30m",
    })
  )
);
```

**Fan-in synthesis:**
```typescript
const scanOutputs = Object.fromEntries(
  scans.map((s, i) => [scanners[i].name, s.output])
);

const aggregate = await claude("aggregate", {
  prompt: "prompts/aggregate.md",
  model: "opus",
  inputs: scanOutputs,
});
```

**Multiple judges (bias mitigation):**
```typescript
const judgments = await Promise.all([
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
]);
```

**Calibration:**
```typescript
const calibration = await judge("calibrate", {
  prompt: "prompts/calibrate.md",
  inputs: {
    judge1: judgments[0].output,
    judge2: judgments[1].output,
  },
});

if (calibration.verdict !== "VETO") {
  console.log("No critical action needed");
  process.exit(0);
}
```

**Adversarial review:**
```typescript
const adversarial = await judge("adversarial-review", {
  prompt: "prompts/adversarial.md",
  resources: [repo],
  inputs: { fixes: fixes.output },
  env: { ROLE: "attacker" },  // Red team mindset
});
```

**Pattern:** Multiple independent judges + calibration reduces LLM bias. Adversarial review catches regressions.

## Multi-Repo Refactor

**Path:** [multi-repo-refactor-imperative/](./multi-repo-refactor-imperative/) (to be created)

**Use case:** Update API across 15 microservices.

**Workflow:**
1. Analyze all repos in parallel
2. Synthesize migration plan (dependency order)
3. Refactor repos (in dependency order)
4. Cross-repo validation
5. Integration tests
6. Create PR per repo

**Key features:**

**Dynamic resources:**
```typescript
const repos = JSON.parse(process.env.REPOS || "[]");

const repoResources = repos.map(name => ({
  name,
  resource: git("read-write"),
}));
```

**Parallel analysis:**
```typescript
const analyses = await Promise.all(
  repos.map(repo =>
    claude(`analyze-${repo}`, {
      prompt: "prompts/analyze-api-usage.md",
      resources: [repoResources.find(r => r.name === repo).resource],
      env: {
        REPO_NAME: repo,
        TARGET_API: process.env.TARGET_API,
      },
    })
  )
);
```

**Dependency-ordered execution:**
```typescript
const plan = await claude("migration-plan", {
  prompt: "prompts/synthesize-plan.md",
  model: "opus",
  inputs: Object.fromEntries(
    analyses.map((a, i) => [`analyze-${repos[i]}`, a.output])
  ),
});

// plan.output.migration_order = ["repo-a", "repo-b", "repo-c"]
```

**Cross-repo validation:**
```typescript
const validation = await claude("validate-consistency", {
  prompt: "prompts/validate-consistency.md",
  model: "opus",
  resources: repoResources.map(r => r.resource),
  inputs: Object.fromEntries(
    refactorResults.map((r, i) => [`refactor-${repos[i]}`, r.output])
  ),
});

if (validation.output.verdict !== "APPROVE") {
  console.log("Inconsistencies detected");
  process.exit(1);
}
```

**Per-repo PRs:**
```typescript
await Promise.all(
  repos.map(repo =>
    claude(`pr-${repo}`, {
      prompt: "prompts/pr-description.md",
      resources: [repoResources.find(r => r.name === repo).resource],
      output: pr({
        branch: `factory/migrate-${process.env.NEW_VERSION}`,
        labels: ["refactor", "api-migration"],
        reviewers: ["platform-team"],
      }),
    })
  )
);
```

**Pattern:** Scale to N repos via params, not hardcoded stages.

## Data Pipeline

**Path:** [data-pipeline-imperative/](./data-pipeline-imperative/) (to be created)

**Use case:** Process 1000s of data files with quality gates.

**Workflow:**
1. Discover input files (S3 bucket)
2. Create batches (100 files per batch)
3. Process batches in parallel (up to 20 concurrent)
4. Aggregate results (deterministic)
5. Quality analysis with LLM
6. Commit results if quality score ≥ 0.95

**Key features:**

**Dynamic parallelism:**
```typescript
const discovery = await run("discover-files", {
  image: "quay.io/example/data-tools:latest",
  command: ["python", "scripts/discover.py"],
  resources: [inputBucket],
});

const batchCount = discovery.output.estimated_batches;

const batches = await Promise.all(
  Array.from({ length: Math.min(batchCount, 20) }, (_, i) =>
    claude(`process-batch-${i}`, {
      prompt: "prompts/process-batch.md",
      model: "sonnet",
      resources: [inputBucket, outputBucket],
      env: {
        BATCH_ID: i.toString(),
        BATCH_FILES: JSON.stringify(discovery.output.batches[i].files),
      },
      timeout: "60m",
    })
  )
);
```

**Deterministic aggregation:**
```typescript
const aggregate = await run("aggregate-results", {
  image: "quay.io/example/data-tools:latest",
  command: ["python", "scripts/aggregate.py"],
  inputs: Object.fromEntries(
    batches.map((b, i) => [`batch-${i}`, b.output])
  ),
});
```

**Quality gate:**
```typescript
const quality = await claude("quality-analysis", {
  prompt: "prompts/quality-analysis.md",
  model: "opus",
  resources: [outputBucket],
  inputs: { results: aggregate.output },
});

if (quality.output.quality_score < 0.95) {
  console.log("Quality too low, not committing");
  process.exit(1);
}
```

**Pattern:** Handle variable workload without changing pipeline definition.

## Declarative Examples (Legacy)

**Path:** [typescript-pipelines/](./typescript-pipelines/)

Declarative TypeScript pipelines (pre-imperative SDK).

**Why deprecated:**
- 67% more boilerplate
- No `if`/`await`/`Promise.all`
- Harder to understand flow
- Less type safety

**Examples:**
- `cve-auto-fix/` — 189 lines (vs 65 imperative)
- `security-audit/` — 318 lines (vs ~100 imperative)
- `multi-repo-refactor/` — 207 lines (vs ~80 imperative)
- `data-pipeline/` — 244 lines (vs ~70 imperative)

See `typescript-pipelines/README.md` for detailed comparison.

## Running Examples

**Prerequisites:**
```bash
# Install SDK
npm install @hummingbird/factory-sdk

# Or use Bun
bun add @hummingbird/factory-sdk

# Set API endpoint
export FACTORY_API_ENDPOINT=http://localhost:8080
```

**Run example:**
```bash
bun run examples/cve-auto-fix-imperative/pipeline.ts
```

**With parameters:**
```bash
export CVE_ID=CVE-2026-1234
export SEVERITY=critical
bun run examples/cve-auto-fix-imperative/pipeline.ts
```

## Writing Your Own

**1. Create directory:**
```bash
mkdir examples/my-pipeline
cd examples/my-pipeline
```

**2. Write pipeline:**
```typescript
// pipeline.ts
import { claude, judge, run, git, pr } from "@hummingbird/factory-sdk";

const repo = git("read-write");

const analysis = await claude("analyze", {
  prompt: "prompts/analyze.md",
  resources: [repo],
});

// Add more stages...
```

**3. Create prompts:**
```bash
mkdir prompts
cat > prompts/analyze.md << 'EOF'
# Analyze Repository

Task: Analyze codebase for issues.

Output JSON to `/output/output.json`:
{
  "issues": [...],
  "severity": "high"
}
EOF
```

**4. Run:**
```bash
bun run pipeline.ts
```

**5. Iterate:**
- Add stages
- Refine prompts
- Test outputs
- Add verification

## Best Practices

**1. Start simple:**
```typescript
// Single stage first
const result = await claude("analyze", { prompt: "prompts/analyze.md" });
console.log(result.output);
```

**2. Add verification:**
```typescript
const review = await judge("review", {
  prompt: "prompts/review.md",
  inputs: { analysis: result.output },
});

if (review.verdict !== "APPROVE") {
  process.exit(1);
}
```

**3. Parallelize:**
```typescript
const [scan1, scan2, scan3] = await Promise.all([
  claude("scan-1", { ... }),
  claude("scan-2", { ... }),
  claude("scan-3", { ... }),
]);
```

**4. Add error handling:**
```typescript
try {
  const patch = await claude("patch", { prompt: "prompts/patch.md" });
} catch (err) {
  console.error(`Patch failed: ${err.message}`);
  // Fallback
}
```

**5. Log progress:**
```typescript
console.log("Step 1: Analyzing...");
const analysis = await claude("analyze", { ... });

console.log(`Step 2: Found ${analysis.output.issues.length} issues`);
```

## Prompt Tips

**Be specific:**
```markdown
✓ Good:
Task: Search for SQL injection in Python files.
Check all .py files in src/ for raw SQL string concatenation.

✗ Bad:
Task: Check security.
```

**Specify output:**
```markdown
Output JSON to `/output/output.json`:
{
  "vulnerabilities": [
    {
      "file": "path/to/file.py",
      "line": 42,
      "severity": "high",
      "description": "..."
    }
  ],
  "total_count": 3
}
```

**Reference inputs:**
```markdown
Analysis from previous stage: `/workspace/inputs/analysis/output.json`

Read the analysis, then generate fixes for each vulnerability.
```

## More Examples

Request more examples by opening issue with use case:
- Automated dependency updates
- Compliance checking
- Code generation from specs
- Test case generation
- Documentation updates
- Performance optimization
- Database migrations
- Infrastructure-as-code updates
