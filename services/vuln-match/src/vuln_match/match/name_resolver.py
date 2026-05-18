"""Stage 1: Name resolution — map RPM source package names to vuln-ingest identities."""

from __future__ import annotations

import re
from dataclasses import dataclass, field

from .cpe_index import CpeIndex


@dataclass
class ResolvedName:
    rpm_name: str
    vuln_names: list[str]
    source: str  # direct|source-rpm|pattern|cpe|mapping


def parse_source_rpm(purl: str, name: str, version: str) -> tuple[str, str]:
    """Extract source RPM name and upstream version from PURL.

    Returns (source_name, upstream_version).
    """
    m = re.search(r"upstream=([^&]+)", purl)
    if not m:
        return name, version.split("-")[0]

    upstream = m.group(1)
    sm = re.match(r"^(.+?)-(\d.*)\.src\.rpm$", upstream)
    if not sm:
        return name, version.split("-")[0]

    return sm.group(1), sm.group(2).split("-")[0]


def _pattern_variants(name: str) -> tuple[set[str], set[str]]:
    """Generate candidate names via pattern transformations.

    Returns (confident_candidates, uncertain_candidates).
    Uncertain candidates (like lib-stripped names) need agent verification.
    """
    confident: set[str] = set()
    uncertain: set[str] = set()
    lower = name.lower()

    # Strip lib prefix — always uncertain, agent should verify
    # (libselinux→selinux could be kernel module, not userspace lib)
    if lower.startswith("lib") and len(lower) > 3:
        uncertain.add(lower[3:])
    if not lower.startswith("lib"):
        confident.add("lib" + lower)

    # Strip trailing version numbers: python3.14 → python
    stripped = re.sub(r"\d+(\.\d+)*$", "", lower)
    if stripped and stripped != lower:
        confident.add(stripped)
        confident.add(stripped.rstrip("."))

    # RPM naming: glib2 → glib
    stripped2 = re.sub(r"\d+$", "", lower)
    if stripped2 and stripped2 != lower:
        confident.add(stripped2)

    # Hyphen/underscore swap
    if "-" in lower:
        confident.add(lower.replace("-", "_"))
    if "_" in lower:
        confident.add(lower.replace("_", "-"))

    return confident, uncertain


def resolve_names(
    src_name: str,
    vuln_index_keys: set[str],
    cpe_index: CpeIndex,
    stored_mappings: dict[str, list[str]],
) -> ResolvedName | None:
    """Resolve an RPM source package name to vuln-ingest package name(s).

    Priority order:
    1. Stored mappings (agent-proposed or human-reviewed)
    2. Direct match
    3. Pattern-based variants
    4. CPE dictionary lookup

    Returns None if no resolution found — caller should escalate to agent.
    """
    lower = src_name.lower()

    # 1. Stored mappings (highest priority — learned from prior agent decisions)
    if lower in stored_mappings:
        matched = [n for n in stored_mappings[lower] if n in vuln_index_keys]
        if matched:
            return ResolvedName(rpm_name=src_name, vuln_names=matched, source="mapping")

    # 2. Direct match
    if lower in vuln_index_keys:
        return ResolvedName(rpm_name=src_name, vuln_names=[lower], source="direct")

    # 3. Pattern-based variants (confident ones first)
    confident, uncertain = _pattern_variants(lower)
    confident_matches = [c for c in confident if c in vuln_index_keys]
    if confident_matches:
        return ResolvedName(rpm_name=src_name, vuln_names=confident_matches, source="pattern")

    # 4. CPE dictionary lookup
    cpe_product = cpe_index.product_name(lower)
    if cpe_product and cpe_product in vuln_index_keys:
        return ResolvedName(rpm_name=src_name, vuln_names=[cpe_product], source="cpe")

    # Also try CPE on confident pattern variants
    for variant in confident:
        cpe_product = cpe_index.product_name(variant)
        if cpe_product and cpe_product in vuln_index_keys:
            return ResolvedName(rpm_name=src_name, vuln_names=[cpe_product], source="cpe")

    # 5. Uncertain patterns (lib-stripped) — route to agent for verification
    uncertain_matches = [c for c in uncertain if c in vuln_index_keys]
    if uncertain_matches:
        return ResolvedName(rpm_name=src_name, vuln_names=uncertain_matches, source="uncertain")

    return None
