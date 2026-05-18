"""Stage 2: Version range matching with quality classification."""

from __future__ import annotations

import re
from dataclasses import dataclass, field
from enum import Enum

from packaging.version import Version, InvalidVersion

_GIT_SHA_RE = re.compile(r"^[0-9a-f]{7,40}$")
_DATE_RE = re.compile(r"^\d{4}-\d{2}-\d{2}")


def _is_semver_like(v: str) -> bool:
    """Return True if the version looks like a numeric version, not a git SHA, date, or other non-version string."""
    if not v or v == "*":
        return True
    if _GIT_SHA_RE.match(v):
        return False
    if _DATE_RE.match(v):
        return False
    # Must start with a digit to be a version
    return bool(v) and v[0].isdigit()


def _filter_comparable_ranges(ranges: list[dict]) -> list[dict]:
    """Remove ranges with git SHA or non-semver fixed/introduced versions.

    OSV entries often have GIT ecosystem ranges with commit SHAs as
    introduced/fixed values. These are not comparable to RPM upstream
    versions and produce false positives.
    """
    result = []
    for r in ranges:
        fixed = r.get("fixed", "")
        intro = r.get("introduced", "")
        last = r.get("last_affected", "")

        if fixed and fixed != "*" and not _is_semver_like(fixed):
            continue
        if intro and not _is_semver_like(intro):
            continue
        if last and not _is_semver_like(last):
            continue
        result.append(r)
    return result


class RangeQuality(Enum):
    HIGH = "high"
    ALL_VERSIONS = "all_versions"
    LOW = "low"
    NONE = "none"


@dataclass
class MatchResult:
    affected: bool
    fixed_version: str = ""
    quality: RangeQuality = RangeQuality.NONE
    flags: list[str] = field(default_factory=list)


def compare_versions(a: str, b: str) -> int:
    """Compare two version strings. Returns -1, 0, or 1."""
    try:
        va, vb = Version(a), Version(b)
        if va < vb:
            return -1
        if va > vb:
            return 1
        return 0
    except InvalidVersion:
        pass

    def split(v: str) -> list[str]:
        return re.split(r"[.\-]", v)

    pa, pb = split(a), split(b)
    for i in range(max(len(pa), len(pb))):
        ca = pa[i] if i < len(pa) else "0"
        cb = pb[i] if i < len(pb) else "0"
        try:
            na, nb = int(ca), int(cb)
            if na < nb:
                return -1
            if na > nb:
                return 1
        except ValueError:
            if ca < cb:
                return -1
            if ca > cb:
                return 1
    return 0


def classify_range_quality(ranges: list[dict]) -> RangeQuality:
    """Classify the quality of version range data."""
    ranges = _filter_comparable_ranges(ranges)
    if not ranges:
        return RangeQuality.NONE

    has_fix = any((r.get("fixed") and r["fixed"] != "*") or r.get("last_affected") for r in ranges)
    has_all_versions = any(r.get("fixed") == "*" for r in ranges)
    has_intro_only = any(r.get("introduced") and not r.get("fixed") and not r.get("last_affected") for r in ranges)

    if has_fix:
        return RangeQuality.HIGH
    if has_all_versions:
        return RangeQuality.ALL_VERSIONS
    if has_intro_only:
        return RangeQuality.LOW
    return RangeQuality.NONE


def is_affected(version: str, ranges: list[dict]) -> MatchResult:
    """Check if a version is affected by any version range.

    Processes ranges in quality tiers:
    - High-quality (has fixed/last_affected) checked first and trusted
    - All-versions (fixed=*) means no fix exists
    - Low-quality (introduced only) used only if no high-quality data

    Ranges with git SHA versions are filtered out (not comparable to
    RPM upstream versions). If all ranges were non-semver, returns
    a special flag so the caller can route to agent review.
    """
    original_count = len(ranges)
    ranges = _filter_comparable_ranges(ranges)
    filtered_count = original_count - len(ranges)

    if not ranges:
        return MatchResult(affected=False, quality=RangeQuality.NONE, flags=["no-ranges"])

    with_fix = [r for r in ranges if (r.get("fixed") and r["fixed"] != "*") or r.get("last_affected")]
    all_versions = [r for r in ranges if r.get("fixed") == "*"]
    intro_only = [r for r in ranges if r.get("introduced") and not r.get("fixed") and not r.get("last_affected")]

    # High-quality ranges first
    for r in with_fix:
        intro = r.get("introduced", "")
        fixed = r.get("fixed", "")
        last = r.get("last_affected", "")

        if intro and compare_versions(version, intro) < 0:
            continue

        if fixed:
            if compare_versions(version, fixed) < 0:
                return MatchResult(
                    affected=True,
                    fixed_version=fixed,
                    quality=RangeQuality.HIGH,
                )
            continue

        if last:
            if compare_versions(version, last) <= 0:
                return MatchResult(affected=True, quality=RangeQuality.HIGH)

    # If high-quality ranges say "not affected", trust them
    if with_fix:
        return MatchResult(affected=False, quality=RangeQuality.HIGH)

    # All versions affected, no fix available
    if all_versions:
        return MatchResult(
            affected=True,
            quality=RangeQuality.ALL_VERSIONS,
            flags=["no-fix-available"],
        )

    # Low-quality: introduced only
    for r in intro_only:
        intro = r.get("introduced", "")
        if intro and compare_versions(version, intro) >= 0:
            return MatchResult(
                affected=True,
                quality=RangeQuality.LOW,
                flags=["no-fixed-version"],
            )

    return MatchResult(affected=False, quality=RangeQuality.NONE)


def needs_agent(mapping_exists: bool, quality: RangeQuality) -> bool:
    """Determine whether this CVE needs agent review."""
    if not mapping_exists:
        return True
    if quality in (RangeQuality.HIGH, RangeQuality.ALL_VERSIONS):
        return False
    return True


def has_suspicious_version_jump(pkg_version: str, fixed_version: str) -> bool:
    """Detect if the fix version suggests a different project (name collision).

    E.g., package is 2.9.5 but fix is 4.9 — likely a WordPress plugin
    sharing the same name, not the same software.
    """
    if not fixed_version or not pkg_version:
        return False
    try:
        pkg_major = int(pkg_version.split(".")[0])
        fix_major = int(fixed_version.split(".")[0])
        # Fix is more than 2 major versions ahead of current — suspicious
        return fix_major > pkg_major + 2
    except (ValueError, IndexError):
        return False
