"""Tool definitions for agent enrichment.

Each tool is defined as an Anthropic tool schema + an executor function.
The enricher calls executors when the agent uses a tool.
"""

from __future__ import annotations

import json
import logging
import re
from pathlib import Path

import httpx

logger = logging.getLogger(__name__)

TOOL_DEFINITIONS = [
    {
        "name": "read_spec_file",
        "description": (
            "Read an RPM spec file from the Hummingbird rpms repository. "
            "Returns package metadata: Name, Version, Summary, URL, License, "
            "and the first 80 lines of %changelog. Use this to understand "
            "what upstream project an RPM corresponds to."
        ),
        "input_schema": {
            "type": "object",
            "properties": {
                "package_name": {
                    "type": "string",
                    "description": "The RPM source package name (e.g., 'openssl', 'curl', 'httpd')",
                },
            },
            "required": ["package_name"],
        },
    },
    {
        "name": "query_vulnerability",
        "description": (
            "Query the vulnerability database for full CVE details including "
            "description, severity, CVSS score, and all affected package entries "
            "with version ranges. Use this to understand what a CVE actually affects."
        ),
        "input_schema": {
            "type": "object",
            "properties": {
                "cve_id": {
                    "type": "string",
                    "description": "The CVE identifier (e.g., 'CVE-2026-1234')",
                },
            },
            "required": ["cve_id"],
        },
    },
    {
        "name": "search_cve_context",
        "description": (
            "Search the web for additional context about a CVE. "
            "Use this when the CVE description is ambiguous or you need "
            "to verify which project/product is actually affected. "
            "Returns top search result snippets."
        ),
        "input_schema": {
            "type": "object",
            "properties": {
                "query": {
                    "type": "string",
                    "description": "Search query (e.g., 'CVE-2026-1234 openssl affected versions')",
                },
            },
            "required": ["query"],
        },
    },
    {
        "name": "get_prior_decisions",
        "description": (
            "Retrieve prior advisory decisions made for this package. "
            "Shows how similar CVEs were previously assessed, including reasoning. "
            "Use this to maintain consistency with prior decisions."
        ),
        "input_schema": {
            "type": "object",
            "properties": {
                "package_name": {
                    "type": "string",
                    "description": "The RPM source package name",
                },
                "limit": {
                    "type": "integer",
                    "description": "Maximum number of prior decisions to return",
                    "default": 10,
                },
            },
            "required": ["package_name"],
        },
    },
]


class ToolExecutor:
    """Executes agent tool calls against real backends."""

    def __init__(
        self,
        rpms_repo_path: str = "",
        vuln_api_url: str = "",
        advisory_store: object | None = None,
    ) -> None:
        self._rpms_path = Path(rpms_repo_path) if rpms_repo_path else None
        self._vuln_api_url = vuln_api_url.rstrip("/") if vuln_api_url else ""
        self._store = advisory_store
        self._http = httpx.Client(timeout=15.0)

    def close(self) -> None:
        self._http.close()

    def execute(self, tool_name: str, tool_input: dict) -> str:
        handlers = {
            "read_spec_file": self._read_spec_file,
            "query_vulnerability": self._query_vulnerability,
            "search_cve_context": self._search_cve_context,
            "get_prior_decisions": self._get_prior_decisions,
        }
        handler = handlers.get(tool_name)
        if not handler:
            return json.dumps({"error": f"unknown tool: {tool_name}"})
        try:
            return handler(tool_input)
        except Exception as e:
            logger.error("tool %s failed: %s", tool_name, e)
            return json.dumps({"error": str(e)})

    def _read_spec_file(self, input: dict) -> str:
        pkg = input["package_name"]
        if not self._rpms_path:
            return json.dumps({"error": "rpms_repo_path not configured"})

        if not re.match(r"^[a-zA-Z0-9][a-zA-Z0-9._+-]*$", pkg):
            return json.dumps({"error": "invalid package name"})

        rpms_dir = self._rpms_path / "default" / "main" / "rpms"
        spec_path = rpms_dir / pkg / f"{pkg}.spec"

        if not spec_path.exists():
            if rpms_dir.exists():
                for d in rpms_dir.iterdir():
                    if d.name.lower() == pkg.lower():
                        spec_path = d / f"{d.name}.spec"
                        break

        if spec_path.exists():
            resolved = spec_path.resolve()
            if not str(resolved).startswith(str(rpms_dir.resolve())):
                return json.dumps({"error": "invalid package path"})

        if not spec_path.exists():
            return json.dumps({"error": f"spec file not found for {pkg}"})

        text = spec_path.read_text(errors="replace")

        # Extract key fields
        result: dict = {}
        for line in text.split("\n")[:200]:
            stripped = line.strip()
            for field in ("Name:", "Version:", "Summary:", "URL:", "License:", "Url:"):
                if stripped.lower().startswith(field.lower()):
                    key = field.rstrip(":").lower()
                    if key == "url":
                        key = "url"
                    result[key] = stripped[len(field):].strip()

        # Extract changelog (first 80 lines)
        changelog_start = text.find("%changelog")
        if changelog_start >= 0:
            changelog_lines = text[changelog_start:].split("\n")[1:81]
            result["changelog"] = "\n".join(changelog_lines)

        # Extract patches for context
        patches = [line.strip() for line in text.split("\n") if re.match(r"^Patch\d+:", line.strip())]
        if patches:
            result["patches"] = patches[:20]

        return json.dumps(result, indent=2)

    def _query_vulnerability(self, input: dict) -> str:
        cve_id = input["cve_id"]
        if not re.match(r"^CVE-\d{4}-\d{4,}$", cve_id):
            return json.dumps({"error": f"invalid CVE ID format: {cve_id}"})
        if not self._vuln_api_url:
            return json.dumps({"error": "vuln_api_url not configured"})

        resp = self._http.get(f"{self._vuln_api_url}/v1/vulns/{cve_id}")
        if resp.status_code == 404:
            return json.dumps({"error": f"CVE {cve_id} not found"})
        if resp.status_code != 200:
            return json.dumps({"error": f"vuln API returned {resp.status_code}"})

        data = resp.json()
        # Trim to relevant fields to save tokens
        return json.dumps(
            {
                "id": data.get("id", cve_id),
                "summary": data.get("summary", ""),
                "details": (data.get("details", "") or "")[:2000],
                "severity": data.get("severity", []),
                "aliases": data.get("aliases", []),
                "affected": data.get("affected_packages", [])[:20],
            },
            indent=2,
            default=str,
        )

    def _search_cve_context(self, input: dict) -> str:
        query = input["query"]
        # Try NVD page directly
        cve_match = re.search(r"(CVE-\d{4}-\d+)", query)
        if cve_match:
            cve_id = cve_match.group(1)
            try:
                resp = self._http.get(
                    f"https://services.nvd.nist.gov/rest/json/cves/2.0?cveId={cve_id}",
                    headers={"Accept": "application/json"},
                )
                if resp.status_code == 200:
                    data = resp.json()
                    vulns = data.get("vulnerabilities", [])
                    if vulns:
                        cve_data = vulns[0].get("cve", {})
                        descriptions = cve_data.get("descriptions", [])
                        en_desc = next((d["value"] for d in descriptions if d.get("lang") == "en"), "")
                        refs = [r.get("url", "") for r in cve_data.get("references", [])[:5]]
                        return json.dumps(
                            {"source": "NVD", "description": en_desc[:2000], "references": refs},
                            indent=2,
                        )
            except Exception as e:
                logger.debug("NVD lookup failed: %s", e)

        return json.dumps({"error": "no results found", "query": query})

    def _get_prior_decisions(self, input: dict) -> str:
        pkg = input["package_name"]
        limit = input.get("limit", 10)

        if not self._store:
            return json.dumps({"error": "advisory store not configured"})

        decisions = self._store.get_prior_decisions(pkg, limit=limit)
        return json.dumps(
            [
                {
                    "vuln_id": d.vuln_id,
                    "status": d.status,
                    "confidence": d.confidence,
                    "notes": d.notes,
                    "agent_reasoning": d.agent_reasoning,
                }
                for d in decisions
            ],
            indent=2,
        )
