"""Workqueue reconciler for CVE-to-RPM matching.

Key formats:
  cve:CVE-2026-9999  — match one CVE against all affected RPMs (common path)
  pkg:openssl        — match one package against all CVEs (bootstrap/new package)
"""

from __future__ import annotations

import json
import logging
import re
import time
from collections import defaultdict
from datetime import datetime, timedelta, timezone
from typing import Any, Callable

import psycopg
from psycopg.rows import dict_row

from factory_workqueue.reconciler import ProcessRequest, ProcessResponse, completed, reject, requeue_after

from ..agent.enricher import EnrichmentResult, create_client, enrich_batch
from ..agent.tools import ToolExecutor
from ..config import Config
from ..match.cpe_index import CpeIndex
from ..match.name_resolver import ResolvedName, parse_source_rpm, resolve_names
from ..match.version_matcher import (
    RangeQuality,
    classify_range_quality,
    has_suspicious_version_jump,
    is_affected,
    needs_agent,
)
from ..store.postgres import Advisory, AdvisoryStore, MatchState, NameMapping

logger = logging.getLogger(__name__)

CACHE_TTL = 300  # 5 minutes


def make_reconciler(cfg: Config, pool: Any):
    """Create the reconciler function with all dependencies wired up."""
    from ..db.pool import create_pool

    store = AdvisoryStore(pool)
    cpe_index = CpeIndex.load(cfg.cpe_index_path)

    catalog_pool = create_pool(cfg.catalogdb_url, min_size=1, max_size=2) if cfg.catalogdb_url else None
    vuln_pool = create_pool(cfg.vulndb_url, min_size=1, max_size=2) if cfg.vulndb_url else None

    agent_client = None
    if cfg.agent_enabled and cfg.vertex_project:
        agent_client = create_client(cfg.vertex_project, cfg.vertex_region)

    tool_executor = ToolExecutor(
        rpms_repo_path=cfg.rpms_repo_path,
        vuln_api_url=cfg.vuln_api_url,
        advisory_store=store,
    )

    _cache: dict[str, Any] = {
        "vuln_keys": set(), "vuln_keys_at": 0.0,
        "mappings": {}, "mappings_at": 0.0,
        "reverse": {}, "reverse_at": 0.0,
        "catalog_sources": {}, "catalog_sources_at": 0.0,
    }

    def _get_cached(key: str, loader: Callable, ttl: float = CACHE_TTL):
        now = time.monotonic()
        at_key = f"{key}_at"
        if now - _cache.get(at_key, 0.0) > ttl:
            result = loader()
            _cache[key] = result
            _cache[at_key] = now
        return _cache[key]

    def reconcile(req: ProcessRequest) -> ProcessResponse:
        return _reconcile(
            req=req, cfg=cfg, store=store, cpe_index=cpe_index,
            agent_client=agent_client, tool_executor=tool_executor,
            catalog_pool=catalog_pool, vuln_pool=vuln_pool,
            get_vuln_keys=lambda: _get_cached("vuln_keys", lambda: _get_vuln_index_keys(vuln_pool)),
            get_mappings=lambda: _get_cached("mappings", store.get_all_mappings),
            get_reverse=lambda: _get_cached("reverse", store.get_reverse_mappings),
            get_catalog_sources=lambda: _get_cached("catalog_sources", lambda: _get_all_source_packages(catalog_pool)),
        )

    return reconcile


def _reconcile(
    req: ProcessRequest,
    cfg: Config,
    store: AdvisoryStore,
    cpe_index: CpeIndex,
    agent_client: Any | None,
    tool_executor: ToolExecutor,
    catalog_pool: Any | None = None,
    vuln_pool: Any | None = None,
    get_vuln_keys: Callable | None = None,
    get_mappings: Callable | None = None,
    get_reverse: Callable | None = None,
    get_catalog_sources: Callable | None = None,
) -> ProcessResponse:
    key = req.key

    if key.startswith("cve:"):
        cve_id = key[4:]
        return _reconcile_cve(
            cve_id=cve_id, cfg=cfg, store=store, cpe_index=cpe_index,
            agent_client=agent_client, tool_executor=tool_executor,
            catalog_pool=catalog_pool, vuln_pool=vuln_pool,
            get_reverse=get_reverse, get_catalog_sources=get_catalog_sources,
        )
    elif key.startswith("pkg:"):
        src_package = key[4:]
        return _reconcile_package(
            src_package=src_package, cfg=cfg, store=store, cpe_index=cpe_index,
            agent_client=agent_client, tool_executor=tool_executor,
            catalog_pool=catalog_pool, vuln_pool=vuln_pool,
            get_vuln_keys=get_vuln_keys, get_mappings=get_mappings,
        )
    else:
        logger.warning("unknown key format: %s", key)
        return reject(f"unknown key format, expected cve: or pkg: prefix")


# ---------------------------------------------------------------------------
# CVE-centric path: 1 CVE → find all affected RPMs → version match
# ---------------------------------------------------------------------------

def _reconcile_cve(
    cve_id: str,
    cfg: Config,
    store: AdvisoryStore,
    cpe_index: CpeIndex,
    agent_client: Any | None,
    tool_executor: ToolExecutor,
    catalog_pool: Any | None,
    vuln_pool: Any | None,
    get_reverse: Callable | None,
    get_catalog_sources: Callable | None,
) -> ProcessResponse:
    logger.info("matching CVE %s", cve_id)

    # 1. Get CVE details from vulndb
    try:
        cve_data = _get_cve_details(vuln_pool, cve_id)
    except Exception as e:
        logger.error("vulndb unavailable for %s: %s", cve_id, type(e).__name__)
        return requeue_after(timedelta(minutes=2))

    if not cve_data:
        logger.warning("CVE %s not found in vulndb", cve_id)
        return reject(f"CVE {cve_id} not found")

    affected_names = cve_data["affected_names"]  # vuln package names from affected_packages
    ranges = cve_data["ranges"]
    severity = cve_data.get("severity", "")
    cvss_score = cve_data.get("cvss_score", "")

    if not affected_names:
        logger.info("CVE %s has no affected package names", cve_id)
        return completed()

    # 2. Find which RPM source packages map to these vuln names
    try:
        reverse_mappings = get_reverse() if get_reverse else store.get_reverse_mappings()
        catalog_sources = get_catalog_sources() if get_catalog_sources else {}
    except Exception as e:
        logger.error("failed loading reverse mappings: %s", type(e).__name__)
        return requeue_after(timedelta(minutes=2))

    matched_rpms = _find_affected_rpms(affected_names, reverse_mappings, catalog_sources, cpe_index)

    if not matched_rpms:
        logger.debug("CVE %s: no RPM matches for names %s", cve_id, affected_names)
        return completed()

    # 3. Version match against each RPM (vendor-filtered ranges)
    ranges_by_vendor = cve_data.get("ranges_by_vendor", {})
    vendors_by_name = cve_data.get("vendors_by_name", {})
    advisories = []

    for rpm_name, rpm_info in matched_rpms.items():
        upstream_ver = rpm_info["upstream_version"]
        rpm_ver = rpm_info["rpm_version"]
        match_source = rpm_info["match_source"]
        matched_vuln_name = rpm_info.get("matched_vuln_name", "")

        # Pick vendor-specific ranges when multiple vendors exist
        rpm_ranges = _select_vendor_ranges(
            rpm_name, matched_vuln_name, ranges, ranges_by_vendor, vendors_by_name,
        )
        quality = classify_range_quality(rpm_ranges)
        match_result = is_affected(upstream_ver, rpm_ranges)

        if not match_result.affected:
            # "no-ranges" = CVE exists but no version data — unknown, not safe to dismiss
            if "no-ranges" in match_result.flags:
                advisories.append(Advisory(
                    source_package=rpm_name, vuln_id=cve_id, status="detected",
                    confidence="low", match_type=match_source,
                    upstream_version=upstream_ver, rpm_version=rpm_ver,
                    severity=severity, cvss_score=cvss_score,
                    flags=match_result.flags,
                    notes="CVE matched by name but no version range data — needs review",
                ))
                continue

            fixed_ver = match_result.fixed_version
            if not fixed_ver:
                for r in rpm_ranges:
                    if r.get("fixed") and r["fixed"] != "*":
                        fixed_ver = r["fixed"]
                        break

            advisories.append(Advisory(
                source_package=rpm_name, vuln_id=cve_id, status="not-affected",
                confidence="high" if quality == RangeQuality.HIGH else "medium",
                match_type=match_source, upstream_version=upstream_ver, rpm_version=rpm_ver,
                upstream_fixed_version=fixed_ver, severity=severity, cvss_score=cvss_score,
                notes=f"Version {upstream_ver} >= fix {fixed_ver}" if fixed_ver else "Not in affected range",
            ))
        else:
            mapping_exists = match_source in ("mapping", "direct", "cpe", "pattern")
            if needs_agent(mapping_exists, quality):
                advisories.append(Advisory(
                    source_package=rpm_name, vuln_id=cve_id, status="detected",
                    confidence="low", match_type=match_source,
                    upstream_version=upstream_ver, rpm_version=rpm_ver,
                    upstream_fixed_version=match_result.fixed_version,
                    severity=severity, cvss_score=cvss_score,
                    flags=match_result.flags,
                    notes="Needs agent review — low-quality range data",
                ))
            else:
                confidence = "high" if quality == RangeQuality.HIGH else "medium"
                status = "affected" if confidence == "high" else "detected"
                flags = list(match_result.flags)

                if has_suspicious_version_jump(upstream_ver, match_result.fixed_version):
                    status = "detected"
                    confidence = "low"
                    flags.append("suspicious-version-jump")

                advisories.append(Advisory(
                    source_package=rpm_name, vuln_id=cve_id,
                    status=status, confidence=confidence, match_type=match_source,
                    upstream_version=upstream_ver, rpm_version=rpm_ver,
                    upstream_fixed_version=match_result.fixed_version,
                    severity=severity, cvss_score=cvss_score,
                    flags=flags,
                ))

    store.upsert_advisories(advisories)

    # Increment usage counts for mappings used
    mapping_rpms = {rpm for rpm, info in matched_rpms.items() if info["match_source"] == "mapping"}
    for rpm_name in mapping_rpms:
        store.increment_mapping_usage(rpm_name)

    affected_count = sum(1 for a in advisories if a.status in ("affected", "detected"))
    logger.info("CVE %s: %d RPMs checked, %d affected, %d not-affected",
                cve_id, len(advisories), affected_count, len(advisories) - affected_count)
    return completed()


def _find_affected_rpms(
    vuln_names: list[str],
    reverse_mappings: dict[str, list[str]],
    catalog_sources: dict[str, dict],
    cpe_index: CpeIndex,
) -> dict[str, dict]:
    """Find RPM source packages affected by a CVE's package names.

    Returns {rpm_name: {upstream_version, rpm_version, match_source, matched_vuln_name}}.
    """
    result: dict[str, dict] = {}

    for vuln_name in vuln_names:
        vn_lower = vuln_name.lower()

        # 1. Reverse mapping lookup
        for rpm_name in reverse_mappings.get(vn_lower, []):
            if rpm_name in catalog_sources and rpm_name not in result:
                info = catalog_sources[rpm_name]
                result[rpm_name] = {**info, "match_source": "mapping", "matched_vuln_name": vn_lower}

        # 2. Direct name match
        if vn_lower in catalog_sources and vn_lower not in result:
            result[vn_lower] = {**catalog_sources[vn_lower], "match_source": "direct", "matched_vuln_name": vn_lower}

        # 3. Pattern reverse
        for rpm_name, info in catalog_sources.items():
            if rpm_name in result:
                continue
            stripped = re.sub(r"\d+(\.\d+)*$", "", rpm_name.lower()).rstrip(".")
            if stripped == vn_lower and stripped != rpm_name.lower():
                result[rpm_name] = {**info, "match_source": "pattern", "matched_vuln_name": vn_lower}

    return result


def _vendor_matches_rpm(vendor: str, rpm_name: str, vuln_name: str) -> bool:
    """Check if a vendor plausibly matches an RPM package."""
    if not vendor:
        return True
    v = vendor.lower()
    rpm = rpm_name.lower()
    vn = vuln_name.lower()

    # Exact or containment match (e.g., "getcomposer" contains "composer")
    if rpm == v or vn == v:
        return True
    if rpm in v or v in rpm:
        # Require word-boundary-like match: "uuid" in "uuidjs" is NOT a match,
        # but "composer" in "getcomposer" IS
        if v.startswith(rpm) or v.endswith(rpm) or rpm.startswith(v) or rpm.endswith(v):
            return True
    if vn in v or v in vn:
        if v.startswith(vn) or v.endswith(vn) or vn.startswith(v) or vn.endswith(v):
            return True

    # Known ecosystem vendor patterns that indicate non-RPM packages
    non_rpm_vendors = {"npm", "uuidjs", "tagdiv", "org."}
    for marker in non_rpm_vendors:
        if marker in v and marker not in rpm:
            return False

    return True


def _select_vendor_ranges(
    rpm_name: str,
    matched_vuln_name: str,
    all_ranges: list[dict],
    ranges_by_vendor: dict[str, list[dict]],
    vendors_by_name: dict[str, set[str]],
) -> list[dict]:
    """Select the best vendor's ranges for an RPM.

    Filters out ranges from vendors that clearly don't match the RPM
    (e.g., npm 'uuidjs' for RPM 'uuid', or 'tagdiv' for RPM 'composer').
    """
    vendors = vendors_by_name.get(matched_vuln_name, set())
    if not vendors or vendors == {""}:
        return all_ranges

    # Filter to compatible vendors
    compatible = {v for v in vendors if _vendor_matches_rpm(v, rpm_name, matched_vuln_name)}

    if not compatible:
        logger.info("no compatible vendor for %s among %s — skipping all ranges", rpm_name, vendors)
        return []

    if compatible == vendors:
        return all_ranges

    # Build filtered ranges from compatible vendors only
    filtered = []
    for v in compatible:
        filtered.extend(ranges_by_vendor.get(v, []))

    if filtered:
        logger.debug("vendor filter: %s → vendors %s (from %s)", rpm_name, compatible, vendors)
        return filtered

    return all_ranges

    return result


# ---------------------------------------------------------------------------
# Package-centric path: 1 package → find all CVEs (bootstrap/new package)
# ---------------------------------------------------------------------------

def _reconcile_package(
    src_package: str,
    cfg: Config,
    store: AdvisoryStore,
    cpe_index: CpeIndex,
    agent_client: Any | None,
    tool_executor: ToolExecutor,
    catalog_pool: Any | None,
    vuln_pool: Any | None,
    get_vuln_keys: Callable | None,
    get_mappings: Callable | None,
) -> ProcessResponse:
    logger.info("matching package %s", src_package)

    try:
        pkg_info = _get_package_info(catalog_pool, src_package)
    except Exception as e:
        logger.error("catalogdb unavailable for %s: %s", src_package, type(e).__name__)
        return requeue_after(timedelta(minutes=2))

    if not pkg_info:
        return reject(f"no packages found for {src_package}")

    upstream_version = pkg_info["upstream_version"]
    rpm_version = pkg_info["rpm_version"]

    try:
        stored_mappings = get_mappings() if get_mappings else store.get_all_mappings()
        vuln_index_keys = get_vuln_keys() if get_vuln_keys else set()
    except Exception as e:
        logger.error("failed loading mappings/vuln keys: %s", type(e).__name__)
        return requeue_after(timedelta(minutes=2))

    resolved = resolve_names(src_package, vuln_index_keys, cpe_index, stored_mappings)

    if resolved and resolved.source == "mapping":
        store.increment_mapping_usage(src_package)

    # Store the resolution as a mapping for future CVE-centric lookups
    if resolved and resolved.source != "mapping":
        existing = store.get_mapping(src_package)
        if not existing:
            store.upsert_mapping(NameMapping(
                rpm_name=src_package, vuln_names=resolved.vuln_names,
                source=resolved.source, confidence="high" if resolved.source == "direct" else "medium",
            ))
            logger.info("stored mapping %s → %s (source=%s)", src_package, resolved.vuln_names, resolved.source)

    # Query vulndb for CVEs
    cves = {}
    if resolved:
        try:
            for vuln_name in resolved.vuln_names:
                found = _query_vulns_by_package(vuln_pool, vuln_name, rpm_name=src_package)
                for vid, data in found.items():
                    if vid not in cves:
                        cves[vid] = data
                    else:
                        cves[vid]["ranges"].extend(data["ranges"])
        except Exception as e:
            logger.error("vulndb query failed for %s: %s", src_package, type(e).__name__)
            return requeue_after(timedelta(minutes=2))

    # Version matching
    advisories = []
    agent_candidates = []

    for vuln_id, cve_data in cves.items():
        ranges = cve_data.get("ranges", [])
        quality = classify_range_quality(ranges)
        match_result = is_affected(upstream_version, ranges)

        if not match_result.affected:
            if "no-ranges" in match_result.flags:
                advisories.append(Advisory(
                    source_package=src_package, vuln_id=vuln_id, status="detected",
                    confidence="low", match_type=resolved.source if resolved else "unknown",
                    upstream_version=upstream_version, rpm_version=rpm_version,
                    severity=cve_data.get("severity", ""), cvss_score=cve_data.get("cvss_score", ""),
                    flags=match_result.flags,
                    notes="CVE matched by name but no version range data — needs review",
                ))
                continue

            fixed_ver = match_result.fixed_version
            if not fixed_ver:
                for r in ranges:
                    if r.get("fixed") and r["fixed"] != "*":
                        fixed_ver = r["fixed"]
                        break

            advisories.append(Advisory(
                source_package=src_package, vuln_id=vuln_id, status="not-affected",
                confidence="high" if quality == RangeQuality.HIGH else "medium",
                match_type=resolved.source if resolved else "unknown",
                upstream_version=upstream_version, rpm_version=rpm_version,
                upstream_fixed_version=fixed_ver,
                severity=cve_data.get("severity", ""), cvss_score=cve_data.get("cvss_score", ""),
                notes=f"Version {upstream_version} >= fix {fixed_ver}" if fixed_ver else "Not in affected range",
            ))
            continue

        mapping_exists = resolved is not None and resolved.source != "uncertain"
        if needs_agent(mapping_exists, quality):
            agent_candidates.append({
                "vuln_id": vuln_id, "quality": quality.value,
                "flags": match_result.flags, "fixed_version": match_result.fixed_version,
                "cve_data": cve_data,
            })
        else:
            confidence = "high" if quality == RangeQuality.HIGH else "medium"
            status = "affected" if confidence == "high" else "detected"
            flags = list(match_result.flags)

            if has_suspicious_version_jump(upstream_version, match_result.fixed_version):
                status = "detected"
                confidence = "low"
                flags.append("suspicious-version-jump")

            advisories.append(Advisory(
                source_package=src_package, vuln_id=vuln_id,
                status=status, confidence=confidence,
                match_type=resolved.source if resolved else "unknown",
                upstream_version=upstream_version, rpm_version=rpm_version,
                upstream_fixed_version=match_result.fixed_version,
                severity=cve_data.get("severity", ""), cvss_score=cve_data.get("cvss_score", ""),
                flags=flags,
            ))

    store.upsert_advisories(advisories)

    # Agent enrichment
    if agent_candidates and agent_client and cfg.agent_enabled:
        _run_agent_enrichment(
            src_package=src_package, upstream_version=upstream_version, rpm_version=rpm_version,
            candidates=agent_candidates, resolved=resolved, store=store,
            agent_client=agent_client, tool_executor=tool_executor, cfg=cfg,
        )
    elif agent_candidates:
        for c in agent_candidates:
            store.upsert_advisory(Advisory(
                source_package=src_package, vuln_id=c["vuln_id"], status="detected",
                confidence="low", match_type=resolved.source if resolved else "unknown",
                upstream_version=upstream_version, rpm_version=rpm_version,
                upstream_fixed_version=c["fixed_version"], flags=c["flags"],
                notes="Agent not available — needs manual review",
            ))

    store.upsert_match_state(MatchState(
        source_package=src_package,
        last_matched_at=datetime.now(timezone.utc),
        catalog_version=rpm_version,
    ))

    logger.info("%s: %d advisories + %d agent candidates", src_package, len(advisories), len(agent_candidates))
    return completed()


# ---------------------------------------------------------------------------
# Agent enrichment (shared by both paths)
# ---------------------------------------------------------------------------

def _run_agent_enrichment(
    src_package: str, upstream_version: str, rpm_version: str,
    candidates: list[dict], resolved: ResolvedName | None,
    store: AdvisoryStore, agent_client: Any, tool_executor: ToolExecutor, cfg: Config,
) -> None:
    prior = store.get_prior_decisions(src_package)
    prior_dicts = [{"vuln_id": p.vuln_id, "status": p.status, "notes": p.notes} for p in prior]

    cve_details = [
        {"cve_id": c["vuln_id"], "range_quality": c["quality"],
         "flags": c["flags"], "fixed_version": c["fixed_version"]}
        for c in candidates[:cfg.agent_batch_size]
    ]

    result = enrich_batch(
        client=agent_client, package=src_package,
        upstream_version=upstream_version, rpm_version=rpm_version,
        cve_details=cve_details, tool_executor=tool_executor,
        prior_decisions=prior_dicts, model=cfg.agent_model,
    )

    logger.info("%s: agent reviewed %d CVEs, %d assessments (tokens: %d/%d)",
                src_package, len(cve_details), len(result.assessments),
                result.input_tokens, result.output_tokens)

    assessment_map = {a.cve_id: a for a in result.assessments}
    for c in candidates[:cfg.agent_batch_size]:
        vid = c["vuln_id"]
        assessment = assessment_map.get(vid)
        if assessment:
            store.upsert_advisory(Advisory(
                source_package=src_package, vuln_id=vid,
                status=assessment.status, confidence=assessment.confidence,
                match_type=resolved.source if resolved else "unknown",
                upstream_version=upstream_version, rpm_version=rpm_version,
                upstream_fixed_version=c["fixed_version"], flags=c["flags"],
                agent_reasoning=assessment.reasoning,
                notes=f"[agent] {assessment.reasoning[:200]}",
            ))
        else:
            store.upsert_advisory(Advisory(
                source_package=src_package, vuln_id=vid, status="under-review",
                confidence="low", match_type=resolved.source if resolved else "unknown",
                upstream_version=upstream_version, rpm_version=rpm_version,
                upstream_fixed_version=c["fixed_version"], flags=c["flags"],
                notes="Agent did not return assessment",
            ))

    for mapping in result.proposed_mappings:
        existing = store.get_mapping(mapping["rpm_name"])
        if not existing:
            store.upsert_mapping(NameMapping(
                rpm_name=mapping["rpm_name"], vuln_names=[mapping["vuln_name"]],
                source="agent", confidence="medium",
                agent_reasoning=f"Agent proposed during {src_package} review",
            ))

    for c in candidates[cfg.agent_batch_size:]:
        store.upsert_advisory(Advisory(
            source_package=src_package, vuln_id=c["vuln_id"], status="detected",
            confidence="low", match_type=resolved.source if resolved else "unknown",
            upstream_version=upstream_version, rpm_version=rpm_version,
            upstream_fixed_version=c["fixed_version"], flags=c["flags"],
            notes="Exceeded agent batch size — needs review",
        ))


# ---------------------------------------------------------------------------
# DB query helpers
# ---------------------------------------------------------------------------

def _get_cve_details(pool: Any, cve_id: str) -> dict | None:
    """Get CVE details + affected package names with per-vendor ranges."""
    if pool is None:
        return None

    with pool.connection() as conn:
        conn.row_factory = dict_row

        vuln = conn.execute(
            "SELECT id, summary, severity FROM vulnerabilities WHERE id = %s", (cve_id,)
        ).fetchone()
        if not vuln:
            return None

        rows = conn.execute(
            "SELECT package_name, vendor, version_ranges FROM affected_packages WHERE vuln_id = %s",
            (cve_id,),
        ).fetchall()

        affected_names = []
        all_ranges = []
        vendors_by_name: dict[str, set[str]] = {}
        ranges_by_vendor: dict[str, list[dict]] = {}

        for row in rows:
            pkg = (row.get("package_name") or "").lower()
            vendor = (row.get("vendor") or "").lower()
            if pkg:
                affected_names.append(pkg)
                vendors_by_name.setdefault(pkg, set()).add(vendor)

            vr = row.get("version_ranges")
            if vr:
                ranges = json.loads(vr) if isinstance(vr, str) else vr
                if isinstance(ranges, list):
                    all_ranges.extend(ranges)
                    ranges_by_vendor.setdefault(vendor, []).extend(ranges)

        severity = ""
        cvss = ""
        if vuln.get("severity"):
            sevs = vuln["severity"]
            if isinstance(sevs, str):
                sevs = json.loads(sevs)
            if isinstance(sevs, list) and sevs:
                severity = sevs[0].get("severity", "")
                cvss = str(sevs[0].get("score", ""))

        return {
            "cve_id": cve_id,
            "affected_names": list(set(affected_names)),
            "ranges": all_ranges,
            "ranges_by_vendor": ranges_by_vendor,
            "vendors_by_name": vendors_by_name,
            "severity": severity,
            "cvss_score": cvss,
            "summary": vuln.get("summary", ""),
        }


def _get_all_source_packages(pool: Any) -> dict[str, dict]:
    """Get all source packages from catalogdb with upstream versions.

    Returns {source_name: {upstream_version, rpm_version}}.
    """
    if pool is None:
        return {}

    with pool.connection() as conn:
        conn.row_factory = dict_row
        rows = conn.execute(
            "SELECT purl, name, version FROM packages WHERE type = 'rpm'"
        ).fetchall()

        result: dict[str, dict] = {}
        for row in rows:
            src_name, upstream_ver = parse_source_rpm(row["purl"], row["name"], row["version"])
            if src_name not in result:
                result[src_name] = {"upstream_version": upstream_ver, "rpm_version": row["version"]}
        return result


def _get_package_info(pool: Any, src_package: str) -> dict | None:
    """Query catalogdb for a single source package."""
    if pool is None:
        return None

    with pool.connection() as conn:
        conn.row_factory = dict_row
        row = conn.execute(
            """
            SELECT purl, name, version FROM packages
            WHERE type = 'rpm' AND (name = %s OR purl LIKE %s)
            ORDER BY name LIMIT 1
            """,
            (src_package, f"%upstream={src_package}-%"),
        ).fetchone()

        if not row:
            return None

        src_name, upstream_ver = parse_source_rpm(row["purl"], row["name"], row["version"])
        return {"source_name": src_name, "upstream_version": upstream_ver, "rpm_version": row["version"]}


def _get_vuln_index_keys(pool: Any) -> set[str]:
    """Get all distinct package names from vuln-ingest."""
    if pool is None:
        return set()

    with pool.connection() as conn:
        rows = conn.execute(
            "SELECT DISTINCT lower(package_name) FROM affected_packages WHERE package_name != ''"
        ).fetchall()
        return {r[0] for r in rows}


def _query_vulns_by_package(pool: Any, package_name: str, rpm_name: str = "") -> dict:
    """Query vuln-ingest for CVEs affecting a package name.

    When multiple vendors share the same package name, filters to the
    vendor that best matches the RPM name.
    """
    if pool is None:
        return {}

    with pool.connection() as conn:
        conn.row_factory = dict_row
        rows = conn.execute(
            """
            SELECT ap.vuln_id, ap.vendor, ap.version_ranges, v.severity, v.summary
            FROM affected_packages ap
            LEFT JOIN vulnerabilities v ON v.id = ap.vuln_id
            WHERE lower(ap.package_name) = lower(%s)
            """,
            (package_name,),
        ).fetchall()

        # Group by CVE, track vendors per CVE
        cve_vendors: dict[str, set[str]] = {}
        for row in rows:
            vid = row["vuln_id"]
            vendor = (row.get("vendor") or "").lower()
            cve_vendors.setdefault(vid, set()).add(vendor)

        result: dict = {}
        for row in rows:
            vid = row["vuln_id"]
            vendor = (row.get("vendor") or "").lower()

            # Skip entries from wrong vendor when there are multiple
            vendors = cve_vendors.get(vid, set())
            if len(vendors) > 1 and rpm_name:
                rpm_lower = rpm_name.lower()
                pkg_lower = package_name.lower()
                is_matching_vendor = (rpm_lower in vendor or vendor in rpm_lower
                                     or pkg_lower in vendor or vendor in pkg_lower)
                has_matching_vendor = any(
                    rpm_lower in v or v in rpm_lower or pkg_lower in v or v in pkg_lower
                    for v in vendors if v
                )
                if has_matching_vendor and not is_matching_vendor:
                    continue

            if vid not in result:
                severity = ""
                cvss = ""
                if row.get("severity"):
                    sevs = row["severity"]
                    if isinstance(sevs, str):
                        sevs = json.loads(sevs)
                    if isinstance(sevs, list) and sevs:
                        severity = sevs[0].get("severity", "")
                        cvss = str(sevs[0].get("score", ""))
                result[vid] = {"ranges": [], "severity": severity, "cvss_score": cvss}

            vr = row.get("version_ranges")
            if vr:
                ranges = json.loads(vr) if isinstance(vr, str) else vr
                if isinstance(ranges, list):
                    result[vid]["ranges"].extend(ranges)
        return result
