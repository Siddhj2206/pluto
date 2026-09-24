#!/usr/bin/env python3
"""Validate the factory contract, the allow-list, and the recipe layout."""

from __future__ import annotations

import sys

from factory import validate_all


def main() -> int:
    errors = validate_all()
    if errors:
        for error in errors:
            print(f"ERROR: {error}", file=sys.stderr)
        return 1
    print("factory configuration valid")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
