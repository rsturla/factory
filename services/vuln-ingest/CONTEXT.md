# vuln-ingest

Vulnerability knowledge base for Hummingbird. Ingests CVE data from 12 upstream feeds, normalizes to a common schema, and serves via REST API. Search index — not source of truth. Flags quality issues, does not correct upstream data.

## Architecture

Two-queue reconciler pattern via factory workqueue:

```
CronJob (syncer) → vuln-fetch queue → fetcher reconciler → shared volume
                                                          ↓
                   vuln-resolve queue ← batch enqueue ←───┘
                         ↓
                   resolver reconciler → PostgreSQL → API server
```

Each source's affected_packages are stored with a `source` column — multiple sources can contribute data for the same CVE without clobbering each other.

## Binaries

| Binary | Purpose | Port |
|--------|---------|------|
| `cmd/fetcher` | Fetch reconciler — pulls from upstream sources, writes raw files | 8082 |
| `cmd/resolver` | Resolve reconciler — parses files, normalizes, upserts to DB | 8082 |
| `cmd/api` | REST query API for downstream services | 8080 |
| `cmd/syncer` | CronJob — enqueues source keys to vuln-fetch queue | - |

## Data Sources

| Source | Type | Parser | Diff mechanism |
|--------|------|--------|---------------|
| cvelistV5 | git | CVEListV5Parser (+ ADP) | git diff since last commit SHA |
| GHSA | git | OSVParser (shared) | git diff |
| RUSTSEC | git (osv branch) | OSVParser (shared) | git diff |
| govuln | git | OSVParser (shared) | git diff |
| PyPA | git | OSVParser (shared) | git diff |
| PSF | git | OSVParser (shared) | git diff |
| kernel | git | OSVParser (shared) | git diff |
| OSV | GCS bucket | OSVParser (shared) | modified since timestamp |
| NVD | REST API | NVDParser | lastModStartDate param |
| anchore-nvd-overrides | git | NVDOverridesParser | git diff (Anchore CPE enrichment for NVD backlog) |
| KEV | file download | ParseKEVBatch | diff against DB by catalog version |
| EPSS | CSV download | ParseEPSSBatch | diff against DB (>0.01 delta) |

## Adding a New Source

1. Implement `source.Source` interface (or reuse `source.GitSource`)
2. Register in `cmd/fetcher/main.go` `registerSources()`
3. Add parser if new format (or map to existing parser in `resolve.NewReconciler`)
4. Add source name to `cmd/syncer/main.go` `defaultSources`

## Key Design Decisions

- **Flag, don't fix**: quality flags on affected_packages indicate data issues
- **Source-scoped storage**: affected_packages carry a `source` column; deletes scoped to `(vuln_id, source)` to prevent cross-source clobbering
- **Vendor separation**: `vendor` and `package_name` stored as separate columns for clean downstream matching
- **Range type preserved**: `VersionRange.RangeType` captures SEMVER/ECOSYSTEM/GIT/semver/git so downstream knows which comparator to use
- **Exports over APIs**: prefer git clone over REST for feed ingestion
- **Shared OSV parser**: single parser handles 7 sources (GHSA, RUSTSEC, govuln, PyPA, PSF, kernel, OSV)
- **NVD overrides**: Anchore's nvd-data-overrides provides ~25K CPE configurations for CVEs not yet analyzed by NVD, filling the `unmapped_cpe`/`empty_range` gap
- **Enrichment via two-queue**: KEV/EPSS download → diff → batch file → resolve
- **Batch enqueue**: up to 5K items per `/wq/enqueue-batch` call
- **Raw hash dedup**: source_records.raw_hash skips unchanged files
- **ADP containers**: cvelistV5 parser processes ADP (CISA-ADP, vendor) containers for additional affected entries and CVSS metrics

## Environment Variables

### Fetcher
- `DATABASE_URL` — PostgreSQL connection string
- `DATA_DIR` — shared volume mount path (default: `/data`)
- `RECEIVER_URL` — workqueue receiver endpoint
- `RESOLVE_QUEUE` — resolve queue name (default: `vuln-resolve`)
- `NVD_API_KEY` — optional NVD API key for higher rate limits
- `OSV_ECOSYSTEMS` — comma-separated OSV ecosystems (default: `Linux,OSS-Fuzz`)

### Resolver
- `DATABASE_URL` — PostgreSQL connection string
- `DATA_DIR` — shared volume mount path (read-only)

### API
- `DATABASE_URL` — PostgreSQL connection string

### Syncer
- `RECEIVER_URL` — workqueue receiver endpoint
- `FETCH_QUEUE` — fetch queue name (default: `vuln-fetch`)
- `SOURCES` — comma-separated source list (default: all 11)

## API Endpoints

- `GET /v1/vulns/{id}` — single vulnerability with KEV/EPSS enrichment
- `GET /v1/vulns?modified_since=&updated_since=&limit=&offset=&enrich=false` — list/changelog feed
- `POST /v1/vulns:batchGet` — bulk lookup `{"ids": [...]}`
- `GET /v1/affected?package_name=&ecosystem=&purl=&enrich=false` — vulns affecting a package (ecosystem optional, purl alternative)
- `POST /v1/affected:batchQuery` — bulk package query `{"queries": [{ecosystem, package_name, vendor, purl}]}`
- `GET /v1/sources` — sync status of all sources
- `GET /healthz` — health check
