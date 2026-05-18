#!/usr/bin/env python3
"""Use an LLM to propose RPM-to-CVE package name mappings.

Uses Vertex AI with Haiku 4.5 via ADC. Provides the agent with:
- The RPM name and description (from SRPM)
- Similar names in the vuln DB
- Example CVEs that reference similar packages
- The actual vuln DB package name list to pick from
"""

import json
import os
import re
import subprocess
import sys
from collections import defaultdict

import psycopg2


def get_unmapped_names():
    """Find source RPM names with enriched context for the agent."""
    catalog = psycopg2.connect("host=localhost port=15433 dbname=catalogdb user=catalogdb password=catalogdb")
    vuln = psycopg2.connect("host=localhost port=15434 dbname=vulndb user=vulndb password=vulndb")

    with catalog.cursor() as cur:
        cur.execute("SELECT DISTINCT name, purl FROM packages WHERE type = 'rpm'")
        src_info = {}
        for name, purl in cur.fetchall():
            m = re.search(r'upstream=(.+?)-(\d.*)\.src\.rpm', purl)
            if m:
                src_name = m.group(1).lower()
                if src_name not in src_info:
                    src_info[src_name] = {"purl": purl, "binary_names": []}
                src_info[src_name]["binary_names"].append(name)

    unmapped = []
    for src_name, info in sorted(src_info.items()):
        # Check if already mapped
        variants = {src_name}
        stripped = re.sub(r'\d+(\.\d+)*$', '', src_name).rstrip('.')
        if stripped and stripped != src_name:
            variants.add(stripped)
        if src_name.startswith('lib'):
            variants.add(src_name[3:])

        found = False
        for v in variants:
            with vuln.cursor() as cur:
                cur.execute("SELECT 1 FROM affected_packages WHERE lower(package_name) = %s LIMIT 1", (v,))
                if cur.fetchone():
                    found = True
                    break

        if found:
            continue

        # Enrich with context
        entry = {"rpm_name": src_name, "binary_packages": list(set(info["binary_names"]))[:5]}

        # Similar names in vuln DB
        with vuln.cursor() as cur:
            cur.execute("""
                SELECT DISTINCT lower(package_name), count(DISTINCT vuln_id) as cves
                FROM affected_packages
                WHERE lower(package_name) LIKE %s AND package_name != ''
                GROUP BY lower(package_name)
                ORDER BY cves DESC LIMIT 5
            """, (f"%{src_name.split('-')[0]}%",))
            entry["similar_in_vuln_db"] = [{"name": r[0], "cve_count": r[1]} for r in cur.fetchall()]

        # RPM description from rpm -qi if available
        entry["description"] = get_rpm_description(src_name)

        # Example CVE that Grype found for any of the binary packages
        example = get_example_cve(src_name, info["binary_names"])
        if example:
            entry["example_cve"] = example

        unmapped.append(entry)

    catalog.close()
    vuln.close()
    return unmapped


def get_rpm_description(src_name: str) -> str:
    """Get RPM description from cached repodata."""
    try:
        with open("srpm_descriptions.json") as f:
            descs = json.load(f)
        return descs.get(src_name, "")
    except FileNotFoundError:
        return ""


def get_example_cve(src_name: str, binary_names: list[str]) -> dict | None:
    """Find an example CVE from scan findings for this package."""
    try:
        scan = psycopg2.connect("host=localhost port=15432 dbname=scandb user=scandb password=scandb")
        with scan.cursor() as cur:
            placeholders = ",".join(["%s"] * len(binary_names))
            cur.execute(f"""
                SELECT f.vuln_id, f.package_name, f.severity
                FROM findings f JOIN scans s ON f.scan_id = s.id
                WHERE s.status = 'completed' AND f.package_name IN ({placeholders})
                LIMIT 1
            """, binary_names)
            row = cur.fetchone()
            if row:
                vuln_id = row[0]
                # Get what vuln-ingest calls this CVE's packages
                vuln = psycopg2.connect("host=localhost port=15434 dbname=vulndb user=vulndb password=vulndb")
                with vuln.cursor() as cur2:
                    cur2.execute("""
                        SELECT DISTINCT lower(package_name), lower(vendor)
                        FROM affected_packages WHERE vuln_id = %s AND package_name != ''
                        LIMIT 5
                    """, (vuln_id,))
                    vuln_names = [{"name": r[0], "vendor": r[1]} for r in cur2.fetchall()]
                vuln.close()
                return {"cve_id": vuln_id, "grype_package": row[1], "severity": row[2], "vuln_db_names": vuln_names}
        scan.close()
    except Exception:
        pass
    return None


def get_vuln_package_names():
    """Get package names from vuln-ingest for the agent to search."""
    vuln = psycopg2.connect("host=localhost port=15434 dbname=vulndb user=vulndb password=vulndb")
    with vuln.cursor() as cur:
        cur.execute("""
            SELECT lower(package_name), count(DISTINCT vuln_id) as cve_count
            FROM affected_packages
            WHERE package_name != '' AND package_name != 'n/a'
            GROUP BY lower(package_name)
            HAVING count(DISTINCT vuln_id) >= 3
            ORDER BY cve_count DESC
            LIMIT 3000
        """)
        result = {r[0]: r[1] for r in cur.fetchall()}
    vuln.close()
    return result


def propose_with_vertex(unmapped: list[dict]) -> list[dict]:
    """Use Vertex AI with Claude Haiku 4.5 to propose mappings."""
    import anthropic

    vuln_names = get_vuln_package_names()
    # Provide top names as searchable list
    top_names_sample = dict(list(vuln_names.items())[:500])

    client = anthropic.AnthropicVertex(
        region=os.getenv("CLOUD_ML_REGION", "global"),
        project_id=os.getenv("GOOGLE_CLOUD_PROJECT", ""),
    )

    prompt = f"""You are a security engineer mapping RPM source package names to their upstream CVE/NVD package names.

## Vulnerability database package names (top 500 by CVE count)
{json.dumps(top_names_sample, indent=2)}

## RPM packages to map
Each entry includes:
- rpm_name: the source RPM name
- binary_packages: RPM subpackages built from this source
- description: what the software is (if known)
- similar_in_vuln_db: similar names found in our vuln DB
- example_cve: a real CVE that Grype matched to this package, showing what names our vuln DB uses for that CVE

{json.dumps(unmapped, indent=2)}

## Instructions
For each RPM package, find the matching name(s) from the vulnerability database list above.

Rules:
1. ONLY propose names that exist in the vulnerability database list above
2. Use the description and example CVE to understand what the software is
3. If the example_cve shows vuln_db_names, those are the names our DB uses — check if they match the RPM
4. If genuinely no match exists (distro-specific package with no upstream CVEs), set confidence to "none"
5. Do NOT match unrelated software just because names are similar

Respond ONLY with a JSON array:
[
  {{
    "rpm_name": "httpd",
    "proposed_names": ["http_server"],
    "confidence": "high",
    "reasoning": "Apache HTTP Server. Example CVE shows vuln DB uses 'http_server' (vendor: apache)"
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
        return json.loads(text[start:end])
    return []


def main():
    unmapped = get_unmapped_names()
    print(f"Found {len(unmapped)} unmapped source RPM names", file=sys.stderr)

    if not unmapped:
        print("All source RPM names have matches!", file=sys.stderr)
        return

    for u in unmapped:
        desc = u.get("description", "")
        example = u.get("example_cve", {})
        cve_id = example.get("cve_id", "-")
        print(f"  {u['rpm_name']:25s} desc={desc[:40]:40s} example_cve={cve_id}", file=sys.stderr)

    if not os.getenv("GOOGLE_CLOUD_PROJECT"):
        print("\nSet GOOGLE_CLOUD_PROJECT and ensure ADC is configured.", file=sys.stderr)
        json.dump({"unmapped": unmapped}, sys.stdout, indent=2)
        return

    print("\nAsking Haiku 4.5 via Vertex AI...", file=sys.stderr)
    proposals = propose_with_vertex(unmapped)

    output = {
        "status": "PROPOSED — requires human review before use",
        "generated_by": "propose_mappings.py + Claude Haiku 4.5 (Vertex AI)",
        "mappings": proposals,
    }

    with open("mappings.json", "w") as f:
        json.dump(output, f, indent=2)

    matched = sum(1 for p in proposals if p.get("confidence") not in ("none", None))
    print(f"\nWrote {len(proposals)} proposals ({matched} with matches) to mappings.json", file=sys.stderr)
    print("Review and edit before running match.py!", file=sys.stderr)


if __name__ == "__main__":
    main()
