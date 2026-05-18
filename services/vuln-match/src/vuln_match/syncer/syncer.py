"""Syncer: enqueue new CVEs and new packages for matching.

Two enqueue paths:
  cve:CVE-2026-9999  — new/updated CVE (common, triggered by vuln-ingest updates)
  pkg:openssl        — new source package (rare, triggered by catalog additions)
"""

from __future__ import annotations

import logging

import psycopg
from psycopg.rows import dict_row

from factory_workqueue.enqueue import EnqueueClient

from ..config import Config
from ..db.pool import create_pool, run_migrations
from ..match.name_resolver import parse_source_rpm
from ..store.postgres import AdvisoryStore

logger = logging.getLogger(__name__)


def run_sync(cfg: Config) -> None:
    run_migrations(cfg.matchdb_url)

    match_pool = create_pool(cfg.matchdb_url, min_size=1, max_size=2)
    store = AdvisoryStore(match_pool)

    if not cfg.receiver_url:
        logger.error("RECEIVER_URL not set")
        return

    enqueue_client = EnqueueClient(cfg.receiver_url)

    enqueued_cves = _enqueue_new_cves(cfg, store, enqueue_client)
    enqueued_pkgs = _enqueue_new_packages(cfg, store, enqueue_client)

    logger.info("syncer complete: %d CVEs, %d packages enqueued", enqueued_cves, enqueued_pkgs)

    enqueue_client.close()
    match_pool.close()


def _enqueue_new_cves(cfg: Config, store: AdvisoryStore, client: EnqueueClient) -> int:
    """Find CVEs modified since last sync and enqueue them."""
    if not cfg.vulndb_url:
        return 0

    # Get last vuln checkpoint from match_state
    last_checkpoint = _get_vuln_checkpoint(store)

    try:
        with psycopg.connect(cfg.vulndb_url) as conn:
            if last_checkpoint:
                rows = conn.execute(
                    """
                    SELECT DISTINCT id FROM vulnerabilities
                    WHERE modified_at > %s
                    ORDER BY id
                    LIMIT 5000
                    """,
                    (last_checkpoint,),
                ).fetchall()
            else:
                # First run: enqueue all CVEs that have affected_packages
                rows = conn.execute(
                    """
                    SELECT DISTINCT vuln_id FROM affected_packages
                    WHERE package_name != ''
                    ORDER BY vuln_id
                    LIMIT 10000
                    """
                ).fetchall()
    except Exception as e:
        logger.error("query vulndb for new CVEs: %s", type(e).__name__)
        return 0

    enqueued = 0
    for row in rows:
        cve_id = row[0]
        try:
            client.enqueue(cfg.match_queue, f"cve:{cve_id}", priority=0)
            enqueued += 1
        except Exception as e:
            logger.error("enqueue cve:%s: %s", cve_id, e)

    # Update checkpoint
    if rows:
        _update_vuln_checkpoint(store)

    return enqueued


def _enqueue_new_packages(cfg: Config, store: AdvisoryStore, client: EnqueueClient) -> int:
    """Find source packages not yet matched and enqueue them."""
    if not cfg.catalogdb_url:
        return 0

    # Get existing match states
    match_states = set()
    try:
        with store._pool.connection() as conn:
            rows = conn.execute("SELECT source_package FROM match_state").fetchall()
            match_states = {r[0] for r in rows}
    except Exception as e:
        logger.error("query match_state: %s", type(e).__name__)

    # Get source packages from catalog
    source_packages = _get_source_packages(cfg.catalogdb_url)

    enqueued = 0
    for pkg_name, pkg_version in source_packages.items():
        if pkg_name not in match_states:
            try:
                client.enqueue(cfg.match_queue, f"pkg:{pkg_name}", priority=0)
                enqueued += 1
            except Exception as e:
                logger.error("enqueue pkg:%s: %s", pkg_name, e)

    return enqueued


def _get_source_packages(catalogdb_url: str) -> dict[str, str]:
    """Get distinct source packages with versions from catalogdb."""
    if not catalogdb_url:
        return {}

    result = {}
    try:
        with psycopg.connect(catalogdb_url) as conn:
            conn.row_factory = dict_row
            rows = conn.execute(
                "SELECT purl, name, version FROM packages WHERE type = 'rpm'"
            ).fetchall()

            for row in rows:
                src_name, _ = parse_source_rpm(row["purl"], row["name"], row["version"])
                if src_name not in result:
                    result[src_name] = row["version"]
    except Exception as e:
        logger.error("query catalogdb: %s", type(e).__name__)

    return result


def _get_vuln_checkpoint(store: AdvisoryStore) -> str | None:
    """Get the last vuln sync checkpoint."""
    state = store.get_match_state("__vuln_checkpoint__")
    return state.vuln_checkpoint if state else None


def _update_vuln_checkpoint(store: AdvisoryStore) -> None:
    """Update vuln checkpoint to now."""
    from datetime import datetime, timezone
    from ..store.postgres import MatchState

    store.upsert_match_state(MatchState(
        source_package="__vuln_checkpoint__",
        last_matched_at=datetime.now(timezone.utc),
        vuln_checkpoint=datetime.now(timezone.utc).isoformat(),
    ))
