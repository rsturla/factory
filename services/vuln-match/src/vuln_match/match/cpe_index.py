"""CPE dictionary loader and lookup."""

from __future__ import annotations

import json
import logging
from pathlib import Path

logger = logging.getLogger(__name__)

CpeEntry = tuple[str, str, str]  # (vendor, product, version_pattern)


class CpeIndex:
    """In-memory CPE dictionary for name resolution.

    Maps lowercase product names to (vendor, product, version_pattern) tuples.
    Built from NVD CPE dictionary export.
    """

    def __init__(self, entries: dict[str, CpeEntry] | None = None) -> None:
        self._entries: dict[str, CpeEntry] = entries or {}

    @classmethod
    def load(cls, path: str | Path) -> CpeIndex:
        path = Path(path)
        if not path.exists():
            logger.warning("CPE index not found at %s", path)
            return cls()

        with open(path) as f:
            raw = json.load(f)

        entries = {}
        for key, value in raw.items():
            if isinstance(value, list) and len(value) >= 2:
                entries[key.lower()] = (value[0], value[1], value[2] if len(value) > 2 else "")
        logger.info("loaded CPE index: %d entries", len(entries))
        return cls(entries)

    def lookup(self, name: str) -> CpeEntry | None:
        return self._entries.get(name.lower())

    def product_name(self, name: str) -> str | None:
        entry = self.lookup(name)
        if entry:
            return entry[1].lower()
        return None

    def __len__(self) -> int:
        return len(self._entries)

    def __contains__(self, name: str) -> bool:
        return name.lower() in self._entries
