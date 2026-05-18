"""Tests for version range matching (Stage 2)."""

from vuln_match.match.version_matcher import (
    MatchResult,
    RangeQuality,
    _filter_comparable_ranges,
    _is_semver_like,
    classify_range_quality,
    compare_versions,
    is_affected,
    needs_agent,
)


class TestCompareVersions:
    def test_equal(self):
        assert compare_versions("1.2.3", "1.2.3") == 0

    def test_less(self):
        assert compare_versions("1.2.3", "1.2.4") == -1

    def test_greater(self):
        assert compare_versions("1.2.4", "1.2.3") == 1

    def test_different_lengths(self):
        assert compare_versions("1.2", "1.2.1") == -1

    def test_major_difference(self):
        assert compare_versions("2.0.0", "3.0.0") == -1

    def test_pep440_pre_release(self):
        assert compare_versions("1.0.0a1", "1.0.0") == -1

    def test_non_pep440_fallback(self):
        assert compare_versions("2024.01.15", "2024.02.01") == -1

    def test_mixed_alpha_numeric(self):
        assert compare_versions("1.0.0", "1.0.0") == 0


class TestClassifyRangeQuality:
    def test_empty(self):
        assert classify_range_quality([]) == RangeQuality.NONE

    def test_high_with_fixed(self):
        ranges = [{"introduced": "1.0", "fixed": "1.5"}]
        assert classify_range_quality(ranges) == RangeQuality.HIGH

    def test_high_with_last_affected(self):
        ranges = [{"introduced": "1.0", "last_affected": "1.4"}]
        assert classify_range_quality(ranges) == RangeQuality.HIGH

    def test_all_versions(self):
        ranges = [{"introduced": "0", "fixed": "*"}]
        assert classify_range_quality(ranges) == RangeQuality.ALL_VERSIONS

    def test_low_intro_only(self):
        ranges = [{"introduced": "1.0"}]
        assert classify_range_quality(ranges) == RangeQuality.LOW

    def test_mixed_prefers_high(self):
        ranges = [{"introduced": "1.0", "fixed": "2.0"}, {"introduced": "3.0"}]
        assert classify_range_quality(ranges) == RangeQuality.HIGH


class TestIsAffected:
    def test_no_ranges(self):
        result = is_affected("1.0", [])
        assert not result.affected
        assert "no-ranges" in result.flags

    def test_version_in_range(self):
        ranges = [{"introduced": "1.0", "fixed": "2.0"}]
        result = is_affected("1.5", ranges)
        assert result.affected
        assert result.fixed_version == "2.0"
        assert result.quality == RangeQuality.HIGH

    def test_version_at_fix(self):
        ranges = [{"introduced": "1.0", "fixed": "2.0"}]
        result = is_affected("2.0", ranges)
        assert not result.affected

    def test_version_past_fix(self):
        ranges = [{"introduced": "1.0", "fixed": "2.0"}]
        result = is_affected("2.1", ranges)
        assert not result.affected
        assert result.quality == RangeQuality.HIGH

    def test_version_before_intro(self):
        ranges = [{"introduced": "2.0", "fixed": "3.0"}]
        result = is_affected("1.0", ranges)
        assert not result.affected

    def test_last_affected_inclusive(self):
        ranges = [{"introduced": "1.0", "last_affected": "1.4"}]
        result = is_affected("1.4", ranges)
        assert result.affected

    def test_last_affected_past(self):
        ranges = [{"introduced": "1.0", "last_affected": "1.4"}]
        result = is_affected("1.5", ranges)
        assert not result.affected

    def test_all_versions_affected(self):
        ranges = [{"introduced": "0", "fixed": "*"}]
        result = is_affected("999.0", ranges)
        assert result.affected
        assert "no-fix-available" in result.flags
        assert result.quality == RangeQuality.ALL_VERSIONS

    def test_intro_only_low_confidence(self):
        ranges = [{"introduced": "1.0"}]
        result = is_affected("2.0", ranges)
        assert result.affected
        assert "no-fixed-version" in result.flags
        assert result.quality == RangeQuality.LOW

    def test_high_quality_trumps_low(self):
        ranges = [
            {"introduced": "1.0", "fixed": "1.5"},
            {"introduced": "1.0"},  # low quality says still affected
        ]
        result = is_affected("2.0", ranges)
        assert not result.affected
        assert result.quality == RangeQuality.HIGH

    def test_multiple_ranges_first_match(self):
        ranges = [
            {"introduced": "1.0", "fixed": "1.5"},
            {"introduced": "2.0", "fixed": "2.5"},
        ]
        result = is_affected("2.3", ranges)
        assert result.affected
        assert result.fixed_version == "2.5"

    def test_no_intro_with_fixed(self):
        ranges = [{"fixed": "2.0"}]
        result = is_affected("1.5", ranges)
        assert result.affected
        assert result.fixed_version == "2.0"


class TestSemverDetection:
    def test_normal_version(self):
        assert _is_semver_like("1.2.3") is True

    def test_git_sha_short(self):
        assert _is_semver_like("ab6eb2e") is False

    def test_git_sha_full(self):
        assert _is_semver_like("ab6eb2ec0744074004980d0f98677e52b215941f") is False

    def test_zero(self):
        assert _is_semver_like("0") is True

    def test_star(self):
        assert _is_semver_like("*") is True

    def test_empty(self):
        assert _is_semver_like("") is True

    def test_kernel_version(self):
        assert _is_semver_like("5.7") is True


class TestFilterRanges:
    def test_keeps_semver_ranges(self):
        ranges = [{"introduced": "1.0", "fixed": "2.0"}]
        assert _filter_comparable_ranges(ranges) == ranges

    def test_drops_git_sha_fixed(self):
        ranges = [{"introduced": "0", "fixed": "ab6eb2ec0744074004980d0f98677e52b215941f"}]
        assert _filter_comparable_ranges(ranges) == []

    def test_drops_git_sha_introduced(self):
        ranges = [{"introduced": "deadbeef1234567890abcdef1234567890abcdef", "fixed": "2.0"}]
        assert _filter_comparable_ranges(ranges) == []

    def test_mixed_keeps_semver_only(self):
        ranges = [
            {"introduced": "1.0", "fixed": "ab6eb2ec0744074004980d0f98677e52b215941f"},
            {"introduced": "2.0", "fixed": "3.0"},
        ]
        result = _filter_comparable_ranges(ranges)
        assert len(result) == 1
        assert result[0]["fixed"] == "3.0"


class TestIsAffectedWithSha:
    def test_git_sha_ranges_skipped(self):
        ranges = [{"introduced": "0", "fixed": "ab6eb2ec0744074004980d0f98677e52b215941f"}]
        result = is_affected("8.20.0", ranges)
        assert not result.affected
        assert "no-ranges" in result.flags

    def test_mixed_sha_and_semver(self):
        ranges = [
            {"introduced": "0", "fixed": "ab6eb2ec0744074004980d0f98677e52b215941f"},
            {"introduced": "8.0.0", "fixed": "8.18.0"},
        ]
        result = is_affected("8.19.0", ranges)
        # SHA range filtered, semver says not affected (8.19.0 >= 8.18.0)
        assert not result.affected


class TestDateFiltering:
    def test_date_version_not_semver(self):
        assert _is_semver_like("2023-09-12") is False

    def test_date_range_filtered(self):
        ranges = [{"introduced": "0", "fixed": "2023-09-12"}]
        assert _filter_comparable_ranges(ranges) == []

    def test_date_in_is_affected(self):
        ranges = [{"introduced": "0", "fixed": "2023-09-12"}]
        result = is_affected("15.2.1", ranges)
        assert not result.affected


class TestSuspiciousVersionJump:
    def test_normal_version(self):
        from vuln_match.match.version_matcher import has_suspicious_version_jump
        assert has_suspicious_version_jump("2.9.5", "2.9.6") is False

    def test_suspicious_jump(self):
        from vuln_match.match.version_matcher import has_suspicious_version_jump
        assert has_suspicious_version_jump("2.9.5", "5.4") is True

    def test_no_fix(self):
        from vuln_match.match.version_matcher import has_suspicious_version_jump
        assert has_suspicious_version_jump("2.9.5", "") is False

    def test_one_major_ahead_ok(self):
        from vuln_match.match.version_matcher import has_suspicious_version_jump
        assert has_suspicious_version_jump("2.9.5", "4.0") is False

    def test_three_major_ahead_suspicious(self):
        from vuln_match.match.version_matcher import has_suspicious_version_jump
        assert has_suspicious_version_jump("2.9.5", "6.0") is True


class TestNeedsAgent:
    def test_no_mapping(self):
        assert needs_agent(False, RangeQuality.HIGH) is True

    def test_mapping_high_quality(self):
        assert needs_agent(True, RangeQuality.HIGH) is False

    def test_mapping_all_versions(self):
        assert needs_agent(True, RangeQuality.ALL_VERSIONS) is False

    def test_mapping_low_quality(self):
        assert needs_agent(True, RangeQuality.LOW) is True

    def test_mapping_no_ranges(self):
        assert needs_agent(True, RangeQuality.NONE) is True
