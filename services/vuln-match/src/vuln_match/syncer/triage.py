"""Batch triage: send 'detected' advisories to agent for review.

Runs periodically (CronJob or manual). Picks up advisories with
status='detected', batches by source package, runs agent enrichment,
and updates status based on agent assessment.
"""

from __future__ import annotations

import logging

from ..agent.enricher import create_client, enrich_batch
from ..agent.tools import ToolExecutor
from ..config import Config
from ..db.pool import create_pool, run_migrations
from ..store.postgres import Advisory, AdvisoryStore, NameMapping

logger = logging.getLogger(__name__)


def run_triage(cfg: Config) -> None:
    run_migrations(cfg.matchdb_url)

    pool = create_pool(cfg.matchdb_url, min_size=1, max_size=2)
    store = AdvisoryStore(pool)

    if not cfg.agent_enabled or not cfg.vertex_project:
        logger.error("agent not configured — cannot triage")
        return

    client = create_client(cfg.vertex_project, cfg.vertex_region)
    tool_executor = ToolExecutor(
        rpms_repo_path=cfg.rpms_repo_path,
        vuln_api_url=cfg.vuln_api_url,
        advisory_store=store,
    )

    detected = store.list_advisories(status="detected", limit=500)
    if not detected:
        logger.info("no detected advisories to triage")
        return

    logger.info("triaging %d detected advisories", len(detected))

    # Group by source package
    by_package: dict[str, list[Advisory]] = {}
    for adv in detected:
        by_package.setdefault(adv.source_package, []).append(adv)

    total_reviewed = 0
    total_changed = 0

    for pkg_name, advisories in by_package.items():
        upstream_ver = advisories[0].upstream_version
        rpm_ver = advisories[0].rpm_version

        prior = store.get_prior_decisions(pkg_name)
        prior_dicts = [
            {"vuln_id": p.vuln_id, "status": p.status, "notes": p.notes}
            for p in prior
        ]

        # Build CVE details for agent
        cve_details = [
            {
                "cve_id": adv.vuln_id,
                "range_quality": "none" if "no-ranges" in adv.flags else "low",
                "flags": adv.flags,
                "fixed_version": adv.upstream_fixed_version,
                "notes": adv.notes,
            }
            for adv in advisories[:cfg.agent_batch_size]
        ]

        result = enrich_batch(
            client=client,
            package=pkg_name,
            upstream_version=upstream_ver,
            rpm_version=rpm_ver,
            cve_details=cve_details,
            tool_executor=tool_executor,
            prior_decisions=prior_dicts,
            model=cfg.agent_model,
        )

        assessment_map = {a.cve_id: a for a in result.assessments}
        for adv in advisories[:cfg.agent_batch_size]:
            assessment = assessment_map.get(adv.vuln_id)
            if assessment and assessment.status != adv.status:
                store.upsert_advisory(Advisory(
                    source_package=pkg_name,
                    vuln_id=adv.vuln_id,
                    status=assessment.status,
                    confidence=assessment.confidence,
                    match_type=adv.match_type,
                    upstream_version=upstream_ver,
                    rpm_version=rpm_ver,
                    upstream_fixed_version=adv.upstream_fixed_version,
                    severity=adv.severity,
                    cvss_score=adv.cvss_score,
                    flags=adv.flags,
                    agent_reasoning=assessment.reasoning,
                    notes=f"[agent-triage] {assessment.reasoning[:200]}",
                ))
                total_changed += 1
                logger.info("%s %s: %s → %s (%s)",
                            pkg_name, adv.vuln_id, adv.status,
                            assessment.status, assessment.reasoning[:80])

        # Store any new mappings agent proposed
        for mapping in result.proposed_mappings:
            existing = store.get_mapping(mapping["rpm_name"])
            if not existing:
                store.upsert_mapping(NameMapping(
                    rpm_name=mapping["rpm_name"],
                    vuln_names=[mapping["vuln_name"]],
                    source="agent",
                    confidence="medium",
                    agent_reasoning=f"Agent proposed during triage of {pkg_name}",
                ))

        total_reviewed += len(cve_details)
        logger.info("%s: reviewed %d, changed %d (tokens: %d/%d)",
                    pkg_name, len(cve_details), total_changed,
                    result.input_tokens, result.output_tokens)

    logger.info("triage complete: %d reviewed, %d changed", total_reviewed, total_changed)
    pool.close()
