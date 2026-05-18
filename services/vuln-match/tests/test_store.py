"""Tests for advisory store — uses a real SQLite-like mock or in-memory approach.

Since the store uses psycopg parameterized queries, we test the data model
and SQL logic via a real PostgreSQL if DATABASE_URL is set, otherwise skip.
"""

import os
import pytest

from vuln_match.store.postgres import Advisory, AdvisoryStore, NameMapping, MatchState

MATCHDB_URL = os.getenv("MATCHDB_URL", "")


def _make_advisory(**overrides) -> Advisory:
    defaults = {
        "source_package": "openssl",
        "vuln_id": "CVE-2026-0001",
        "status": "detected",
        "confidence": "medium",
        "match_type": "direct",
        "upstream_version": "3.5.6",
        "rpm_version": "3.5.6-0.3.hum1",
        "upstream_fixed_version": "3.6.0",
        "severity": "HIGH",
        "cvss_score": "7.5",
    }
    defaults.update(overrides)
    return Advisory(**defaults)


class TestAdvisoryDataclass:
    def test_default_values(self):
        adv = Advisory(source_package="curl", vuln_id="CVE-2026-0001")
        assert adv.status == "detected"
        assert adv.confidence == "medium"
        assert adv.flags == []
        assert adv.in_kev is False

    def test_with_flags(self):
        adv = Advisory(
            source_package="curl",
            vuln_id="CVE-2026-0001",
            flags=["no-fixed-version", "low-confidence"],
        )
        assert len(adv.flags) == 2


class TestNameMappingDataclass:
    def test_defaults(self):
        m = NameMapping(rpm_name="httpd", vuln_names=["http_server"])
        assert m.source == "manual"
        assert m.reviewed is False
        assert m.usage_count == 0

    def test_agent_source(self):
        m = NameMapping(
            rpm_name="httpd",
            vuln_names=["http_server", "httpd"],
            source="agent",
            agent_reasoning="CPE dictionary maps httpd to Apache HTTP Server",
        )
        assert m.source == "agent"
        assert "CPE" in m.agent_reasoning


class TestMatchStateDataclass:
    def test_defaults(self):
        s = MatchState(source_package="openssl")
        assert s.last_matched_at is None
        assert s.vuln_checkpoint == ""
        assert s.catalog_version == ""


@pytest.mark.skipif(not MATCHDB_URL, reason="MATCHDB_URL not set")
class TestAdvisoryStoreIntegration:
    """Integration tests requiring a real PostgreSQL database."""

    @pytest.fixture(autouse=True)
    def setup_store(self):
        from vuln_match.db.pool import create_pool, run_migrations

        run_migrations(MATCHDB_URL)
        pool = create_pool(MATCHDB_URL, min_size=1, max_size=2)
        self.store = AdvisoryStore(pool)

        # Clean tables
        with pool.connection() as conn:
            conn.execute("DELETE FROM advisories")
            conn.execute("DELETE FROM name_mappings")
            conn.execute("DELETE FROM match_state")
            conn.commit()

        yield
        pool.close()

    def test_upsert_and_get(self):
        adv = _make_advisory()
        self.store.upsert_advisory(adv)
        got = self.store.get_advisory("openssl", "CVE-2026-0001")
        assert got is not None
        assert got.status == "detected"
        assert got.upstream_version == "3.5.6"

    def test_upsert_updates_existing(self):
        adv = _make_advisory()
        self.store.upsert_advisory(adv)
        adv.status = "affected"
        self.store.upsert_advisory(adv)
        got = self.store.get_advisory("openssl", "CVE-2026-0001")
        assert got.status == "affected"

    def test_list_by_status(self):
        self.store.upsert_advisory(_make_advisory(vuln_id="CVE-1", status="detected"))
        self.store.upsert_advisory(_make_advisory(vuln_id="CVE-2", status="affected"))
        self.store.upsert_advisory(_make_advisory(vuln_id="CVE-3", status="detected"))

        detected = self.store.list_advisories(status="detected")
        assert len(detected) == 2

    def test_list_by_package(self):
        self.store.upsert_advisory(_make_advisory(source_package="curl", vuln_id="CVE-1"))
        self.store.upsert_advisory(_make_advisory(source_package="openssl", vuln_id="CVE-2"))

        curl_advs = self.store.list_advisories(source_package="curl")
        assert len(curl_advs) == 1
        assert curl_advs[0].source_package == "curl"

    def test_review(self):
        self.store.upsert_advisory(_make_advisory())
        ok = self.store.review_advisory(
            "openssl", "CVE-2026-0001",
            status="fixed",
            reviewed_by="rsturla",
            distro_fixed_version="3.6.0-1.hum1",
        )
        assert ok
        got = self.store.get_advisory("openssl", "CVE-2026-0001")
        assert got.status == "fixed"
        assert got.reviewed_by == "rsturla"

    def test_prior_decisions(self):
        self.store.upsert_advisory(_make_advisory(vuln_id="CVE-1", status="affected"))
        self.store.upsert_advisory(_make_advisory(vuln_id="CVE-2", status="not-affected"))
        self.store.upsert_advisory(_make_advisory(vuln_id="CVE-3", status="detected"))

        prior = self.store.get_prior_decisions("openssl")
        assert len(prior) == 2  # excludes "detected"

    def test_mapping_crud(self):
        m = NameMapping(rpm_name="httpd", vuln_names=["http_server"], source="agent")
        self.store.upsert_mapping(m)

        got = self.store.get_mapping("httpd")
        assert got is not None
        assert got.vuln_names == ["http_server"]

        self.store.increment_mapping_usage("httpd", 5)
        got = self.store.get_mapping("httpd")
        assert got.usage_count == 5

        self.store.review_mapping("httpd")
        got = self.store.get_mapping("httpd")
        assert got.reviewed is True

    def test_get_all_mappings(self):
        self.store.upsert_mapping(NameMapping("httpd", ["http_server"]))
        self.store.upsert_mapping(NameMapping("dotnet8.0", ["dotnet", ".net"]))

        all_m = self.store.get_all_mappings()
        assert "httpd" in all_m
        assert "dotnet8.0" in all_m

    def test_match_state(self):
        from datetime import datetime, timezone

        state = MatchState(
            source_package="curl",
            last_matched_at=datetime.now(timezone.utc),
            catalog_version="8.19.0-2.hum1",
        )
        self.store.upsert_match_state(state)

        got = self.store.get_match_state("curl")
        assert got is not None
        assert got.catalog_version == "8.19.0-2.hum1"

    def test_ping(self):
        assert self.store.ping() is True
