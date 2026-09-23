#!/usr/bin/env python3
"""Update the cache manifest after a build, and prune RPMs the build replaced.

Reads the prior manifest, records the new cache key and RPM set for every
package built in this run, removes any of that package's prior RPMs from the
output repository, and writes the merged manifest. Packages from other waves are
carried forward, so the repository accumulates across waves.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from factory import FactoryError, cache_key, load_contract, load_manifest


def built_map(built_dir: Path) -> dict[str, list[str]]:
    """Map package name to its RPM filenames from rpm-<name>/ artifact dirs."""
    result: dict[str, list[str]] = {}
    if not built_dir.is_dir():
        return result
    for directory in sorted(built_dir.iterdir()):
        if not directory.is_dir() or not directory.name.startswith("rpm-"):
            continue
        name = directory.name[len("rpm-") :]
        rpms = sorted(path.name for path in directory.glob("*.rpm"))
        if rpms:
            result[name] = rpms
    return result


def update_manifest(prior_dir: Path, built_dir: Path, output_dir: Path) -> dict:
    """Merge prior and newly built packages into the output manifest."""
    prior = load_manifest(prior_dir)
    contract = load_contract()
    built = built_map(built_dir)
    packages = dict(prior.get("packages", {}))
    for name, rpms in built.items():
        for stale in packages.get(name, {}).get("rpms", []):
            if stale not in rpms:
                (Path(output_dir) / stale).unlink(missing_ok=True)
        packages[name] = {"key": cache_key(name), "rpms": rpms}

    manifest = {
        "schema": 1,
        "buildroot": contract["build_container"],
        "packages": packages,
    }
    output = Path(output_dir)
    output.mkdir(parents=True, exist_ok=True)
    (output / contract["cache_manifest"]).write_text(
        json.dumps(manifest, indent=2, sort_keys=True) + "\n"
    )
    return manifest


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--prior", type=Path, required=True, help="prior repository dir")
    parser.add_argument("--built-dir", type=Path, required=True, help="rpm-<name>/ artifacts")
    parser.add_argument("--output", type=Path, required=True, help="repository to write into")
    args = parser.parse_args()

    try:
        manifest = update_manifest(args.prior, args.built_dir, args.output)
    except FactoryError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 1

    print(f"manifest: {len(manifest['packages'])} package(s)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
