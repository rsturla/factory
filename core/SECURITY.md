# Factory Core - Security Notes

## TypeScript Pipeline Execution

### Current State (Phase 1b)

TypeScript pipelines evaluated with Bun runtime have **NO SANDBOXING** in Phase 1b. Bun executes with full process privileges.

**Attack Surface:**
- Full filesystem access (can read secrets, credentials)
- Network access (can exfiltrate data)
- Process spawning (can fork-bomb)
- Example exploit: `Bun.file("/etc/passwd").text()` in pipeline.ts

### Mitigations Applied

1. **Path Validation** (loader.go:145-157)
   - Verifies TypeScript file within allowed basePath
   - Prevents arbitrary .ts file execution via path traversal
   - Uses filepath.Rel() to detect escape attempts

2. **Output Size Limit** (loader.go:160-183)
   - 10MB cap on stdout to prevent OOM attacks
   - Malicious pipeline can't exhaust memory with infinite JSON

3. **Request Validation** (server.go:295-318)
   - filepath.Clean() + traversal detection
   - Blocks: `../../`, `/absolute`, embedded `..`, backslash variants
   - 7 test cases covering attack vectors

### Production Requirements (Phase 2+)

**CRITICAL:** Before production deployment, add one of:

1. **Container Isolation** (Recommended)
   ```yaml
   # Run Bun in isolated container per pipeline eval
   docker run --rm --network=none --read-only \
     -v pipeline:/pipeline:ro \
     -v output:/output:rw \
     oven/bun run /pipeline/pipeline.ts
   ```

2. **Bun Permission Flags**
   ```bash
   bun run --allow-read=/workspace/.factory \
           --allow-write=/tmp/bun-output \
           --no-net \
           pipeline.ts
   ```

3. **OpenShell Integration**
   - Evaluate TypeScript in sandbox (same as agent runtime)
   - Landlock filesystem isolation
   - Network namespace restrictions
   - Resource limits (CPU, memory, time)

### Code Review Findings Addressed

**Fixed in this iteration:**
- ✅ Path traversal validation (filepath.Clean + basePath verification)
- ✅ Bun output size limit (10MB via io.LimitReader)
- ✅ Path escape detection (filepath.Rel check in loader)
- ✅ Request body size limit (1MB in API handler)
- ✅ Enhanced test coverage (7 path traversal cases, TypeScript path escape)

**Deferred to Phase 3:**
- ⚠️ Bun sandboxing (container/permissions/OpenShell)
- ⚠️ Sandbox cleanup retry/GC (cleanup queue)

### Current Deployment Guidance

**DO NOT** expose factory-api to untrusted users in Phase 1b configuration. TypeScript evaluation runs with full process privileges.

**Safe for:**
- Internal development/testing
- Trusted pipeline authors only
- Sandboxed deployment environments

**Unsafe for:**
- Public API endpoints
- Multi-tenant environments
- Untrusted pipeline repositories

## Output Processing Security (Phase 2)

### Mitigations Applied

1. **Output Size Validation** (output/reconciler.go)
   - 50MB max output size (JSON serialized)
   - Prevents DoS via unbounded output payloads
   - Failed validation marks stage as failed, logs error

2. **Output Depth Validation** (output/reconciler.go)
   - 10 levels max nesting depth for maps/arrays
   - Prevents stack overflow from deeply nested structures
   - Recursive validation before handler processing

3. **Type-Specific Limits** (output/report.go)
   - Report title: 1KB max
   - Report content: 10MB max
   - Type assertions validate bounds before processing

4. **Audit Trail** (output/reconciler.go)
   - All output processing operations logged to audit events
   - Includes output type, result, handler details
   - Enables compliance tracking and security investigations

5. **Verification Gates** (verification/*)
   - No-secrets scanner (AWS keys, GitHub tokens, private keys, high-entropy strings)
   - Diff size limits (5000 lines, 100 files max)
   - Path allowed validation (denies *.env, credentials.*, .ssh/*, etc.)
   - Shannon entropy calculation for API key detection

6. **PR Output Handler** (output/pr.go)
   - Validates PR output format (title, body, files)
   - File content size limits (1MB per file)
   - PR metadata validation
   - Note: Actual PR creation requires GitHub/GitLab client (Phase 3)

7. **Agent Execution Control** (sandbox/reconciler.go)
   - Detached process execution (no blocking on agent command)
   - Configurable timeout (30s default, 0 for tests)
   - Exit code validation (non-zero marks stage failed)
   - Timeout enforcement (marks stage failed after deadline)
   - Process status polling (adaptive: 5s/15s/30s intervals)
   - Sandbox cleanup on all failure paths (timeout, exit code, provisioning)
   - Output size limit (100MB max per file via LimitReader)

### Known Limitations

1. **No XSS Sanitization**: Output stored as-is, no HTML/JS escaping. If rendered in web UI, must sanitize at display time.
2. **No Schema Enforcement**: Arbitrary keys allowed in output map. Validation only checks size/depth/type-specific fields.
3. **Silent Audit Failures**: Audit event creation failure logged but doesn't fail output processing. Could miss security events.
4. **PR Creation Stub**: PRHandler validates output but doesn't create actual PRs (requires GitHub/GitLab API integration).
5. **Git Ref Extraction Incomplete**: Git-proxy ref extraction uses placeholder (needs git protocol parsing for accurate branch policy enforcement).

## Transaction Safety

All database operations use atomic transactions:
- CreateRunWithOutbox: single transaction for run + outbox
- Postgres commit is atomic (no partial writes)
- pgx defer rollback pattern is correct (no-op after commit)

## Path Traversal Protection

Request validation uses defense-in-depth:
1. Application layer: validateCreateRunRequest (filepath.Clean + checks)
2. Loader layer: basePath verification (filepath.Rel escape detection)
3. Test coverage: 7 adversarial cases (embedded `..`, absolute, backslash)

Protection covers:
- `../../etc/passwd`
- `/absolute/path`
- `foo/../bar`
- `foo\\..\bar`
- Windows backslash variants

## Outbox Reliability

Exponential backoff on DB errors prevents hot loops:
- 1s → 2s → 4s → 8s → 16s → max 30s
- Resets to 1s on success
- Prevents DB connection exhaustion during outages

## Known Limitations

1. **No rate limiting** on API endpoints (add in Phase 2)
2. **No request authentication** (interface defined, noop implementation)
3. **No authorization** on pipeline execution (interface defined, noop implementation)
4. **Sandbox deletion failures** logged but not retried (cleanup queue in Phase 2)
5. **TypeScript execution** not sandboxed (see above)

## Security Review Schedule

Phase 1: ✅ Complete (2 iterations)
- Initial review: 15 findings
- Security iteration: 5 critical fixes
- Test coverage: 20+ security test cases

Phase 2: ✅ Complete (3 iterations)
- Git-proxy implementation (token minting, policy enforcement, audit)
- Git-proxy credential injection (scoped tokens in sandbox environment)
- Output validation (size, depth, type-specific limits)
- Verification gates (no-secrets, diff-size, path-allowed)
- Prompt template engine (path traversal protection)
- Real agent execution (detached process with exit code tracking)
- Code review: 4 critical security fixes (output size, type bounds, sanitization, race conditions)
- Test coverage: 30+ security test cases

**Deferred to Phase 3:**
- GitHub/GitLab API integration (actual PR creation)
- Git-proxy credential injection into sandbox
- Bun sandboxing (container/OpenShell isolation)
- Git ref extraction (git protocol parsing)

Phase 3: Pending
- Authorization policy audit
- Multi-tenancy isolation
- Secrets management review
- Production threat model
- GitHub/GitLab client security
