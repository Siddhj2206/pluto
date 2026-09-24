#!/usr/bin/env python3
"""List allow-listed packages, optionally filtered by dependency wave.

The build workflow uses this to compute its matrix, so the allow-list stays the
single definition of what exists.
"""

from __future__ import annotations

import argparse
import json
import sys

from factory import FactoryError, load_allow_list


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--wave", type=int, help="only packages in this wave")
    parser.add_argument("--json", action="store_true", help="emit a JSON array")
    args = parser.parse_args()

    try:
        packages = load_allow_list()
    except FactoryError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 1

    if args.wave is not None:
        packages = [entry for entry in packages if entry["wave"] == args.wave]

    names = sorted(entry["name"] for entry in packages)
    print(json.dumps(names) if args.json else "\n".join(names))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
