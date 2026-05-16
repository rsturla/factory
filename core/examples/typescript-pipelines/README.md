# TypeScript Pipeline Examples

Advanced patterns showing what TypeScript DSL enables vs YAML.

## Examples

### 1. CVE Auto-Fix (`cve-auto-fix/`)

**Pattern:** Conditional execution + dynamic stages

Demonstrates:
- `when` conditions based on upstream outputs
- Dynamic model selection (Opus for critical, Sonnet for high/medium)
- Parallel test execution (3 test types)
- Fan-in synthesis of test results
- Conditional PR creation vs issue filing
- Structured output schemas

**Key feature:** Pipeline branches based on `auto_fixable` analysis result.

### 2. Multi-Repo Refactor (`multi-repo-refactor/`)

**Pattern:** Dynamic resource generation + cross-repo orchestration

Demonstrates:
- Runtime resource generation from `params.repos` JSON array
- Parallel analysis across N repositories
- Dependency-ordered execution (migration plan determines order)
- Cross-repo validation
- Integration testing with all repos
- Per-repo PR creation

**Key feature:** Scale to any number of repos via params, not hardcoded stages.

### 3. Security Audit (`security-audit/`)

**Pattern:** Multiple LLM judges + bias mitigation

Demonstrates:
- Parallel specialized scans (7 scan types)
- Multiple independent judges reviewing same findings
- Judge calibration stage (reconcile disagreements)
- Adversarial review (red team testing fixes)
- Complex fan-in with multiple input stages
- Structured verification outputs

**Key feature:** 2+ judges with different models/prompts → calibration → consensus.

### 4. Batch Data Processor (`data-pipeline/`)

**Pattern:** Dynamic parallelism + deterministic fan-in

Demonstrates:
- Runtime batch creation (1000s of files → N batches)
- Dynamic fan-out (create M stages for M batches)
- Conditional stage creation (`when` based on batch count)
- Deterministic fan-in (simple aggregation, no LLM)
- Quality analysis sampling
- Conditional commit based on quality score

**Key feature:** Handle variable workload without changing pipeline definition.

## What TypeScript Enables

### vs YAML:

**Type Safety**
```typescript
// Compile-time checks
resources: [resources.targetRepo]  // ✓ exists
resources: [resources.typo]        // ✗ error
```

**Logic & Computation**
```typescript
// Dynamic resource generation
const repos = JSON.parse(params.repos || "[]");
repos.forEach(repo => {
  resources[`repo_${repo}`] = resource(git({...}));
});
```

**Conditionals**
```typescript
if (params.severity === "critical") {
  stages.push(...criticalPath);
} else {
  stages.push(...standardPath);
}
```

**Loops**
```typescript
// Generate N parallel stages
scanners.forEach(scanner => {
  stages.push(stage(scanner.name, {...}));
});
```

**Template String Interpolation**
```typescript
environment: {
  COMMIT_MESSAGE: `Run ${params.run_id}: processed ${count} files`
}
```

**Reusable Functions**
```typescript
function createTestStage(name: string, focus: string) {
  return stage(name, {
    agent: { ... },
    output: { ... }
  });
}

const tests = ["unit", "integration", "e2e"].map(t => 
  createTestStage(`${t}-test`, t)
);
```

**IDE Support**
- Autocomplete for resources, params, outputs
- Inline documentation
- Jump to definition
- Refactoring tools

## YAML Equivalent Complexity

CVE auto-fix in YAML would require:
- 2 separate pipeline files (auto vs manual path)
- No dynamic model selection
- Hardcoded parallel stages (can't scale)
- Template string hacks for conditionals
- No type safety

Multi-repo refactor in YAML:
- Impossible to handle variable repo count
- Would need pipeline-per-repo (not scalable)
- Can't pass structured data between stages easily

## When to Use TypeScript

**Use TypeScript when:**
- Dynamic stage creation needed
- Conditional execution logic complex
- Need to loop/generate stages
- Passing structured data between stages
- Want IDE autocomplete/type checking
- Pipeline logic reusable across projects

**Use YAML when:**
- Simple linear pipeline (3-5 stages)
- No conditionals needed
- Static resource bindings
- Quick prototyping
- Non-developer users (ops team)

## Implementation Note

TypeScript evaluated with Bun runtime in `factory-api` service:

```
POST /api/v1/runs
{
  "pipeline_repo": "github.com/org/pipelines",
  "pipeline_path": ".factory/cve-auto-fix",
  "parameters": {
    "cve_id": "CVE-2026-1234",
    "severity": "critical",
    "auto_fix": "true"
  }
}
```

API service:
1. Clones pipeline repo
2. Runs `bun run pipeline.ts` → JSON output
3. Parses JSON → `PipelineSpec`
4. Validates + stores in run store
5. Enqueues into `sf-pipeline` queue
