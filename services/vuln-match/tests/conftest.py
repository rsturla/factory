"""Shared test fixtures."""

from __future__ import annotations

import sys
from pathlib import Path

# Ensure src/ is on path for imports
sys.path.insert(0, str(Path(__file__).parent.parent / "src"))
