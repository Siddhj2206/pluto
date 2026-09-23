#!/usr/bin/env python3
"""Decide which packages in a wave need rebuilding, using the cache manifest.

A package is reused when the published manifest records the same cache key and
every RPM it lists is present in the prior repository. Everything else builds.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from factory import FactoryError, cache_key, load_allow_list, load_manifest


def plan_wave(wave: int, prior_dir: Path) -> dict[str, list[str]]:
    """Split a wave into packages to build and packages to reuse."""
    packages = load_allow_list()
    prior = load_manifest(prior_dir).get("packages", {})
    build: list[str] = []
    cached: list[str] = []
    for entry in packages:
        if entry["wave"] != wave:
            continue
        name = entry["name"]
        record = prior.get(name)
        if (
            record
            and record.get("key") == cache_key(name)
            and record.get("rpms")
            and all((Path(prior_dir) / rpm).is_file() for rpm in record["rpms"])
        ):
            cached.append(name)
        else:
            build.append(name)
    return {"build": sorted(build), "cached": sorted(cached)}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--wave", type=int, required=True)
    parser.add_argument("--prior", type=Path, required=True, help="prior repository dir")
    args = parser.parse_args()

    try:
        result = plan_wave(args.wave, args.prior)
    except FactoryError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 1

    print(json.dumps(result))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
