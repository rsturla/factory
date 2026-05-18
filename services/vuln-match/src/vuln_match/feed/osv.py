"""OSV JSON feed generator for scanner consumption."""

from __future__ import annotations

from datetime import datetime, timezone
from typing import Any

from ..store.postgres import Advisory


def generate_osv_feed(advisories: list[Advisory]) -> dict:
    """Generate an OSV-format feed from advisories.

    Groups advisories by source package and creates one OSV entry
    per CVE with Hummingbird-specific advisory IDs.
    """
    entries = []
    seen = set()

    for adv in advisories:
        key = (adv.source_package, adv.vuln_id)
        if key in seen:
            continue
        seen.add(key)

        entry = _advisory_to_osv(adv)
        if entry:
            entries.append(entry)

    return {
        "schema_version": "1.6.0",
        "generated_at": datetime.now(timezone.utc).isoformat(),
        "entries": entries,
    }


def _advisory_to_osv(adv: Advisory) -> dict | None:
    """Convert a single advisory to OSV format."""
    events = [{"introduced": "0"}]

    if adv.status == "fixed" and adv.distro_fixed_version:
        events.append({"fixed": adv.distro_fixed_version})
    elif adv.status == "fixed" and adv.upstream_fixed_version:
        events.append({"fixed": adv.upstream_fixed_version})
    elif adv.status == "affected" and adv.upstream_fixed_version:
        events.append({"limit": adv.upstream_fixed_version})

    severity = []
    if adv.cvss_score:
        severity.append({
            "type": "CVSS_V3",
            "score": adv.cvss_score,
        })

    entry: dict[str, Any] = {
        "id": f"HUM-{adv.vuln_id}",
        "aliases": [adv.vuln_id],
        "summary": adv.notes[:500] if adv.notes else "",
        "affected": [
            {
                "package": {
                    "ecosystem": "Red Hat",
                    "name": adv.source_package,
                    "purl": f"pkg:rpm/redhat/{adv.source_package}",
                },
                "ranges": [
                    {
                        "type": "ECOSYSTEM",
                        "events": events,
                    }
                ],
            }
        ],
        "database_specific": {
            "status": adv.status,
            "confidence": adv.confidence,
            "upstream_version": adv.upstream_version,
            "rpm_version": adv.rpm_version,
        },
    }

    if severity:
        entry["severity"] = severity

    if adv.updated_at:
        entry["modified"] = adv.updated_at.isoformat() if hasattr(adv.updated_at, 'isoformat') else str(adv.updated_at)

    return entry
