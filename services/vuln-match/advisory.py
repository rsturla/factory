#!/usr/bin/env python3
"""Hummingbird Advisory Generator.

Matches CVEs against Hummingbird RPM packages and generates draft
advisory data per source package. Output is a directory of YAML files
ready for human review.

Flow:
1. Load all source RPMs from catalog (with upstream versions)
2. Match against vuln-ingest CVE data using name + version ranges
3. Enrich with CVSS, EPSS, KEV data from vuln-ingest
4. Generate per-package advisory YAML files
5. Compare against Grype for validation (optional)
"""

import json
import os
import re
import sys
import time
from collections import defaultdict
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path

import psycopg2
from packaging.version import Version, InvalidVersion

try:
    import yaml
except ImportError:
    yaml = None
    print("pip install pyyaml for YAML output (falling back to JSON)", file=sys.stderr)


@dataclass
class Advisory:
    vuln_id: str
    status: str  # "detected", "affected", "not-affected", "fixed", "under-review"
    detected_by: str = "vuln-match-proto"
    detected_at: str = ""
    match_type: str = ""
    confidence: str = ""
    upstream_fixed_version: str = ""
    distro_fixed_version: str = ""
    severity: str = ""
    cvss_score: str = ""
    epss_score: float = 0.0
    in_kev: bool = False
    flags: list = field(default_factory=list)
    notes: str = ""


@dataclass
class PackageAdvisory:
    package: str
    upstream_version: str
    rpm_version: str
    binary_packages: list
    advisories: list  # list of Advisory


def main():
    catalog_url = os.getenv("CATALOG_DB", "host=localhost port=15433 dbname=catalogdb user=catalogdb password=catalogdb")
    vuln_url = os.getenv("VULN_DB", "host=localhost port=15434 dbname=vulndb user=vulndb password=vulndb")
    scan_url = os.getenv("SCAN_DB", "host=localhost port=15432 dbname=scandb user=scandb password=scandb")
    output_dir = os.getenv("OUTPUT_DIR", "advisories")

    catalog = psycopg2.connect(catalog_url)
    vuln = psycopg2.connect(vuln_url)

    # Load CPE index for name resolution
    cpe_index = {}
    if os.path.exists("cpe_index.json"):
        with open("cpe_index.json") as f:
            cpe_index = json.load(f)

    # Load manual mappings
    name_mappings = {}
    if os.path.exists("mappings.json"):
        with open("mappings.json") as f:
            data = json.load(f)
        for m in data.get("mappings", []):
            if m.get("confidence") in ("high", "medium"):
                name_mappings[m["rpm_name"].lower()] = [n.lower() for n in m["proposed_names"]]

    # Step 1: Load source packages from catalog
    source_pkgs = load_source_packages(catalog)
    print(f"Source packages: {len(source_pkgs)}", file=sys.stderr)

    # Step 2: Build vuln index
    vuln_index = build_vuln_index(vuln)
    print(f"Vuln index: {len(vuln_index)} package names", file=sys.stderr)

    # Step 3: Match and generate advisories
    all_advisories = {}
    stats = {"matched": 0, "upstream_fixed": 0, "no_match": 0}

    for src_name, info in source_pkgs.items():
        upstream_ver = info["upstream_version"]
        rpm_ver = info["rpm_version"]
        binaries = info["binary_packages"]

        # Find CVEs for this package
        lookup_names = generate_lookups(src_name, cpe_index, name_mappings, set(vuln_index.keys()))
        if not lookup_names:
            stats["no_match"] += 1
            continue

        advisories = []
        seen_cves = set()

        for lookup_name in lookup_names:
            for vuln_id, cve_data in vuln_index.get(lookup_name, {}).items():
                if vuln_id in seen_cves:
                    continue
                seen_cves.add(vuln_id)

                ranges = cve_data["ranges"]
                sources = cve_data["sources"]

                if not ranges:
                    continue

                affected, fixed_ver, flags = is_affected(upstream_ver, ranges)

                adv = Advisory(
                    vuln_id=vuln_id,
                    status="pending",
                    detected_at=datetime.now(timezone.utc).isoformat(),
                    match_type=f"name:{lookup_name}",
                    flags=flags,
                )

                if affected:
                    adv.status = "detected"
                    adv.confidence = "low" if "no-fixed-version" in flags or "no-fix-available" in flags else "medium"
                    adv.upstream_fixed_version = fixed_ver
                    adv.notes = f"Upstream version {upstream_ver} is within affected range. Sources: {', '.join(sources)}"
                    stats["matched"] += 1
                else:
                    adv.status = "not-affected"
                    adv.confidence = "medium"
                    adv.upstream_fixed_version = fixed_ver
                    adv.notes = f"Upstream version {upstream_ver} >= fix version {fixed_ver}. Verify distro patch status."
                    stats["upstream_fixed"] += 1

                # Enrich with severity/CVSS/EPSS
                enrich_advisory(vuln, adv)

                advisories.append(adv)

        if advisories:
            all_advisories[src_name] = PackageAdvisory(
                package=src_name,
                upstream_version=upstream_ver,
                rpm_version=rpm_ver,
                binary_packages=binaries,
                advisories=sorted(advisories, key=lambda a: (
                    0 if a.status == "detected" else 1,
                    severity_rank(a.severity),
                    a.vuln_id,
                )),
            )

    print(f"\nStats: {stats}", file=sys.stderr)
    print(f"Packages with advisories: {len(all_advisories)}", file=sys.stderr)

    detected_count = sum(
        1 for pa in all_advisories.values()
        for a in pa.advisories if a.status == "detected"
    )
    not_affected_count = sum(
        1 for pa in all_advisories.values()
        for a in pa.advisories if a.status == "not-affected"
    )
    print(f"CVEs detected (needs review): {detected_count}", file=sys.stderr)
    print(f"CVEs not-affected (upstream fixed): {not_affected_count}", file=sys.stderr)

    # Step 4: Output advisory files
    write_advisories(all_advisories, output_dir)
    print(f"\nAdvisories written to {output_dir}/", file=sys.stderr)

    # Step 5: Validate against Grype (optional)
    try:
        scan = psycopg2.connect(scan_url)
        validate_against_grype(all_advisories, scan)
        scan.close()
    except Exception as e:
        print(f"Grype validation skipped: {e}", file=sys.stderr)

    catalog.close()
    vuln.close()


def load_source_packages(catalog):
    """Load source RPM packages with upstream version info."""
    with catalog.cursor() as cur:
        cur.execute("SELECT DISTINCT purl, name, version FROM packages WHERE type = 'rpm'")
        src_pkgs = {}
        for purl, name, version in cur.fetchall():
            m = re.search(r'upstream=(.+?)-(\d.*)\.src\.rpm', purl)
            if not m:
                continue
            src_name = m.group(1).lower()
            src_ver = m.group(2).split('-')[0]

            if src_name not in src_pkgs:
                src_pkgs[src_name] = {
                    "upstream_version": src_ver,
                    "rpm_version": version,
                    "binary_packages": [],
                }
            if name not in src_pkgs[src_name]["binary_packages"]:
                src_pkgs[src_name]["binary_packages"].append(name)

    return src_pkgs


def build_vuln_index(vuln):
    """Build package_name → {vuln_id → {ranges, sources}} index."""
    index = defaultdict(lambda: defaultdict(lambda: {"sources": [], "ranges": []}))
    with vuln.cursor() as cur:
        cur.execute("""
            SELECT ap.vuln_id, ap.source, lower(ap.package_name), ap.version_ranges
            FROM affected_packages ap
            WHERE ap.package_name != '' AND ap.package_name != 'n/a'
            AND ap.source IN ('nvd', 'anchore-nvd-overrides', 'cvelistv5')
        """)
        for vuln_id, source, pkg_name, ranges_json in cur.fetchall():
            entry = index[pkg_name][vuln_id]
            if source not in entry["sources"]:
                entry["sources"].append(source)
            if ranges_json:
                ranges = json.loads(ranges_json) if isinstance(ranges_json, str) else ranges_json
                entry["ranges"].extend(ranges)
    return index


def generate_lookups(src_name, cpe_index, mappings, vuln_keys):
    """Generate lookup names for a source RPM, only returning ones that exist in vuln index."""
    candidates = {src_name}

    # Manual mappings
    if src_name in mappings:
        candidates.update(mappings[src_name])

    # Strip lib prefix / add lib prefix
    if src_name.startswith('lib') and len(src_name) > 3:
        candidates.add(src_name[3:])
    else:
        candidates.add('lib' + src_name)

    # Strip version suffixes
    stripped = re.sub(r'\d+(\.\d+)*$', '', src_name).rstrip('.')
    if stripped and stripped != src_name:
        candidates.add(stripped)
    stripped2 = re.sub(r'\d+$', '', src_name)
    if stripped2 and stripped2 != src_name:
        candidates.add(stripped2)

    # Hyphen/underscore variants
    if '-' in src_name:
        candidates.add(src_name.replace('-', '_'))
    if '_' in src_name:
        candidates.add(src_name.replace('_', '-'))

    # CPE dictionary lookup
    for c in list(candidates):
        if c in cpe_index:
            _, cpe_product, _ = cpe_index[c]
            candidates.add(cpe_product.lower())

    return [c for c in candidates if c in vuln_keys]


def enrich_advisory(vuln, adv):
    """Add severity, CVSS, EPSS, KEV data."""
    with vuln.cursor() as cur:
        # Severity
        cur.execute("SELECT severity FROM vulnerabilities WHERE id = %s", (adv.vuln_id,))
        row = cur.fetchone()
        if row and row[0]:
            sevs = json.loads(row[0]) if isinstance(row[0], str) else row[0]
            if isinstance(sevs, list) and sevs:
                best = sevs[0]
                adv.severity = best.get("severity", best.get("type", ""))
                adv.cvss_score = best.get("score", "")

        # EPSS
        cur.execute("SELECT score FROM epss_scores WHERE cve_id = %s", (adv.vuln_id,))
        row = cur.fetchone()
        if row:
            adv.epss_score = float(row[0]) if row[0] else 0.0

        # KEV
        cur.execute("SELECT 1 FROM kev_entries WHERE cve_id = %s", (adv.vuln_id,))
        adv.in_kev = cur.fetchone() is not None


def is_affected(version, ranges):
    if not ranges:
        return False, "", []

    with_fix = [r for r in ranges if (r.get('fixed') and r['fixed'] != '*') or r.get('last_affected')]
    all_versions = [r for r in ranges if r.get('fixed') == '*']
    intro_only = [r for r in ranges if r.get('introduced') and not r.get('fixed') and not r.get('last_affected')]

    flags = []

    for r in with_fix:
        intro = r.get('introduced', '')
        fixed = r.get('fixed', '')
        last = r.get('last_affected', '')

        if intro and intro != '0' and compare_ver(version, intro) < 0:
            continue
        if fixed:
            if compare_ver(version, fixed) < 0:
                return True, fixed, flags
            continue
        if last:
            if compare_ver(version, last) <= 0:
                return True, "", flags

    if all_versions:
        flags.append("no-fix-available")
        return True, "", flags

    if with_fix:
        return False, with_fix[0].get("fixed", ""), []

    for r in intro_only:
        intro = r.get('introduced', '')
        if intro and compare_ver(version, intro) >= 0:
            flags.append("no-fixed-version")
            return True, "", flags

    return False, "", []


def compare_ver(a, b):
    try:
        return -1 if Version(a) < Version(b) else (1 if Version(a) > Version(b) else 0)
    except InvalidVersion:
        pa, pb = re.split(r'[.\-]', a), re.split(r'[.\-]', b)
        for i in range(max(len(pa), len(pb))):
            ca, cb = pa[i] if i < len(pa) else "0", pb[i] if i < len(pb) else "0"
            try:
                na, nb = int(ca), int(cb)
                if na != nb: return -1 if na < nb else 1
            except ValueError:
                if ca != cb: return -1 if ca < cb else 1
        return 0


def severity_rank(sev):
    ranks = {"critical": 0, "high": 1, "medium": 2, "low": 3}
    return ranks.get(sev.lower() if sev else "", 4)


def write_advisories(all_advisories, output_dir):
    """Write per-package advisory files."""
    Path(output_dir).mkdir(parents=True, exist_ok=True)

    summary = {"generated_at": datetime.now(timezone.utc).isoformat(), "packages": {}}

    for src_name, pa in sorted(all_advisories.items()):
        detected = [a for a in pa.advisories if a.status == "detected"]
        not_affected = [a for a in pa.advisories if a.status == "not-affected"]

        pkg_data = {
            "package": pa.package,
            "upstream_version": pa.upstream_version,
            "rpm_version": pa.rpm_version,
            "binary_packages": pa.binary_packages,
            "advisories": [],
        }

        for adv in pa.advisories:
            entry = {
                "id": adv.vuln_id,
                "status": adv.status,
                "confidence": adv.confidence,
                "detected_by": adv.detected_by,
                "detected_at": adv.detected_at,
            }
            if adv.severity:
                entry["severity"] = adv.severity
            if adv.cvss_score:
                entry["cvss_score"] = adv.cvss_score
            if adv.epss_score > 0:
                entry["epss_score"] = round(adv.epss_score, 5)
            if adv.in_kev:
                entry["in_kev"] = True
            if adv.upstream_fixed_version:
                entry["upstream_fixed_version"] = adv.upstream_fixed_version
            if adv.distro_fixed_version:
                entry["distro_fixed_version"] = adv.distro_fixed_version
            if adv.flags:
                entry["flags"] = adv.flags
            if adv.notes:
                entry["notes"] = adv.notes
            pkg_data["advisories"].append(entry)

        # Write per-package file
        filename = f"{src_name}.advisories"
        if yaml:
            filepath = Path(output_dir) / f"{filename}.yaml"
            with open(filepath, "w") as f:
                yaml.dump(pkg_data, f, default_flow_style=False, sort_keys=False)
        else:
            filepath = Path(output_dir) / f"{filename}.json"
            with open(filepath, "w") as f:
                json.dump(pkg_data, f, indent=2)

        summary["packages"][src_name] = {
            "detected": len(detected),
            "not_affected": len(not_affected),
            "total": len(pa.advisories),
        }

    # Write summary
    summary_path = Path(output_dir) / ("summary.yaml" if yaml else "summary.json")
    if yaml:
        with open(summary_path, "w") as f:
            yaml.dump(summary, f, default_flow_style=False, sort_keys=False)
    else:
        with open(summary_path, "w") as f:
            json.dump(summary, f, indent=2)


def validate_against_grype(all_advisories, scan):
    """Compare our advisory data against Grype findings."""
    with scan.cursor() as cur:
        cur.execute("""
            SELECT DISTINCT f.vuln_id, f.package_name
            FROM findings f JOIN scans s ON f.scan_id = s.id
            WHERE s.status = 'completed'
        """)
        grype_set = {(r[0], r[1]) for r in cur.fetchall()}

    our_detected = set()
    our_not_affected = set()
    for pa in all_advisories.values():
        for adv in pa.advisories:
            for bin_pkg in pa.binary_packages:
                if adv.status == "detected":
                    our_detected.add((adv.vuln_id, bin_pkg))
                elif adv.status == "not-affected":
                    our_not_affected.add((adv.vuln_id, bin_pkg))

    both = our_detected & grype_set
    only_us = our_detected - grype_set
    only_grype = grype_set - our_detected
    grype_we_say_fixed = our_not_affected & grype_set

    print(f"\n=== GRYPE VALIDATION ===", file=sys.stderr)
    print(f"We detected, Grype agrees:     {len(both)}", file=sys.stderr)
    print(f"We detected, Grype doesn't:    {len(only_us)} (potential false positives)", file=sys.stderr)
    print(f"Grype found, we didn't detect: {len(only_grype)}", file=sys.stderr)
    print(f"  of which we say 'not-affected': {len(grype_we_say_fixed)} (upstream fixed)", file=sys.stderr)
    print(f"  of which we have no data:       {len(only_grype) - len(grype_we_say_fixed)}", file=sys.stderr)

    if grype_set:
        recall = len(both) / len(grype_set) * 100
        print(f"Recall vs Grype: {recall:.1f}%", file=sys.stderr)


if __name__ == "__main__":
    main()
