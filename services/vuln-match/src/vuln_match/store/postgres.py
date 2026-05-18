"""PostgreSQL advisory store."""

from __future__ import annotations

import json
import logging
from dataclasses import asdict, dataclass, field
from datetime import datetime, timezone
from typing import Any

import psycopg
from psycopg.rows import dict_row

logger = logging.getLogger(__name__)


@dataclass
class Advisory:
    source_package: str
    vuln_id: str
    status: str = "detected"
    confidence: str = "medium"
    match_type: str = ""
    upstream_version: str = ""
    rpm_version: str = ""
    upstream_fixed_version: str = ""
    distro_fixed_version: str = ""
    severity: str = ""
    cvss_score: str = ""
    epss_score: float = 0.0
    in_kev: bool = False
    flags: list[str] = field(default_factory=list)
    notes: str = ""
    agent_reasoning: str = ""
    reviewed_by: str = ""
    reviewed_at: datetime | None = None
    id: str = ""
    created_at: datetime | None = None
    updated_at: datetime | None = None


@dataclass
class NameMapping:
    rpm_name: str
    vuln_names: list[str]
    source: str = "manual"
    confidence: str = "medium"
    reviewed: bool = False
    agent_reasoning: str = ""
    usage_count: int = 0


@dataclass
class MatchState:
    source_package: str
    last_matched_at: datetime | None = None
    vuln_checkpoint: str = ""
    catalog_version: str = ""


class AdvisoryStore:
    def __init__(self, pool: Any) -> None:
        self._pool = pool

    def upsert_advisory(self, adv: Advisory) -> None:
        with self._pool.connection() as conn:
            conn.execute(
                """
                INSERT INTO advisories (
                    source_package, vuln_id, status, confidence, match_type,
                    upstream_version, rpm_version, upstream_fixed_version,
                    distro_fixed_version, severity, cvss_score, epss_score,
                    in_kev, flags, notes, agent_reasoning, updated_at
                ) VALUES (
                    %(source_package)s, %(vuln_id)s, %(status)s, %(confidence)s, %(match_type)s,
                    %(upstream_version)s, %(rpm_version)s, %(upstream_fixed_version)s,
                    %(distro_fixed_version)s, %(severity)s, %(cvss_score)s, %(epss_score)s,
                    %(in_kev)s, %(flags)s, %(notes)s, %(agent_reasoning)s, now()
                )
                ON CONFLICT (source_package, vuln_id) DO UPDATE SET
                    status = EXCLUDED.status,
                    confidence = EXCLUDED.confidence,
                    match_type = EXCLUDED.match_type,
                    upstream_version = EXCLUDED.upstream_version,
                    rpm_version = EXCLUDED.rpm_version,
                    upstream_fixed_version = EXCLUDED.upstream_fixed_version,
                    distro_fixed_version = EXCLUDED.distro_fixed_version,
                    severity = EXCLUDED.severity,
                    cvss_score = EXCLUDED.cvss_score,
                    epss_score = EXCLUDED.epss_score,
                    in_kev = EXCLUDED.in_kev,
                    flags = EXCLUDED.flags,
                    notes = EXCLUDED.notes,
                    agent_reasoning = EXCLUDED.agent_reasoning,
                    updated_at = now()
                WHERE advisories.reviewed_at IS NULL
                """,
                {
                    "source_package": adv.source_package,
                    "vuln_id": adv.vuln_id,
                    "status": adv.status,
                    "confidence": adv.confidence,
                    "match_type": adv.match_type,
                    "upstream_version": adv.upstream_version,
                    "rpm_version": adv.rpm_version,
                    "upstream_fixed_version": adv.upstream_fixed_version,
                    "distro_fixed_version": adv.distro_fixed_version,
                    "severity": adv.severity,
                    "cvss_score": adv.cvss_score,
                    "epss_score": adv.epss_score,
                    "in_kev": adv.in_kev,
                    "flags": adv.flags,
                    "notes": adv.notes,
                    "agent_reasoning": adv.agent_reasoning,
                },
            )

    def upsert_advisories(self, advisories: list[Advisory]) -> None:
        if not advisories:
            return
        with self._pool.connection() as conn:
            for adv in advisories:
                conn.execute(
                    """
                    INSERT INTO advisories (
                        source_package, vuln_id, status, confidence, match_type,
                        upstream_version, rpm_version, upstream_fixed_version,
                        distro_fixed_version, severity, cvss_score, epss_score,
                        in_kev, flags, notes, agent_reasoning, updated_at
                    ) VALUES (
                        %(source_package)s, %(vuln_id)s, %(status)s, %(confidence)s, %(match_type)s,
                        %(upstream_version)s, %(rpm_version)s, %(upstream_fixed_version)s,
                        %(distro_fixed_version)s, %(severity)s, %(cvss_score)s, %(epss_score)s,
                        %(in_kev)s, %(flags)s, %(notes)s, %(agent_reasoning)s, now()
                    )
                    ON CONFLICT (source_package, vuln_id) DO UPDATE SET
                        status = EXCLUDED.status, confidence = EXCLUDED.confidence,
                        match_type = EXCLUDED.match_type, upstream_version = EXCLUDED.upstream_version,
                        rpm_version = EXCLUDED.rpm_version, upstream_fixed_version = EXCLUDED.upstream_fixed_version,
                        distro_fixed_version = EXCLUDED.distro_fixed_version, severity = EXCLUDED.severity,
                        cvss_score = EXCLUDED.cvss_score, epss_score = EXCLUDED.epss_score,
                        in_kev = EXCLUDED.in_kev, flags = EXCLUDED.flags,
                        notes = EXCLUDED.notes, agent_reasoning = EXCLUDED.agent_reasoning,
                        updated_at = now()
                    WHERE advisories.reviewed_at IS NULL
                    """,
                    {
                        "source_package": adv.source_package, "vuln_id": adv.vuln_id,
                        "status": adv.status, "confidence": adv.confidence,
                        "match_type": adv.match_type, "upstream_version": adv.upstream_version,
                        "rpm_version": adv.rpm_version, "upstream_fixed_version": adv.upstream_fixed_version,
                        "distro_fixed_version": adv.distro_fixed_version, "severity": adv.severity,
                        "cvss_score": adv.cvss_score, "epss_score": adv.epss_score,
                        "in_kev": adv.in_kev, "flags": adv.flags,
                        "notes": adv.notes, "agent_reasoning": adv.agent_reasoning,
                    },
                )
            conn.commit()

    def get_advisory(self, source_package: str, vuln_id: str) -> Advisory | None:
        with self._pool.connection() as conn:
            conn.row_factory = dict_row
            row = conn.execute(
                "SELECT * FROM advisories WHERE source_package = %s AND vuln_id = %s",
                (source_package, vuln_id),
            ).fetchone()
            if row:
                return _row_to_advisory(row)
        return None

    def list_advisories(
        self,
        source_package: str | None = None,
        status: str | None = None,
        limit: int = 100,
        offset: int = 0,
    ) -> list[Advisory]:
        conditions = []
        params: list[Any] = []

        if source_package:
            conditions.append("source_package = %s")
            params.append(source_package)
        if status:
            conditions.append("status = %s")
            params.append(status)

        where = " AND ".join(conditions) if conditions else "TRUE"
        params.extend([limit, offset])

        with self._pool.connection() as conn:
            conn.row_factory = dict_row
            rows = conn.execute(
                f"SELECT * FROM advisories WHERE {where} ORDER BY updated_at DESC LIMIT %s OFFSET %s",
                params,
            ).fetchall()
            return [_row_to_advisory(r) for r in rows]

    def count_advisories(self, source_package: str | None = None, status: str | None = None) -> int:
        conditions = []
        params: list[Any] = []

        if source_package:
            conditions.append("source_package = %s")
            params.append(source_package)
        if status:
            conditions.append("status = %s")
            params.append(status)

        where = " AND ".join(conditions) if conditions else "TRUE"

        with self._pool.connection() as conn:
            row = conn.execute(f"SELECT COUNT(*) FROM advisories WHERE {where}", params).fetchone()
            return row[0] if row else 0

    def review_advisory(
        self,
        source_package: str,
        vuln_id: str,
        status: str,
        reviewed_by: str,
        distro_fixed_version: str = "",
        notes: str = "",
    ) -> bool:
        with self._pool.connection() as conn:
            result = conn.execute(
                """
                UPDATE advisories
                SET status = %s, reviewed_by = %s, reviewed_at = now(),
                    distro_fixed_version = %s, notes = %s, updated_at = now()
                WHERE source_package = %s AND vuln_id = %s
                """,
                (status, reviewed_by, distro_fixed_version, notes, source_package, vuln_id),
            )
            return result.rowcount > 0

    def get_prior_decisions(self, source_package: str, limit: int = 10) -> list[Advisory]:
        with self._pool.connection() as conn:
            conn.row_factory = dict_row
            rows = conn.execute(
                """
                SELECT * FROM advisories
                WHERE source_package = %s AND status != 'detected'
                ORDER BY updated_at DESC LIMIT %s
                """,
                (source_package, limit),
            ).fetchall()
            return [_row_to_advisory(r) for r in rows]

    # --- Name mappings ---

    def get_mapping(self, rpm_name: str) -> NameMapping | None:
        with self._pool.connection() as conn:
            conn.row_factory = dict_row
            row = conn.execute(
                "SELECT * FROM name_mappings WHERE rpm_name = %s", (rpm_name.lower(),)
            ).fetchone()
            if row:
                return NameMapping(
                    rpm_name=row["rpm_name"],
                    vuln_names=row["vuln_names"],
                    source=row["source"],
                    confidence=row["confidence"],
                    reviewed=row["reviewed"],
                    agent_reasoning=row.get("agent_reasoning", ""),
                    usage_count=row.get("usage_count", 0),
                )
        return None

    def get_all_mappings(self) -> dict[str, list[str]]:
        with self._pool.connection() as conn:
            conn.row_factory = dict_row
            rows = conn.execute("SELECT rpm_name, vuln_names FROM name_mappings").fetchall()
            return {r["rpm_name"]: r["vuln_names"] for r in rows}

    def get_reverse_mappings(self) -> dict[str, list[str]]:
        """Build vuln_name → [rpm_names] reverse index from name_mappings."""
        forward = self.get_all_mappings()
        reverse: dict[str, list[str]] = {}
        for rpm_name, vuln_names in forward.items():
            for vn in vuln_names:
                reverse.setdefault(vn, []).append(rpm_name)
        return reverse

    def upsert_mapping(self, mapping: NameMapping) -> None:
        with self._pool.connection() as conn:
            conn.execute(
                """
                INSERT INTO name_mappings (rpm_name, vuln_names, source, confidence, reviewed, agent_reasoning, updated_at)
                VALUES (%s, %s, %s, %s, %s, %s, now())
                ON CONFLICT (rpm_name) DO UPDATE SET
                    vuln_names = EXCLUDED.vuln_names,
                    source = EXCLUDED.source,
                    confidence = EXCLUDED.confidence,
                    agent_reasoning = EXCLUDED.agent_reasoning,
                    updated_at = now()
                """,
                (
                    mapping.rpm_name.lower(),
                    mapping.vuln_names,
                    mapping.source,
                    mapping.confidence,
                    mapping.reviewed,
                    mapping.agent_reasoning,
                ),
            )

    def increment_mapping_usage(self, rpm_name: str, count: int = 1) -> None:
        with self._pool.connection() as conn:
            conn.execute(
                "UPDATE name_mappings SET usage_count = usage_count + %s WHERE rpm_name = %s",
                (count, rpm_name.lower()),
            )

    def review_mapping(self, rpm_name: str, reviewed: bool = True) -> bool:
        with self._pool.connection() as conn:
            result = conn.execute(
                "UPDATE name_mappings SET reviewed = %s, updated_at = now() WHERE rpm_name = %s",
                (reviewed, rpm_name.lower()),
            )
            return result.rowcount > 0

    # --- Match state ---

    def get_match_state(self, source_package: str) -> MatchState | None:
        with self._pool.connection() as conn:
            conn.row_factory = dict_row
            row = conn.execute(
                "SELECT * FROM match_state WHERE source_package = %s", (source_package,)
            ).fetchone()
            if row:
                return MatchState(
                    source_package=row["source_package"],
                    last_matched_at=row.get("last_matched_at"),
                    vuln_checkpoint=row.get("vuln_checkpoint", ""),
                    catalog_version=row.get("catalog_version", ""),
                )
        return None

    def upsert_match_state(self, state: MatchState) -> None:
        with self._pool.connection() as conn:
            conn.execute(
                """
                INSERT INTO match_state (source_package, last_matched_at, vuln_checkpoint, catalog_version)
                VALUES (%s, %s, %s, %s)
                ON CONFLICT (source_package) DO UPDATE SET
                    last_matched_at = EXCLUDED.last_matched_at,
                    vuln_checkpoint = EXCLUDED.vuln_checkpoint,
                    catalog_version = EXCLUDED.catalog_version
                """,
                (state.source_package, state.last_matched_at, state.vuln_checkpoint, state.catalog_version),
            )

    def ping(self) -> bool:
        try:
            with self._pool.connection() as conn:
                conn.execute("SELECT 1")
            return True
        except Exception:
            return False


def _row_to_advisory(row: dict) -> Advisory:
    return Advisory(
        id=str(row.get("id", "")),
        source_package=row["source_package"],
        vuln_id=row["vuln_id"],
        status=row["status"],
        confidence=row.get("confidence", "medium"),
        match_type=row.get("match_type", ""),
        upstream_version=row.get("upstream_version", ""),
        rpm_version=row.get("rpm_version", ""),
        upstream_fixed_version=row.get("upstream_fixed_version", ""),
        distro_fixed_version=row.get("distro_fixed_version", ""),
        severity=row.get("severity", ""),
        cvss_score=row.get("cvss_score", ""),
        epss_score=row.get("epss_score", 0.0) or 0.0,
        in_kev=row.get("in_kev", False),
        flags=row.get("flags", []),
        notes=row.get("notes", ""),
        agent_reasoning=row.get("agent_reasoning", ""),
        reviewed_by=row.get("reviewed_by", ""),
        reviewed_at=row.get("reviewed_at"),
        created_at=row.get("created_at"),
        updated_at=row.get("updated_at"),
    )
