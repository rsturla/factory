"""Integration tests for agent enrichment with real Vertex AI calls.

Requires GOOGLE_CLOUD_PROJECT env var.
"""

import json
import os

import pytest

VERTEX_PROJECT = os.getenv("GOOGLE_CLOUD_PROJECT", "")

pytestmark = pytest.mark.skipif(
    not VERTEX_PROJECT,
    reason="GOOGLE_CLOUD_PROJECT not set — skipping agent integration tests",
)


class TestAgentIntegration:
    def _create_client(self):
        import anthropic
        return anthropic.AnthropicVertex(
            region=os.getenv("CLOUD_ML_REGION", "global"),
            project_id=VERTEX_PROJECT,
        )

    def test_basic_enrichment(self):
        from vuln_match.agent.enricher import enrich_batch
        from vuln_match.agent.tools import ToolExecutor

        client = self._create_client()
        executor = ToolExecutor(
            rpms_repo_path=os.getenv("RPMS_REPO_PATH", "/var/home/admin/Repos/hummingbird/rpms"),
        )

        cve_details = [
            {
                "cve_id": "CVE-2024-6119",
                "range_quality": "high",
                "flags": [],
                "fixed_version": "3.3.2",
            },
        ]

        result = enrich_batch(
            client=client,
            package="openssl",
            upstream_version="3.5.6",
            rpm_version="3.5.6-0.3.hum1",
            cve_details=cve_details,
            tool_executor=executor,
        )

        assert len(result.assessments) >= 1
        assert result.assessments[0].cve_id == "CVE-2024-6119"
        assert result.assessments[0].status in ("affected", "not-affected", "under-review")
        assert result.input_tokens > 0

    def test_tool_use_spec_file(self):
        from vuln_match.agent.enricher import enrich_batch
        from vuln_match.agent.tools import ToolExecutor

        rpms_path = os.getenv("RPMS_REPO_PATH", "/var/home/admin/Repos/hummingbird/rpms")
        if not os.path.exists(os.path.join(rpms_path, "default/main/rpms/curl")):
            pytest.skip("rpms repo not available")

        client = self._create_client()
        executor = ToolExecutor(rpms_repo_path=rpms_path)

        cve_details = [
            {
                "cve_id": "CVE-2024-11053",
                "range_quality": "low",
                "flags": ["no-fixed-version"],
                "fixed_version": "",
            },
        ]

        result = enrich_batch(
            client=client,
            package="curl",
            upstream_version="8.19.0",
            rpm_version="8.19.0-2.hum1",
            cve_details=cve_details,
            tool_executor=executor,
        )

        assert len(result.assessments) >= 1
        # Agent should have used read_spec_file tool
        assert result.input_tokens > 0
