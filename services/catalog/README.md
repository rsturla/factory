# Catalog

Discovers, indexes, and serves metadata for Hummingbird's OCI container images. Continuously monitors registries for new images and tags, fetches their contents, generates SBOMs, and extracts package inventories into a queryable database.

## Components

The catalog runs as five cooperating services connected by workqueue pipelines:

- **Syncer** — Enumerates all repositories in a registry namespace and seeds the discovery pipeline. Runs as a periodic job.

- **Discoverer** — Resolves image references into their constituent platforms (OS/architecture variants). Records images, tags, and platforms, then enqueues new platforms for fetching.

- **Fetcher** — Pulls platform images, generates SBOMs using Syft, and stores them in blob storage. Captures OCI configuration metadata (entrypoint, labels, layer count, compressed size).

- **Analyzer** — Reads SBOMs from blob storage, extracts individual software packages (identified by PURL), and associates them with their platform in the database.

- **API Server** — REST interface for querying the catalog. Supports listing images, fetching packages by platform, searching by package name, retrieving SBOMs, and diffing packages across platforms.

## Storage

- **PostgreSQL** — Images, tags, platforms, packages, and SBOM metadata.
- **Blob storage** — Raw SBOM documents (S3 or local filesystem).
