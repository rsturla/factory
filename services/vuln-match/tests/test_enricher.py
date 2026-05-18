"""Tests for agent enricher — uses recorded fixtures, no real API calls."""

import json
from dataclasses import dataclass
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from vuln_match.agent.enricher import (
    AgentAssessment,
    EnrichmentResult,
    _extract_mappings,
    _parse_assessments,
)
from vuln_match.agent.prompts import build_review_prompt

FIXTURES_DIR = Path(__file__).parent / "fixtures"


class TestParseAssessments:
    def test_valid_json(self):
        text = """Here are my assessments:
[
  {"cve_id": "CVE-2025-0001", "status": "affected", "confidence": "high", "reasoning": "Version in range"},
  {"cve_id": "CVE-2025-0002", "status": "not-affected", "confidence": "medium", "reasoning": "Wrong branch"}
]"""
        result = _parse_assessments(text)
        assert len(result) == 2
        assert result[0].cve_id == "CVE-2025-0001"
        assert result[0].status == "affected"
        assert result[1].status == "not-affected"

    def test_invalid_status_defaults_to_under_review(self):
        text = '[{"cve_id": "CVE-1", "status": "maybe", "confidence": "low", "reasoning": "unsure"}]'
        result = _parse_assessments(text)
        assert result[0].status == "under-review"

    def test_no_json(self):
        result = _parse_assessments("No JSON here, just text.")
        assert result == []

    def test_truncated_json(self):
        text = '[{"cve_id": "CVE-1", "status": "affected", "confidence": "high", "reasoning": "ok"}, {"cve_id": "CVE-2"'
        result = _parse_assessments(text)
        assert len(result) == 1
        assert result[0].cve_id == "CVE-1"

    def test_fixture_openssl(self):
        text = (FIXTURES_DIR / "agent_review_openssl.json").read_text()
        result = _parse_assessments(text)
        assert len(result) == 3
        statuses = {a.cve_id: a.status for a in result}
        assert statuses["CVE-2025-0001"] == "not-affected"
        assert statuses["CVE-2025-0002"] == "affected"
        assert statuses["CVE-2025-0003"] == "under-review"

    def test_fixture_curl(self):
        text = (FIXTURES_DIR / "agent_review_curl.json").read_text()
        result = _parse_assessments(text)
        assert len(result) == 2
        assert result[0].status == "not-affected"
        assert "WordPress" in result[0].reasoning


class TestExtractMappings:
    def test_rpm_maps_to_pattern(self):
        text = 'RPM "httpd" maps to upstream "http_server" in NVD.'
        mappings = _extract_mappings(text, "httpd")
        assert len(mappings) >= 1
        assert any(m["vuln_name"] == "http_server" for m in mappings)

    def test_no_mapping(self):
        text = "This CVE affects openssl version 3.5.6."
        mappings = _extract_mappings(text, "openssl")
        assert mappings == []

    def test_same_name_not_extracted(self):
        text = 'RPM "curl" maps to upstream "curl".'
        mappings = _extract_mappings(text, "curl")
        assert all(m["rpm_name"] != m["vuln_name"] for m in mappings)


class TestBuildReviewPrompt:
    def test_basic_prompt(self):
        prompt = build_review_prompt(
            package="openssl",
            upstream_version="3.5.6",
            rpm_version="3.5.6-0.3.hum1",
            cve_details=[{"cve_id": "CVE-2025-0001", "summary": "Test CVE"}],
        )
        assert "openssl" in prompt
        assert "3.5.6" in prompt
        assert "CVE-2025-0001" in prompt

    def test_with_prior_decisions(self):
        prompt = build_review_prompt(
            package="openssl",
            upstream_version="3.5.6",
            rpm_version="3.5.6-0.3.hum1",
            cve_details=[],
            prior_decisions=[
                {"vuln_id": "CVE-2025-0001", "status": "not-affected", "notes": "Wrong branch"},
            ],
        )
        assert "Prior decisions" in prompt
        assert "CVE-2025-0001" in prompt
        assert "not-affected" in prompt
