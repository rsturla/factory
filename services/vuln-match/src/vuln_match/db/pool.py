"""PostgreSQL connection pool wrapper."""

from __future__ import annotations

import logging
from pathlib import Path

import psycopg
from psycopg_pool import ConnectionPool

logger = logging.getLogger(__name__)

_MIGRATIONS_DIR = Path(__file__).parent / "migrations"


def create_pool(conninfo: str, min_size: int = 2, max_size: int = 10) -> ConnectionPool:
    return ConnectionPool(conninfo, min_size=min_size, max_size=max_size)


def run_migrations(conninfo: str) -> None:
    migration_files = sorted(_MIGRATIONS_DIR.glob("*.sql"))
    if not migration_files:
        logger.warning("no migration files found in %s", _MIGRATIONS_DIR)
        return

    with psycopg.connect(conninfo) as conn:
        conn.execute("""
            CREATE TABLE IF NOT EXISTS schema_migrations (
                version TEXT PRIMARY KEY,
                applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
            )
        """)

        applied = {row[0] for row in conn.execute("SELECT version FROM schema_migrations").fetchall()}

        for f in migration_files:
            version = f.stem
            if version in applied:
                continue

            logger.info("applying migration %s", version)
            sql = f.read_text()
            conn.execute(sql)
            conn.execute("INSERT INTO schema_migrations (version) VALUES (%s)", (version,))

        conn.commit()
    logger.info("migrations complete")
