#!/usr/bin/env python3
"""Print the content-hash cache key for one or all allow-listed packages."""

from __future__ import annotations

import argparse
import json
import sys

from factory import FactoryError, cache_key, load_allow_list


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("package", nargs="?", help="allow-listed package name")
    parser.add_argument("--all", action="store_true", help="every allow-listed package")
    parser.add_argument("--json", action="store_true", help="emit a JSON object")
    args = parser.parse_args()

    try:
        packages = load_allow_list()
    except FactoryError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 1

    if args.all:
        keys = {entry["name"]: cache_key(entry["name"]) for entry in packages}
        if args.json:
            print(json.dumps(keys, indent=2, sort_keys=True))
        else:
            for name, key in sorted(keys.items()):
                print(f"{key}  {name}")
        return 0

    if not args.package:
        parser.error("give a package name or --all")
    print(cache_key(args.package))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
