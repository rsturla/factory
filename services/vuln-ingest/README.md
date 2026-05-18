# Vuln Ingest

Ingests CVE data from upstream vulnerability feeds, normalizes it into a common schema, and serves it as a searchable index. Acts as a read-only knowledge base — it flags data quality issues but never corrects upstream data.

## Sources

Pulls from 12 upstream feeds: cvelistV5, NVD, GHSA, RUSTSEC, Go vulnerability database, PyPA, PSF, Linux kernel, OSV, anchore NVD overrides, CISA KEV, and EPSS scores.

## Components

The service uses a two-queue reconciler pattern with a shared blob store between stages:

- **Syncer** — Periodic job that triggers data refresh by enqueuing source keys to the fetch queue.

- **Fetcher** — Pulls raw advisory data from upstream sources (git repos and HTTP feeds). Stores files in blob storage and batch-enqueues changed files to the resolve queue. Tracks progress via checkpoints (git SHA or etag) to avoid re-fetching unchanged data.

- **Resolver** — Parses raw advisory files using source-specific parsers, normalizes them into a common vulnerability model, and upserts to the database. Each source's data is scoped independently to prevent cross-source interference.

- **API Server** — REST interface for querying vulnerabilities by ID, by affected package, or in batch. Supports enrichment with EPSS scores and KEV status. Follows cross-source aliases for unified lookup.

## Storage

- **PostgreSQL** — Normalized vulnerabilities, affected packages, source records, checkpoints, and enrichment data (KEV, EPSS).
- **Blob storage** — Raw advisory files from upstream sources (S3 or local filesystem).
