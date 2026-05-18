# Hummingbird CVE-to-RPM Matching: Design Document

## Problem

Given a Hummingbird RPM package (e.g., `curl-8.19.0-2.hum1`), determine which CVEs affect it. This is the foundation for generating Hummingbird's own security advisory data — the distro equivalent of Red Hat's RHSA/OVAL or Chainguard's Wolfi secdb.

## Why This Is Hard

CVE databases (NVD, cvelistV5) describe vulnerabilities using upstream project names and upstream version numbers. Hummingbird packages use RPM names with distro-specific versioning. The mapping between these two worlds is imprecise:

| NVD says | RPM says | Gap |
|----------|----------|-----|
| `curl` versions 8.11.0-8.16.0 | `curl-8.19.0-2.hum1` | RPM version scheme differs |
| `http_server` (Apache) | `httpd` | Different name entirely |
| `php` | `php-cli`, `php-xml`, `php-fpm` | RPM splits into subpackages |
| `.NET 8.0` | `dotnet8.0` | Product name vs package name |
| "affects libssh" | `libssh` RPM exists | But CVE is for Go's `x/crypto/ssh` |

## The Matching Pipeline

### Stage 1: Name Resolution

**Goal:** Map each RPM source package name to its upstream CVE/NVD identity.

**Method (layered, in priority order):**

1. **Direct name match** — RPM source name matches NVD package_name exactly.
   - `openssl` → `openssl` ✓
   - Works for ~70% of packages.

2. **Source RPM extraction** — Parse the PURL `upstream=` qualifier to get the source RPM name. All binary subpackages (`php-cli`, `php-xml`) map to a single source (`php`).
   - `pkg:rpm/redhat/php-cli@8.5.6?upstream=php-8.5.6-1.hum1.src.rpm` → source `php`
   - This is the critical step that handles subpackage matching.

3. **Pattern-based variants** — Generate candidate names by:
   - Stripping `lib` prefix: `libxml2` → `xml2` (then try `libxml2` too)
   - Stripping version suffix: `python3.14` → `python`, `glib2` → `glib`
   - Hyphen/underscore swap: `util-linux` → `util_linux`

4. **CPE dictionary lookup** — The NVD CPE dictionary maps product names across naming conventions.
   - `httpd` → CPE product `http_server` (vendor: `apache`)
   - Download from: `https://nvd.nist.gov/feeds/json/cpe/2.0/nvdcpe-2.0.tar.gz`
   - Build a local index: RPM name → CPE product name → NVD package_name

5. **Agent-proposed mappings** — For packages that still don't match, an LLM agent (Claude Haiku via Vertex AI) proposes mappings given:
   - The RPM name and SRPM description (from repodata)
   - The CPE dictionary entries
   - Example CVEs with their NVD package names
   - The agent proposes, a human reviews and approves.

6. **Reviewed mapping file** — All approved mappings stored in `mappings.json`. This is the only file that contains human-curated knowledge. Everything else is algorithmic.

**What NOT to do:**
- Don't hardcode mappings in source code — use the mapping file.
- Don't match by vendor alone — `microsoft:python` is VS Code Python extension, not CPython.
- Don't match `kernel-headers` to `linux_kernel` — headers are for compilation, kernel CVEs don't apply.

### Stage 2: Version Range Matching

**Goal:** Given a CVE that mentions this package name, determine if the RPM's upstream version is within the affected range.

**Method:**

1. **Extract upstream version** from RPM version string.
   - `8.19.0-2.hum1` → upstream `8.19.0` (strip `-release.distro` suffix)

2. **Check version ranges** from vuln-ingest data (NVD + anchore-nvd-overrides + cvelistV5).
   Each range has:
   - `introduced`: first affected version
   - `fixed`: first unaffected version (exclusive)
   - `last_affected`: last affected version (inclusive)

3. **Quality tiers for ranges:**

   | Tier | Has `fixed` or `last_affected`? | Has `introduced`? | Reliability |
   |------|--------------------------------|-------------------|-------------|
   | High | Yes | Yes or No | Trust it — clear version bounds |
   | All-versions | `fixed: "*"` | Yes | All versions affected, no fix exists |
   | Low | No | Yes only | Suspicious — "vulnerable forever" |
   | None | No | No | Skip — no version data |

4. **Cross-source conflict resolution:**
   - If NVD says "fixed in 8.16.0" and cvelistV5 says "introduced in 8.16.0" (contradictory), prefer the range with both `introduced` AND `fixed`.
   - If any high-quality range says "not affected," trust it over low-quality ranges that disagree.

5. **What this stage CANNOT determine:**
   - Whether the distro RPM has backported the upstream fix.
   - Whether distro-specific patches re-introduce the vulnerability.
   - These require distro-specific advisory data (Stage 4).

### Stage 3: Agent Enrichment

**Goal:** Use an LLM to interpret CVE descriptions and catch false positives that version matching misses.

**Method:**

Feed the agent the CVE description, affected entries, and package context. Ask it to determine:
- Does this CVE actually apply to this package? (e.g., "composer" WordPress plugin vs PHP Composer)
- Is the version range for a different branch? (OpenSSL 1.1.x vs 3.x)
- Is this platform-specific? ("Windows only")
- Is this disputed? ("NOTE: disputed by third parties")

**Results from prototype testing:**
- Agent reviewed 140 CVEs, changed status on 139.
- **Zero false dismissals** — every agent "not-affected" decision was validated correct.
- Major false positive categories eliminated:
  - Wrong project (WordPress plugin, Go library, Erlang module)
  - Wrong platform (Windows-only CVEs)
  - Wrong branch (old version branch)

**Safety model:**
- Agent proposes status changes with reasoning.
- High-confidence agent decisions can be auto-applied.
- Low-confidence decisions flagged as `under-review` for human triage.
- All agent decisions are logged with reasoning for audit.

**Runtime:** Claude Haiku 4.5 via Vertex AI with ADC. ~20 CVEs per API call, ~2 seconds per batch.

### Stage 4: Human Review & Advisory Publication

**Goal:** Security team reviews remaining `detected` items and publishes advisory data.

**What the team reviews:**
- `detected` items not yet triaged by the agent (batch remaining through agent)
- `under-review` items where the agent was uncertain
- `affected` items to add `distro_fixed_version` when a fix is released

**What the team does NOT need to review:**
- `not-affected` items validated by version comparison (upstream version past fix)
- `not-affected` items validated by agent (wrong project/platform/branch)

**Advisory output format** (per source package):

```yaml
package: curl
upstream_version: "8.19.0"
rpm_version: "8.19.0-3.1.hum1"
binary_packages: [curl, libcurl]
advisories:
  - id: CVE-2026-5545
    status: affected
    severity: CVSS_V3_1
    cvss_score: "6.5"
    epss_score: 0.00052
    upstream_fixed_version: "8.20.0"
    distro_fixed_version: "8.20.0-1.hum1"  # added by security team
    notes: "Upstream version 8.19.0 is within affected range"
  - id: CVE-2025-10148
    status: not-affected
    confidence: medium
    upstream_fixed_version: "8.16.0"
    notes: "Upstream version 8.19.0 >= fix version 8.16.0"
```

## Accuracy Analysis (Validated Against Grype)

Tested against 408 Grype findings on Hummingbird images:

| Category | Count | % | Source |
|----------|-------|---|--------|
| Correctly identified as affected | 133 | 33% | Version matching + agent |
| Correctly identified as not-affected | 118 | 29% | Version comparison (upstream fixed) |
| Agent correctly dismissed (wrong project/platform) | ~80 | 20% | Agent enrichment |
| Upstream says fixed, Grype uses distro data | 232 | — | Inherent gap* |
| Name not resolvable | 61 | — | Needs rpms monorepo cve_product |
| No version range data in NVD | 40 | — | NVD data quality |

*The 232 "upstream fixed" items are NOT errors — they are cases where the upstream release includes the fix but the distro RPM may not have backported it. Grype knows this from distro-specific advisory feeds. Our tool doesn't have that data yet — producing it IS the purpose of this service.

**Agent accuracy: 100%** — zero false dismissals across 140 reviewed CVEs.

## Data Sources

| Source | What it provides | How we use it |
|--------|-----------------|---------------|
| Catalog service (catalogdb) | RPM packages with PURLs, source RPM names | Package identity, upstream versions |
| Vuln-ingest (vulndb) | 800K CVEs with affected packages, version ranges, CVSS, EPSS, KEV | CVE matching data |
| NVD CPE dictionary | Product name → vendor:product mapping | Name resolution (Stage 1) |
| SRPM repodata | Package descriptions | Agent context for name resolution |
| CPE index (local) | Cached CPE dictionary lookup | Fast name resolution |
| mappings.json | Agent-proposed, human-reviewed name mappings | Override name resolution |

## Architecture (When Productionized)

```
┌─────────────────────────────────────────────┐
│           vuln-match service                │
│                                             │
│  Workqueue reconciler pattern:              │
│                                             │
│  syncer (CronJob)                           │
│  ├── queries catalogdb for source packages  │
│  ├── queries vulndb for new/updated CVEs    │
│  └── enqueues packages needing re-matching  │
│                                             │
│  matcher (reconciler, KEDA scaled)          │
│  ├── Stage 1: name resolution              │
│  ├── Stage 2: version range matching        │
│  ├── Stage 3: agent enrichment (Vertex AI)  │
│  └── stores draft advisories in matchdb     │
│                                             │
│  api                                        │
│  ├── GET /advisories/{package}              │
│  ├── GET /advisories/{package}/{cve}        │
│  ├── POST /advisories/{package}/{cve}/review│
│  └── GET /feed/secdb.json (scanner feed)    │
│                                             │
│  publisher                                  │
│  └── exports reviewed advisories as:        │
│      ├── OSV feed (for Grype/Trivy)         │
│      ├── secdb JSON (Alpine/Wolfi format)   │
│      └── YAML files (human-readable)        │
└─────────────────────────────────────────────┘
```

## Key Principles

1. **Flag, don't fix** — store the best mechanical interpretation with quality flags. Never silently correct bad upstream data.

2. **Agent proposes, human disposes** — LLM enrichment is a triage accelerator, not an autonomous decision maker. High-confidence agent decisions can be auto-applied; everything else goes to human review.

3. **Upstream truth is the starting point** — version range matching against upstream data gives you the baseline. Distro-specific advisory data (backports, patches) is layered on top by the security team.

4. **Multiple sources, explicit disagreement** — when NVD and cvelistV5 disagree, show both. Don't pick a winner silently.

5. **The advisory IS the product** — the goal is not to replicate Grype. The goal is to produce Hummingbird's own advisory feed that scanners consume. Once published, Grype scanning Hummingbird images will be accurate because it reads YOUR data.
