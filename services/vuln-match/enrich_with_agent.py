#!/usr/bin/env python3
"""Enrich advisory data using an LLM agent to interpret CVE details.

Reads draft advisories and asks the agent to:
1. Verify if the CVE actually affects the given package version
2. Interpret complex version ranges (branch-specific, feature-dependent)
3. Identify obvious false positives (wrong OS, wrong branch, etc.)
4. Provide a confidence-adjusted status

Uses Vertex AI with Claude Haiku 4.5 via ADC.
"""

import json
import os
import sys
from pathlib import Path

import psycopg2

try:
    import yaml
except ImportError:
    yaml = None
    print("pip install pyyaml", file=sys.stderr)
    sys.exit(1)

try:
    import anthropic
except ImportError:
    print("pip install anthropic[vertex]", file=sys.stderr)
    sys.exit(1)


def main():
    vuln_url = os.getenv("VULN_DB", "host=localhost port=15434 dbname=vulndb user=vulndb password=vulndb")
    advisory_dir = os.getenv("ADVISORY_DIR", "advisories")
    max_per_package = int(os.getenv("MAX_PER_PACKAGE", "20"))

    vuln = psycopg2.connect(vuln_url)
    client = anthropic.AnthropicVertex(
        region=os.getenv("CLOUD_ML_REGION", "global"),
        project_id=os.getenv("GOOGLE_CLOUD_PROJECT", ""),
    )

    advisory_files = sorted(Path(advisory_dir).glob("*.advisories.yaml"))
    print(f"Found {len(advisory_files)} advisory files", file=sys.stderr)

    total_reviewed = 0
    total_changed = 0

    for filepath in advisory_files:
        with open(filepath) as f:
            pkg_data = yaml.safe_load(f)

        if not pkg_data or not pkg_data.get("advisories"):
            continue

        # Only process "detected" entries (needs review)
        detected = [a for a in pkg_data["advisories"] if a["status"] == "detected"]
        if not detected:
            continue

        # Prioritize: high CVSS first, limit per package
        detected.sort(key=lambda a: (
            -float(a.get("cvss_score", "0") or "0"),
            -float(a.get("epss_score", 0)),
        ))
        to_review = detected[:max_per_package]

        print(f"\n{filepath.stem}: {len(to_review)} CVEs to review", file=sys.stderr)

        # Gather CVE details from vuln-ingest
        cve_details = []
        for adv in to_review:
            detail = get_cve_detail(vuln, adv["id"])
            detail["current_status"] = adv["status"]
            detail["confidence"] = adv.get("confidence", "")
            detail["flags"] = adv.get("flags", [])
            detail["match_notes"] = adv.get("notes", "")
            cve_details.append(detail)

        # Ask agent to review
        results = review_with_agent(
            client,
            pkg_data["package"],
            pkg_data["upstream_version"],
            pkg_data["rpm_version"],
            cve_details,
        )

        # Apply agent results
        changes = 0
        advisory_map = {a["id"]: a for a in pkg_data["advisories"]}
        for result in results:
            cve_id = result.get("cve_id", "")
            if cve_id not in advisory_map:
                continue

            adv = advisory_map[cve_id]
            old_status = adv["status"]
            new_status = result.get("status", old_status)
            agent_reasoning = result.get("reasoning", "")

            if new_status != old_status:
                adv["status"] = new_status
                adv["notes"] = f"[agent] {agent_reasoning}"
                adv["confidence"] = result.get("confidence", adv.get("confidence", ""))
                changes += 1
                print(f"  {cve_id}: {old_status} → {new_status} ({agent_reasoning[:60]})", file=sys.stderr)

        if changes > 0:
            with open(filepath, "w") as f:
                yaml.dump(pkg_data, f, default_flow_style=False, sort_keys=False)

        total_reviewed += len(to_review)
        total_changed += changes

    print(f"\nTotal reviewed: {total_reviewed}, changed: {total_changed}", file=sys.stderr)
    vuln.close()


def get_cve_detail(vuln, cve_id):
    """Get full CVE details from vuln-ingest."""
    detail = {"cve_id": cve_id}

    with vuln.cursor() as cur:
        cur.execute("SELECT summary, details, severity FROM vulnerabilities WHERE id = %s", (cve_id,))
        row = cur.fetchone()
        if row:
            detail["summary"] = row[0] or ""
            detail["description"] = row[1] or ""
            sevs = row[2]
            if sevs:
                sevs = json.loads(sevs) if isinstance(sevs, str) else sevs
                if isinstance(sevs, list) and sevs:
                    detail["severity"] = sevs[0].get("severity", "")
                    detail["cvss"] = sevs[0].get("score", "")

        cur.execute("""
            SELECT source, package_name, vendor, version_ranges
            FROM affected_packages WHERE vuln_id = %s
            ORDER BY source
        """, (cve_id,))
        detail["affected_entries"] = []
        for source, pkg, vendor, vr in cur.fetchall():
            entry = {"source": source, "package": pkg, "vendor": vendor}
            if vr:
                entry["ranges"] = json.loads(vr) if isinstance(vr, str) else vr
            detail["affected_entries"].append(entry)

    return detail


def review_with_agent(client, package, upstream_ver, rpm_ver, cve_details):
    """Ask the agent to review CVE applicability."""

    prompt = f"""You are a Linux distribution security engineer reviewing CVEs for the Hummingbird distro.

## Package under review
- Source RPM: {package}
- Upstream version: {upstream_ver}
- RPM version: {rpm_ver}

## Your task
For each CVE below, determine if it ACTUALLY affects this package version.

Consider:
- Does the CVE description mention a specific OS, platform, or feature that doesn't apply?
- Is the version range applicable? Some ranges are for different major branches (e.g., OpenSSL 1.1.x vs 3.x)
- Does "introduced: X" without "fixed" mean all versions after X? Or is data missing?
- Is this a false positive from pattern matching (e.g., CVE for "php" PECL extension matched to "php" core)?

For each CVE, respond with:
- "affected" if the CVE genuinely applies to version {upstream_ver}
- "not-affected" if the CVE clearly doesn't apply (wrong branch, fixed in earlier version, wrong platform)
- "under-review" if you're uncertain and a human should check

## CVEs to review
{json.dumps(cve_details, indent=2, default=str)}

Respond ONLY with a JSON array:
[
  {{
    "cve_id": "CVE-XXXX-YYYY",
    "status": "affected|not-affected|under-review",
    "confidence": "high|medium|low",
    "reasoning": "Brief explanation"
  }}
]"""

    response = client.messages.create(
        model="claude-haiku-4-5@20251001",
        max_tokens=8192,
        messages=[{"role": "user", "content": prompt}],
    )

    text = response.content[0].text
    start = text.find("[")
    end = text.rfind("]") + 1
    if start >= 0 and end > start:
        try:
            return json.loads(text[start:end])
        except json.JSONDecodeError:
            # Try to salvage partial JSON
            try:
                # Find last complete object
                last_brace = text.rfind("}")
                if last_brace > start:
                    truncated = text[start:last_brace+1] + "]"
                    return json.loads(truncated)
            except json.JSONDecodeError:
                print(f"  WARNING: could not parse agent response", file=sys.stderr)
    return []


if __name__ == "__main__":
    main()
