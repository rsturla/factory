# Vuln Match

Matches CVEs against Hummingbird RPM packages and produces security advisories. The goal is to generate Hummingbird's own advisory feed that vulnerability scanners (Grype, Trivy) can consume — similar to Red Hat's RHSA or Chainguard's Wolfi secdb.

## Components

- **Name Resolver** — Maps RPM source package names to upstream CVE identities using a layered approach: direct match, pattern variants, CPE dictionary, and agent-proposed mappings.

- **Version Matcher** — Determines whether a package version falls within a CVE's affected ranges. Extracts upstream versions from RPM version strings and handles quality tiers and cross-source conflicts.

- **Agent Enrichment** — Uses an LLM (Claude Haiku via Vertex AI) to interpret CVE descriptions, catch false positives (wrong project, platform, or branch), and assign confidence levels.

- **Reconciler** — Workqueue-driven worker that processes two work item types: match a single CVE against all packages, or match a single package against all CVEs. Stores draft advisories in the database.

- **Syncer** — Periodic job that queries the catalog for source packages and the vulnerability database for CVEs, then enqueues work items for matching.

- **Triage** — Human-facing review workflow for high-priority matches before publication.

- **API Server** — Serves advisories per package and per CVE. Publishes an OSV-format feed for scanner integration. Supports review via POST endpoints.

## Storage

- **matchdb** (PostgreSQL) — Draft and reviewed advisories with status, confidence, severity, and fix versions.
- Reads from **catalogdb** (packages) and **vulndb** (CVEs) owned by sibling services.
