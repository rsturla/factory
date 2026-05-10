"""Parse and format Go time.Duration strings."""

from __future__ import annotations

import re
from datetime import timedelta

_UNITS = {
    "ns": 1e-9,
    "us": 1e-6,
    "µs": 1e-6,
    "ms": 1e-3,
    "s": 1.0,
    "m": 60.0,
    "h": 3600.0,
}

_PATTERN = re.compile(r"(-?)(\d+(?:\.\d+)?)(ns|µs|us|ms|s|m|h)")


def parse_duration(s: str) -> timedelta:
    if not s or s == "0" or s == "0s":
        return timedelta()

    negative = s.startswith("-")
    remaining = s.lstrip("-")
    total_seconds = 0.0
    pos = 0

    for m in _PATTERN.finditer(remaining):
        if m.start() != pos:
            raise ValueError(f"invalid duration: {s!r}")
        value = float(m.group(2))
        unit = m.group(3)
        total_seconds += value * _UNITS[unit]
        pos = m.end()

    if pos != len(remaining):
        raise ValueError(f"invalid duration: {s!r}")

    if negative:
        total_seconds = -total_seconds

    return timedelta(seconds=total_seconds)


def format_duration(d: timedelta) -> str:
    total = d.total_seconds()
    if total == 0:
        return "0s"

    negative = total < 0
    total = abs(total)

    parts: list[str] = []

    hours = int(total // 3600)
    if hours:
        parts.append(f"{hours}h")
        total -= hours * 3600

    minutes = int(total // 60)
    if minutes or hours:
        parts.append(f"{minutes}m")
        total -= minutes * 60

    has_higher = bool(hours or minutes)

    if total > 0 or not parts:
        if not has_higher and total < 1e-6:
            ns = total * 1e9
            parts.append(f"{ns:g}ns")
        elif not has_higher and total < 1e-3:
            us = total * 1e6
            parts.append(f"{us:g}µs")
        elif not has_higher and total < 1:
            ms = total * 1e3
            parts.append(f"{ms:g}ms")
        else:
            parts.append(f"{total:g}s")
    elif has_higher:
        parts.append("0s")

    result = "".join(parts)
    return f"-{result}" if negative else result
