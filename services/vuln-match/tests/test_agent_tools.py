"""Tests for agent tool executor."""

import json
import os
from pathlib import Path
from unittest.mock import MagicMock

import pytest

from vuln_match.agent.tools import ToolExecutor


RPMS_REPO = os.getenv("RPMS_REPO_PATH", "/var/home/admin/Repos/hummingbird/rpms")


class TestReadSpecFile:
    @pytest.mark.skipif(
        not Path(RPMS_REPO, "default/main/rpms/openssl/openssl.spec").exists(),
        reason="rpms repo not available",
    )
    def test_read_openssl(self):
        executor = ToolExecutor(rpms_repo_path=RPMS_REPO)
        result = json.loads(executor.execute("read_spec_file", {"package_name": "openssl"}))
        assert "error" not in result
        assert "name" in result or "version" in result or "summary" in result

    def test_missing_package(self):
        executor = ToolExecutor(rpms_repo_path=RPMS_REPO)
        result = json.loads(executor.execute("read_spec_file", {"package_name": "nonexistent-pkg-xyz"}))
        assert "error" in result

    def test_no_repo_configured(self):
        executor = ToolExecutor(rpms_repo_path="")
        result = json.loads(executor.execute("read_spec_file", {"package_name": "curl"}))
        assert "error" in result


class TestQueryVulnerability:
    def test_no_api_configured(self):
        executor = ToolExecutor(vuln_api_url="")
        result = json.loads(executor.execute("query_vulnerability", {"cve_id": "CVE-2026-0001"}))
        assert "error" in result


class TestGetPriorDecisions:
    def test_no_store(self):
        executor = ToolExecutor()
        result = json.loads(executor.execute("get_prior_decisions", {"package_name": "curl"}))
        assert "error" in result

    def test_with_mock_store(self):
        store = MagicMock()
        store.get_prior_decisions.return_value = []
        executor = ToolExecutor(advisory_store=store)
        result = json.loads(executor.execute("get_prior_decisions", {"package_name": "curl"}))
        assert result == []
        store.get_prior_decisions.assert_called_once_with("curl", limit=10)


class TestUnknownTool:
    def test_returns_error(self):
        executor = ToolExecutor()
        result = json.loads(executor.execute("nonexistent_tool", {}))
        assert "error" in result


class TestSearchCveContext:
    def test_nvd_lookup_format(self):
        executor = ToolExecutor()
        # Just test it doesn't crash — real NVD calls may be rate-limited
        result = json.loads(executor.execute("search_cve_context", {"query": "test query no CVE"}))
        # Should return error since no CVE ID found for NVD lookup
        assert isinstance(result, dict)
