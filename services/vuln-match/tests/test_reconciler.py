"""Tests for the matching reconciler."""

from unittest.mock import MagicMock, patch

from factory_workqueue.reconciler import ProcessRequest

from vuln_match.config import Config
from vuln_match.match.cpe_index import CpeIndex
from vuln_match.match.reconciler import _reconcile
from vuln_match.store.postgres import AdvisoryStore


def _make_config(**overrides) -> Config:
    defaults = {
        "matchdb_url": "", "catalogdb_url": "postgresql://test",
        "vulndb_url": "postgresql://test", "agent_enabled": False,
    }
    defaults.update(overrides)
    return Config(**defaults)


def _make_store(**mapping_overrides):
    store = MagicMock(spec=AdvisoryStore)
    store.get_all_mappings.return_value = mapping_overrides.get("mappings", {})
    store.get_reverse_mappings.return_value = mapping_overrides.get("reverse", {})
    store.get_prior_decisions.return_value = []
    store.get_mapping.return_value = None
    return store


def _call_reconcile(key, store, cfg, **kwargs):
    """Helper for CVE or package reconcile calls."""
    return _reconcile(
        req=ProcessRequest(key=key), cfg=cfg, store=store,
        cpe_index=CpeIndex(), agent_client=None, tool_executor=MagicMock(),
        catalog_pool=MagicMock(), vuln_pool=MagicMock(),
        get_vuln_keys=kwargs.get("get_vuln_keys", lambda: set()),
        get_mappings=kwargs.get("get_mappings", lambda: {}),
        get_reverse=kwargs.get("get_reverse", lambda: {}),
        get_catalog_sources=kwargs.get("get_catalog_sources", lambda: {}),
    )


class TestKeyDispatch:
    def test_unknown_key_rejects(self):
        result = _call_reconcile("bad-key", _make_store(), _make_config())
        assert result.action == "reject"

    def test_cve_prefix(self):
        with patch("vuln_match.match.reconciler._get_cve_details", return_value=None):
            result = _call_reconcile("cve:CVE-2026-0001", _make_store(), _make_config())
        assert result.action == "reject"  # CVE not found

    def test_pkg_prefix(self):
        with patch("vuln_match.match.reconciler._get_package_info", return_value=None):
            result = _call_reconcile("pkg:nonexistent", _make_store(), _make_config())
        assert result.action == "reject"


class TestCveCentric:
    @patch("vuln_match.match.reconciler._get_cve_details")
    def test_cve_not_found_rejects(self, mock_cve):
        mock_cve.return_value = None
        result = _call_reconcile("cve:CVE-2026-0001", _make_store(), _make_config())
        assert result.action == "reject"

    @patch("vuln_match.match.reconciler._get_cve_details")
    def test_cve_no_affected_names_completes(self, mock_cve):
        mock_cve.return_value = {"affected_names": [], "ranges": [], "severity": "", "cvss_score": ""}
        result = _call_reconcile("cve:CVE-2026-0001", _make_store(), _make_config())
        assert result.action == "completed"

    @patch("vuln_match.match.reconciler._get_cve_details")
    def test_cve_matches_rpm_via_reverse_mapping(self, mock_cve):
        mock_cve.return_value = {
            "affected_names": ["http_server"],
            "ranges": [{"introduced": "2.4.0", "fixed": "2.4.63"}],
            "ranges_by_vendor": {"apache": [{"introduced": "2.4.0", "fixed": "2.4.63"}]},
            "vendors_by_name": {"http_server": {"apache"}},
            "severity": "HIGH", "cvss_score": "7.5",
        }
        store = _make_store(reverse={"http_server": ["httpd"]})
        catalog = {"httpd": {"upstream_version": "2.4.62", "rpm_version": "2.4.62-1.hum1"}}

        result = _call_reconcile(
            "cve:CVE-2026-0001", store, _make_config(),
            get_reverse=lambda: {"http_server": ["httpd"]},
            get_catalog_sources=lambda: catalog,
        )
        assert result.action == "completed"
        store.upsert_advisories.assert_called_once()
        advs = store.upsert_advisories.call_args[0][0]
        assert len(advs) == 1
        assert advs[0].source_package == "httpd"
        assert advs[0].status == "affected"

    @patch("vuln_match.match.reconciler._get_cve_details")
    def test_cve_matches_rpm_via_direct_name(self, mock_cve):
        mock_cve.return_value = {
            "affected_names": ["curl"],
            "ranges": [{"introduced": "8.0.0", "fixed": "8.18.0"}],
            "ranges_by_vendor": {"haxx": [{"introduced": "8.0.0", "fixed": "8.18.0"}]},
            "vendors_by_name": {"curl": {"haxx"}},
            "severity": "HIGH", "cvss_score": "7.5",
        }
        catalog = {"curl": {"upstream_version": "8.19.0", "rpm_version": "8.19.0-2.hum1"}}

        store = _make_store()
        result = _call_reconcile(
            "cve:CVE-2026-0001", store, _make_config(),
            get_catalog_sources=lambda: catalog,
        )
        assert result.action == "completed"
        advs = store.upsert_advisories.call_args[0][0]
        assert len(advs) == 1
        assert advs[0].status == "not-affected"
        assert advs[0].upstream_fixed_version == "8.18.0"

    @patch("vuln_match.match.reconciler._get_cve_details")
    def test_cve_matches_multiple_python_versions(self, mock_cve):
        mock_cve.return_value = {
            "affected_names": ["python"],
            "ranges": [{"introduced": "3.0.0", "fixed": "3.13.2"}],
            "ranges_by_vendor": {"python": [{"introduced": "3.0.0", "fixed": "3.13.2"}]},
            "vendors_by_name": {"python": {"python"}},
            "severity": "HIGH", "cvss_score": "8.0",
        }
        catalog = {
            "python3.11": {"upstream_version": "3.11.12", "rpm_version": "3.11.12-1.hum1"},
            "python3.12": {"upstream_version": "3.12.9", "rpm_version": "3.12.9-1.hum1"},
            "python3.13": {"upstream_version": "3.13.5", "rpm_version": "3.13.5-1.hum1"},
            "python3.14": {"upstream_version": "3.14.1", "rpm_version": "3.14.1-1.hum1"},
        }

        store = _make_store()
        result = _call_reconcile(
            "cve:CVE-2026-PYTHON", store, _make_config(),
            get_catalog_sources=lambda: catalog,
        )
        assert result.action == "completed"
        advs = store.upsert_advisories.call_args[0][0]
        assert len(advs) == 4

        by_pkg = {a.source_package: a for a in advs}
        assert by_pkg["python3.11"].status == "affected"   # 3.11.12 < 3.13.2
        assert by_pkg["python3.12"].status == "affected"   # 3.12.9 < 3.13.2
        assert by_pkg["python3.13"].status == "not-affected"  # 3.13.5 >= 3.13.2
        assert by_pkg["python3.14"].status == "not-affected"  # 3.14.1 >= 3.13.2

    @patch("vuln_match.match.reconciler._get_cve_details")
    def test_cve_filters_wrong_vendor(self, mock_cve):
        """composer CVE from vendor 'tagdiv' (WordPress) should NOT match RPM 'composer' (PHP)."""
        mock_cve.return_value = {
            "affected_names": ["composer"],
            "ranges": [
                {"introduced": "1.0", "fixed": "5.4"},    # tagdiv WordPress plugin
                {"introduced": "2.0", "fixed": "2.9.6"},  # getcomposer PHP
            ],
            "ranges_by_vendor": {
                "tagdiv": [{"introduced": "1.0", "fixed": "5.4"}],
                "getcomposer": [{"introduced": "2.0", "fixed": "2.9.6"}],
            },
            "vendors_by_name": {"composer": {"tagdiv", "getcomposer"}},
            "severity": "HIGH", "cvss_score": "7.5",
        }
        catalog = {"composer": {"upstream_version": "2.9.5", "rpm_version": "2.9.5-1.hum1"}}
        store = _make_store()

        result = _call_reconcile(
            "cve:CVE-2026-COMPOSER", store, _make_config(),
            get_reverse=lambda: {},
            get_catalog_sources=lambda: catalog,
        )
        assert result.action == "completed"
        advs = store.upsert_advisories.call_args[0][0]
        assert len(advs) == 1
        # Should use getcomposer ranges (fix=2.9.6), NOT tagdiv (fix=5.4)
        assert advs[0].status == "affected"
        assert advs[0].upstream_fixed_version == "2.9.6"

    @patch("vuln_match.match.reconciler._get_cve_details")
    def test_cve_filters_npm_uuid_vendor(self, mock_cve):
        """uuid CVE from vendor 'uuidjs' (npm) should NOT match RPM 'uuid' (OSSP)."""
        mock_cve.return_value = {
            "affected_names": ["uuid"],
            "ranges": [{"introduced": "1.0.0", "fixed": "14.0.0"}],
            "ranges_by_vendor": {"uuidjs": [{"introduced": "1.0.0", "fixed": "14.0.0"}]},
            "vendors_by_name": {"uuid": {"uuidjs"}},
            "severity": "HIGH", "cvss_score": "7.5",
        }
        catalog = {"uuid": {"upstream_version": "1.6.2", "rpm_version": "1.6.2-1.hum1"}}
        store = _make_store()

        result = _call_reconcile(
            "cve:CVE-2026-UUID", store, _make_config(),
            get_catalog_sources=lambda: catalog,
        )
        assert result.action == "completed"
        advs = store.upsert_advisories.call_args[0][0]
        # Should NOT be marked as "affected" — vendor mismatch + version jump
        for a in advs:
            assert a.status != "affected", f"uuid CVE from npm vendor should not be 'affected'"

    @patch("vuln_match.match.reconciler._get_cve_details")
    def test_cve_no_ranges_flags_detected_not_dismissed(self, mock_cve):
        """CVE with no version ranges should be 'detected', not 'not-affected'."""
        mock_cve.return_value = {
            "affected_names": ["curl"],
            "ranges": [],
            "ranges_by_vendor": {},
            "vendors_by_name": {"curl": set()},
            "severity": "HIGH", "cvss_score": "7.5",
        }
        catalog = {"curl": {"upstream_version": "8.20.0", "rpm_version": "8.20.0-1.hum1"}}
        store = _make_store()

        result = _call_reconcile(
            "cve:CVE-2026-NORANGE", store, _make_config(),
            get_catalog_sources=lambda: catalog,
        )
        assert result.action == "completed"
        advs = store.upsert_advisories.call_args[0][0]
        assert len(advs) == 1
        assert advs[0].status == "detected"
        assert "no-ranges" in advs[0].flags

    @patch("vuln_match.match.reconciler._get_cve_details")
    def test_cve_requeues_on_db_error(self, mock_cve):
        mock_cve.side_effect = ConnectionError("db down")
        result = _call_reconcile("cve:CVE-2026-0001", _make_store(), _make_config())
        assert result.action == "requeue"


class TestPackageCentric:
    @patch("vuln_match.match.reconciler._get_package_info")
    def test_pkg_not_found_rejects(self, mock_pkg):
        mock_pkg.return_value = None
        result = _call_reconcile("pkg:nonexistent", _make_store(), _make_config())
        assert result.action == "reject"

    @patch("vuln_match.match.reconciler._get_package_info")
    @patch("vuln_match.match.reconciler._query_vulns_by_package")
    def test_pkg_stores_mapping_on_resolution(self, mock_query, mock_pkg):
        mock_pkg.return_value = {"source_name": "curl", "upstream_version": "8.19.0", "rpm_version": "8.19.0-2.hum1"}
        mock_query.return_value = {}

        store = _make_store()
        _call_reconcile(
            "pkg:curl", store, _make_config(),
            get_vuln_keys=lambda: {"curl"},
            get_mappings=lambda: {},
        )
        # Should store mapping for future CVE-centric lookups
        store.upsert_mapping.assert_called_once()

    @patch("vuln_match.match.reconciler._get_package_info")
    @patch("vuln_match.match.reconciler._query_vulns_by_package")
    def test_pkg_match_state_updated(self, mock_query, mock_pkg):
        mock_pkg.return_value = {"source_name": "curl", "upstream_version": "8.19.0", "rpm_version": "8.19.0-2.hum1"}
        mock_query.return_value = {}

        store = _make_store()
        _call_reconcile(
            "pkg:curl", store, _make_config(),
            get_vuln_keys=lambda: {"curl"}, get_mappings=lambda: {},
        )
        store.upsert_match_state.assert_called_once()

    @patch("vuln_match.match.reconciler._get_package_info")
    def test_pkg_requeues_on_db_error(self, mock_pkg):
        mock_pkg.side_effect = ConnectionError("db down")
        result = _call_reconcile("pkg:curl", _make_store(), _make_config())
        assert result.action == "requeue"
