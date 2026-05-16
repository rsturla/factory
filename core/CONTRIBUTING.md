# Contributing to Factory

Development guide for Factory contributors.

## Repository Structure

```
factory/core/
├── cmd/                    # Binary entrypoints
│   ├── factory-api/        # HTTP API service
│   ├── factory-sandbox-manager/  # Stage executor
│   └── factory-output-processor/ # Output handler
├── internal/               # Internal packages
│   ├── api/                # API handlers
│   ├── runstore/           # PostgreSQL persistence
│   │   ├── postgres/       # PostgreSQL implementation
│   │   └── conformance/    # Compliance test suite
│   ├── verification/       # Verification gates
│   └── pipeline/           # Pipeline validation (legacy)
├── pkg/api/v1/             # Public API types
├── sdk/typescript/         # TypeScript SDK
│   └── src/
│       ├── claude.ts       # Claude stage builder
│       ├── judge.ts        # Judge stage builder
│       ├── run.ts          # Custom agent builder
│       ├── stage.ts        # Low-level builder
│       ├── client.ts       # API HTTP client
│       ├── types.ts        # Type definitions
│       └── index.ts        # Public exports
├── examples/               # Example pipelines
│   └── cve-auto-fix-imperative/
│       ├── pipeline.ts
│       └── prompts/
└── e2e_test.go             # End-to-end integration tests
```

## Development Setup

### Prerequisites

- Go 1.23+
- Bun 1.1+ (for SDK)
- PostgreSQL 16+
- Docker (for sandbox execution)

### Install Dependencies

**Go:**
```bash
go mod download
```

**SDK:**
```bash
cd sdk/typescript
bun install
```

### Database Setup

**Start PostgreSQL:**
```bash
docker run -d \
  --name factory-postgres \
  -e POSTGRES_PASSWORD=postgres \
  -e POSTGRES_DB=factory \
  -p 5432:5432 \
  postgres:16
```

**Run migrations:**
```bash
export DATABASE_URL="postgres://postgres:postgres@localhost/factory?sslmode=disable"
psql $DATABASE_URL < internal/runstore/postgres/schema.sql
```

### Run Services Locally

**API service:**
```bash
export DATABASE_URL="postgres://postgres:postgres@localhost/factory?sslmode=disable"
export ENQUEUE_ENDPOINT="http://localhost:8081"
go run cmd/factory-api/main.go
```

**Sandbox manager:**
```bash
# Requires workqueue running at localhost:8081
go run cmd/factory-sandbox-manager/main.go
```

## Testing

### Go Tests

**Run all tests:**
```bash
go test ./...
```

**Run with coverage:**
```bash
go test -cover ./...
```

**Run specific package:**
```bash
go test ./internal/api
go test ./internal/runstore/postgres
```

**Run conformance tests:**
```bash
go test ./internal/runstore/conformance
```

### SDK Tests

```bash
cd sdk/typescript
bun test
```

### E2E Tests

**Prerequisites:** API + PostgreSQL running

```bash
export DATABASE_URL="postgres://postgres:postgres@localhost/factory?sslmode=disable"
go test -v ./e2e_test.go
```

### Integration Tests

Full pipeline test requires:
- factory-api
- factory-sandbox-manager
- factory-output-processor
- workqueue
- PostgreSQL

See [e2e_test.go](./e2e_test.go) for example.

## Code Style

### Go

**Format:**
```bash
gofmt -w .
```

**Lint:**
```bash
golangci-lint run
```

**Conventions:**
- Structured logging via `slog`
- Errors: wrap with context (`fmt.Errorf("context: %w", err)`)
- SQL: use `pgx` with parameterized queries
- Tests: table-driven tests preferred

**Example:**
```go
func TestStageCreation(t *testing.T) {
	tests := []struct {
		name    string
		input   CreateStageRequest
		wantErr bool
	}{
		{"valid stage", CreateStageRequest{Name: "test"}, false},
		{"missing name", CreateStageRequest{}, true},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := createStage(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("want error %v, got %v", tt.wantErr, err)
			}
		})
	}
}
```

### TypeScript

**Format:**
```bash
bun run format  # Uses Prettier
```

**Lint:**
```bash
bun run lint  # Uses ESLint
```

**Conventions:**
- Async functions return `Promise<T>`
- Export types explicitly
- Use `const` by default
- Avoid `any`, prefer `unknown` or specific type

## Architecture Decisions

### Why Imperative Pipelines?

Declarative DSLs (YAML, JSON) hit expressiveness limits:
- Conditionals require template hacks
- Dynamic stage generation impossible
- No type checking
- Poor IDE support

TypeScript gives language primitives (`if`, `await`, `Promise.all`) + type safety.

### Why Workqueue?

Orchestrator polling is inefficient. Workqueue provides:
- At-least-once delivery
- Priority ordering
- Horizontal scaling
- Retry with backoff

### Why Separate Sandbox Manager?

Decouples orchestration from execution:
- SDK orchestrates (control flow)
- Sandbox-manager executes (stage lifecycle)
- Can scale independently
- Failure isolation

### Why Git Proxy?

Sandboxes never see git credentials:
- Security: credentials isolated
- Auditability: all git ops logged
- Policy enforcement: branch protection, signed commits

## Adding Features

### New SDK Function

**1. Define types** (`sdk/typescript/src/types.ts`):
```typescript
export interface CustomOptions {
  param1: string;
  param2?: number;
}
```

**2. Implement function** (`sdk/typescript/src/custom.ts`):
```typescript
export async function custom(name: string, opts: CustomOptions): Promise<StageResult> {
  return stage(name, {
    image: "quay.io/example/custom:latest",
    command: ["custom-cli", "--param", opts.param1],
    // ...
  });
}
```

**3. Export** (`sdk/typescript/src/index.ts`):
```typescript
export { custom } from "./custom";
```

**4. Document** (update `SDK.md`)

**5. Add example** (`examples/custom-example/`)

### New Verification Gate

**1. Implement gate** (`internal/verification/`):
```go
func CheckCustom(output map[string]any) error {
	// Validate output
	if invalid {
		return fmt.Errorf("custom validation failed: %s", reason)
	}
	return nil
}
```

**2. Register in processor** (`cmd/factory-output-processor/main.go`):
```go
gates := []verification.Gate{
	verification.CheckSecrets,
	verification.CheckDiffSize,
	verification.CheckCustom,  // Add here
}
```

**3. Add test** (`internal/verification/custom_test.go`)

**4. Document in ARCHITECTURE.md

### New Output Type

**1. Define type** (`pkg/api/v1/imperative.go`):
```go
type OutputAction struct {
	Type string `json:"type"`  // Add "custom" option
	// ... existing fields
	CustomField string `json:"custom_field,omitempty"`
}
```

**2. Implement handler** (`internal/outputprocessor/`):
```go
func handleCustomOutput(stage *v1.StageRun) error {
	// Process custom output
	return nil
}
```

**3. Wire handler** (`cmd/factory-output-processor/main.go`):
```go
switch output.Type {
case "pr":
	return handlePR(stage)
case "custom":
	return handleCustomOutput(stage)
// ...
}
```

**4. Add SDK helper** (`sdk/typescript/src/output.ts`):
```typescript
export function custom(opts: CustomOptions): OutputAction {
	return {
		type: "custom",
		customField: opts.field,
	};
}
```

## Database Migrations

Schema in `internal/runstore/postgres/schema.sql`.

**Adding column:**
```sql
ALTER TABLE stage_runs ADD COLUMN new_field TEXT;
```

**Adding table:**
```sql
CREATE TABLE new_table (
	id TEXT PRIMARY KEY,
	created_at TIMESTAMP NOT NULL
);
```

**Migration process:**
1. Update `schema.sql`
2. Write migration script (`migrations/003_add_new_field.sql`)
3. Test against fresh DB
4. Test against existing DB (upgrade)
5. Update Go types in `pkg/api/v1/`

## Release Process

**1. Version bump:**
```bash
# Update sdk/typescript/package.json
{
  "version": "0.2.0"
}
```

**2. Changelog:**
```bash
# Update CHANGELOG.md
## v0.2.0 - 2026-05-16
- Added custom stage type
- Fixed polling backoff bug
```

**3. Tag:**
```bash
git tag v0.2.0
git push origin v0.2.0
```

**4. Build binaries:**
```bash
make build
```

**5. Publish SDK:**
```bash
cd sdk/typescript
bun run build
npm publish
```

**6. Update docs:**
```bash
# Rebuild examples
# Update version refs in README.md
```

## Debugging

### API Requests

**Enable debug logging:**
```bash
export LOG_LEVEL=debug
go run cmd/factory-api/main.go
```

**Inspect database:**
```sql
-- List runs
SELECT id, phase, created_at FROM pipeline_runs ORDER BY created_at DESC LIMIT 10;

-- List stages for run
SELECT id, stage_name, phase FROM stage_runs WHERE run_id = 'run-123' ORDER BY created_at;

-- Check outbox
SELECT * FROM outbox WHERE sent = false;
```

### SDK Polling

**Add debug logs:**
```typescript
import { setConfig } from "@hummingbird/factory-sdk";

setConfig({
  apiEndpoint: "http://localhost:8080",
  pollIntervalMs: 500,  // Poll faster
});

console.log("Starting pipeline...");
const result = await claude("test", { prompt: "prompts/test.md" });
console.log("Result:", result);
```

### Sandbox Execution

**Inspect sandbox logs:**
```bash
docker logs <container-id>
```

**Enter sandbox:**
```bash
docker exec -it <container-id> /bin/sh
ls -la /workspace
cat /workspace/.prompt.md
cat /output/output.json
```

### Common Issues

**"Stage failed with exit code 1":**
- Check agent command correct
- Verify prompt file exists
- Review sandbox logs

**"Connection refused":**
- API service not running
- Wrong `FACTORY_API_ENDPOINT`
- Firewall blocking port

**"Stage stuck in pending":**
- Sandbox-manager not running
- Workqueue not configured
- Outbox poller not enqueueing

## Performance

### Profiling

**CPU profile:**
```bash
go test -cpuprofile=cpu.prof -bench=.
go tool pprof cpu.prof
```

**Memory profile:**
```bash
go test -memprofile=mem.prof -bench=.
go tool pprof mem.prof
```

### Benchmarking

```go
func BenchmarkStageCreation(b *testing.B) {
	for i := 0; i < b.N; i++ {
		createStage(CreateStageRequest{Name: "test"})
	}
}
```

**Run:**
```bash
go test -bench=. ./internal/api
```

### Optimization Tips

**Database:**
- Index frequently queried columns
- Use prepared statements
- Batch inserts where possible

**API:**
- Cache static data (field schemas, etc.)
- Use connection pooling
- Compress responses

**Sandbox:**
- Pre-warm image cache
- Reuse sandboxes when safe
- Parallel provisioning

## Documentation

**Update after changes:**
- `README.md` — High-level description
- `ARCHITECTURE.md` — Component descriptions
- `SDK.md` — API reference
- `CONTRIBUTING.md` — This file

**Generate API docs:**
```bash
# Go
godoc -http=:6060

# TypeScript
cd sdk/typescript
bun run docs
```

## Questions?

Open issue or discussion on GitHub.
