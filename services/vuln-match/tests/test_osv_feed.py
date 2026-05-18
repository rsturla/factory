"""Tests for OSV feed generation."""

from datetime import datetime, timezone

from vuln_match.feed.osv import generate_osv_feed
from vuln_match.store.postgres import Advisory


def _make_advisory(**kw) -> Advisory:
    defaults = {
        "source_package": "openssl",
        "vuln_id": "CVE-2026-0001",
        "status": "affected",
        "confidence": "high",
        "upstream_version": "3.5.6",
        "rpm_version": "3.5.6-0.3.hum1",
        "upstream_fixed_version": "3.6.0",
        "cvss_score": "7.5",
        "notes": "Test advisory",
    }
    defaults.update(kw)
    return Advisory(**defaults)


class TestOsvFeed:
    def test_basic_feed(self):
        advs = [_make_advisory()]
        feed = generate_osv_feed(advs)
        assert feed["schema_version"] == "1.6.0"
        assert len(feed["entries"]) == 1

    def test_entry_structure(self):
        feed = generate_osv_feed([_make_advisory()])
        entry = feed["entries"][0]
        assert entry["id"] == "HUM-CVE-2026-0001"
        assert entry["aliases"] == ["CVE-2026-0001"]
        assert entry["affected"][0]["package"]["ecosystem"] == "Red Hat"
        assert entry["affected"][0]["package"]["name"] == "openssl"

    def test_fixed_with_distro_version(self):
        adv = _make_advisory(status="fixed", distro_fixed_version="3.6.0-1.hum1")
        feed = generate_osv_feed([adv])
        events = feed["entries"][0]["affected"][0]["ranges"][0]["events"]
        assert {"fixed": "3.6.0-1.hum1"} in events

    def test_affected_with_upstream_fix(self):
        adv = _make_advisory(status="affected", upstream_fixed_version="3.6.0")
        feed = generate_osv_feed([adv])
        events = feed["entries"][0]["affected"][0]["ranges"][0]["events"]
        assert {"limit": "3.6.0"} in events

    def test_deduplication(self):
        advs = [
            _make_advisory(vuln_id="CVE-1"),
            _make_advisory(vuln_id="CVE-1"),  # duplicate
            _make_advisory(vuln_id="CVE-2"),
        ]
        feed = generate_osv_feed(advs)
        assert len(feed["entries"]) == 2

    def test_empty_input(self):
        feed = generate_osv_feed([])
        assert feed["entries"] == []

    def test_severity_included(self):
        adv = _make_advisory(cvss_score="9.8")
        feed = generate_osv_feed([adv])
        entry = feed["entries"][0]
        assert "severity" in entry
        assert entry["severity"][0]["score"] == "9.8"

    def test_no_severity_when_empty(self):
        adv = _make_advisory(cvss_score="")
        feed = generate_osv_feed([adv])
        entry = feed["entries"][0]
        assert "severity" not in entry
