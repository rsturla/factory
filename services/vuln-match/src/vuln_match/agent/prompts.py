"""Prompt templates for agent enrichment."""

SYSTEM_PROMPT = """\
You are a Linux distribution security engineer reviewing CVEs for the Hummingbird Linux distribution.

Hummingbird builds RPM packages from upstream source code with minimal patches. When reviewing CVEs:

1. **Be conservative** — prefer "under-review" over "not-affected" when uncertain.
2. **Never dismiss without evidence** — use tools to verify before marking not-affected.
3. **Check the CVE description carefully** — many false positives come from:
   - Wrong project (e.g., WordPress plugin "curl" vs GNU curl)
   - Wrong platform (Windows-only, macOS-only)
   - Wrong branch (OpenSSL 1.1.x CVE applied to 3.x)
   - Feature-specific (affects only if compiled with specific flag)
4. **Version ranges are upstream** — the RPM version may include backported fixes.

When you identify a name mapping (e.g., RPM "httpd" = upstream "http_server"), state it explicitly \
in your reasoning so it can be stored for future use.

Respond with a JSON array of assessments. Each must have: cve_id, status, confidence, reasoning.
Valid statuses: "affected", "not-affected", "under-review".
Valid confidences: "high", "medium", "low".\
"""


def build_review_prompt(
    package: str,
    upstream_version: str,
    rpm_version: str,
    cve_details: list[dict],
    prior_decisions: list[dict] | None = None,
) -> str:
    parts = [
        f"## Package under review",
        f"- Source RPM: {package}",
        f"- Upstream version: {upstream_version}",
        f"- RPM version: {rpm_version}",
    ]

    if prior_decisions:
        parts.append("\n## Prior decisions for this package (for context)")
        for d in prior_decisions[:5]:
            parts.append(f"- {d.get('vuln_id', '?')}: {d.get('status', '?')} — {d.get('notes', '')[:100]}")

    parts.append("\n## CVEs to review")
    parts.append("Use the available tools to investigate each CVE before making a decision.")
    parts.append("For each CVE, call query_vulnerability to get full details, and read_spec_file ")
    parts.append("to understand the package. Use search_cve_context if the CVE description is ambiguous.")

    import json
    parts.append(f"\n```json\n{json.dumps(cve_details, indent=2, default=str)}\n```")

    parts.append("\nAfter investigating, respond with ONLY a JSON array:")
    parts.append('[{"cve_id": "...", "status": "...", "confidence": "...", "reasoning": "..."}]')

    return "\n".join(parts)
