#!/usr/bin/env python3
"""Dependency-free host check for display snapshot fixtures and edge cases."""

from __future__ import annotations

import json
import pathlib
import sys


def validate_window(value: object, path: str) -> None:
    if value is None:
        return
    if not isinstance(value, dict):
        raise ValueError(f"{path} must be object or null")
    used = value.get("usedPercent")
    remaining = value.get("remainingPercent", 100 - used if isinstance(used, (int, float)) else None)
    if not isinstance(used, (int, float)) or not 0 <= used <= 100:
        raise ValueError(f"{path}.usedPercent invalid")
    if not isinstance(remaining, (int, float)) or not 0 <= remaining <= 100:
        raise ValueError(f"{path}.remainingPercent invalid")


def validate(document: object) -> None:
    if not isinstance(document, dict) or document.get("schemaVersion") != 1:
        raise ValueError("unsupported schemaVersion")
    providers = document.get("providers", {})
    if not isinstance(providers, dict):
        raise ValueError("providers must be object")
    for name in ("codex", "claude"):
        provider = providers.get(name) or {}
        if not isinstance(provider, dict):
            raise ValueError(f"providers.{name} must be object")
        if provider.get("freshness", "unavailable") not in ("fresh", "stale", "unavailable"):
            raise ValueError(f"providers.{name}.freshness invalid")
        windows = provider.get("windows", {}) or {}
        validate_window(windows.get("fiveHour"), f"providers.{name}.windows.fiveHour")
        validate_window(windows.get("sevenDay"), f"providers.{name}.windows.sevenDay")


def main() -> int:
    fixtures = pathlib.Path(__file__).parents[1] / "tests" / "fixtures"
    good = json.loads((fixtures / "snapshot.json").read_text(encoding="utf-8"))
    validate(good)
    invalid = dict(good)
    invalid["schemaVersion"] = 2
    try:
        validate(invalid)
    except ValueError:
        pass
    else:
        raise AssertionError("schemaVersion 2 was accepted")
    print("snapshot fixture: OK")
    return 0


if __name__ == "__main__":
    sys.exit(main())
