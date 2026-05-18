"""Tests for name resolution (Stage 1)."""

from vuln_match.match.cpe_index import CpeIndex
from vuln_match.match.name_resolver import (
    ResolvedName,
    parse_source_rpm,
    resolve_names,
)


class TestParseSourceRpm:
    def test_with_upstream_qualifier(self):
        purl = "pkg:rpm/redhat/php-cli@8.5.6-1.hum1?arch=x86_64&upstream=php-8.5.6-1.hum1.src.rpm"
        name, ver = parse_source_rpm(purl, "php-cli", "8.5.6-1.hum1")
        assert name == "php"
        assert ver == "8.5.6"

    def test_without_upstream_qualifier(self):
        purl = "pkg:rpm/redhat/curl@8.19.0-2.hum1?arch=x86_64"
        name, ver = parse_source_rpm(purl, "curl", "8.19.0-2.hum1")
        assert name == "curl"
        assert ver == "8.19.0"

    def test_complex_version(self):
        purl = "pkg:rpm/redhat/openssl-libs@3.5.6-0.3.hum1?upstream=openssl-3.5.6-0.3.hum1.src.rpm"
        name, ver = parse_source_rpm(purl, "openssl-libs", "3.5.6-0.3.hum1")
        assert name == "openssl"
        assert ver == "3.5.6"

    def test_epoch_in_name(self):
        purl = "pkg:rpm/redhat/vim-enhanced@9.1.1-1.hum1?upstream=vim-9.1.1-1.hum1.src.rpm"
        name, ver = parse_source_rpm(purl, "vim-enhanced", "9.1.1-1.hum1")
        assert name == "vim"
        assert ver == "9.1.1"

    def test_no_purl(self):
        name, ver = parse_source_rpm("", "coreutils", "9.11-2.hum1")
        assert name == "coreutils"
        assert ver == "9.11"


class TestResolveNames:
    VULN_KEYS = {"openssl", "curl", "http_server", "python", "libxml2", "glib", "util_linux", "php"}

    def test_direct_match(self):
        result = resolve_names("openssl", self.VULN_KEYS, CpeIndex(), {})
        assert result is not None
        assert result.vuln_names == ["openssl"]
        assert result.source == "direct"

    def test_stored_mapping_takes_priority(self):
        mappings = {"httpd": ["http_server"]}
        result = resolve_names("httpd", self.VULN_KEYS, CpeIndex(), mappings)
        assert result is not None
        assert result.vuln_names == ["http_server"]
        assert result.source == "mapping"

    def test_pattern_strip_lib_is_uncertain(self):
        vuln_keys = {"xml2", "curl"}
        result = resolve_names("libxml2", vuln_keys, CpeIndex(), {})
        assert result is not None
        assert "xml2" in result.vuln_names
        assert result.source == "uncertain"

    def test_pattern_add_lib(self):
        vuln_keys = {"libxml2", "curl"}
        result = resolve_names("xml2", vuln_keys, CpeIndex(), {})
        assert result is not None
        assert "libxml2" in result.vuln_names

    def test_pattern_strip_version_suffix(self):
        result = resolve_names("python3.14", self.VULN_KEYS, CpeIndex(), {})
        assert result is not None
        assert "python" in result.vuln_names

    def test_pattern_rpm_version_strip(self):
        result = resolve_names("glib2", self.VULN_KEYS, CpeIndex(), {})
        assert result is not None
        assert "glib" in result.vuln_names

    def test_pattern_hyphen_underscore(self):
        result = resolve_names("util-linux", self.VULN_KEYS, CpeIndex(), {})
        assert result is not None
        assert "util_linux" in result.vuln_names

    def test_cpe_lookup(self):
        cpe = CpeIndex({"httpd": ("apache", "http_server", "")})
        result = resolve_names("httpd", self.VULN_KEYS, cpe, {})
        assert result is not None
        assert "http_server" in result.vuln_names
        assert result.source == "cpe"

    def test_no_match_returns_none(self):
        result = resolve_names("totally-unknown-pkg", self.VULN_KEYS, CpeIndex(), {})
        assert result is None

    def test_mapping_with_no_vuln_key_match_falls_through(self):
        mappings = {"mypkg": ["nonexistent_name"]}
        result = resolve_names("mypkg", self.VULN_KEYS, CpeIndex(), mappings)
        assert result is None

    def test_libselinux_routes_to_agent(self):
        vuln_keys = {"selinux", "curl"}
        result = resolve_names("libselinux", vuln_keys, CpeIndex(), {})
        assert result is not None
        assert result.source == "uncertain"
        assert "selinux" in result.vuln_names

    def test_multiple_mapping_names(self):
        mappings = {"dotnet": ["dotnet", ".net"]}
        vuln_keys = {"dotnet", ".net", "curl"}
        result = resolve_names("dotnet", vuln_keys, CpeIndex(), mappings)
        assert result is not None
        assert set(result.vuln_names) == {"dotnet", ".net"}
