#!/usr/bin/env python3
"""Fetch allow-listed sources and verify their sha512, failing closed.

The build workflow stages sources with this before `rpmbuild`, so a source that
changed upstream (or a bad mirror) stops the build instead of shipping.
"""

from __future__ import annotations

import argparse
import hashlib
import sys
import urllib.error
import urllib.request
from pathlib import Path

from factory import FactoryError, entry_for, load_allow_list


def sha512_of(path: Path) -> str:
    digest = hashlib.sha512()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _download(url: str, dest: Path) -> None:
    request = urllib.request.Request(url, headers={"User-Agent": "pluto-source-pipeline"})
    with urllib.request.urlopen(request, timeout=120) as response:  # noqa: S310 - allow-listed URLs
        dest.write_bytes(response.read())


def fetch(entry: dict, output: Path) -> Path:
    """Download an entry's source and verify its sha512, trying fallbacks."""
    output.mkdir(parents=True, exist_ok=True)
    dest = output / entry["filename"]
    urls = [entry["url"], *entry.get("fallback_urls", [])]
    last_error: Exception | None = None
    for url in urls:
        try:
            _download(url, dest)
        except (urllib.error.URLError, TimeoutError, OSError) as exc:
            last_error = exc
            continue
        if sha512_of(dest) == entry["sha512"]:
            return dest
        last_error = FactoryError(f"{entry['name']}: sha512 mismatch for {url}")
    dest.unlink(missing_ok=True)
    raise FactoryError(f"{entry['name']}: could not fetch a verified source ({last_error})")


def verify_staged(entry: dict, staged: Path) -> None:
    path = staged / entry["filename"]
    if not path.exists():
        raise FactoryError(f"{entry['name']}: {path} is not staged")
    if sha512_of(path) != entry["sha512"]:
        raise FactoryError(f"{entry['name']}: staged {path} has the wrong sha512")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("package", nargs="?", help="allow-listed package name")
    parser.add_argument("--all", action="store_true", help="process every allow-listed package")
    parser.add_argument("--wave", type=int, help="process every package in a wave")
    parser.add_argument("--output", type=Path, help="directory to fetch verified sources into")
    parser.add_argument(
        "--verify-staged", type=Path, help="verify already-staged sources instead of fetching"
    )
    args = parser.parse_args()

    try:
        packages = load_allow_list()
    except FactoryError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 1

    if args.package:
        selected = [entry_for(packages, args.package)]
    elif args.wave is not None:
        selected = [entry for entry in packages if entry["wave"] == args.wave]
    elif args.all:
        selected = packages
    else:
        parser.error("give a package name, --wave, or --all")

    if not selected:
        print("no packages selected", file=sys.stderr)
        return 1

    try:
        for entry in selected:
            if args.verify_staged is not None:
                verify_staged(entry, args.verify_staged)
                print(f"verified {entry['name']}")
            else:
                if args.output is None:
                    parser.error("--output is required when fetching")
                print(f"fetched {fetch(entry, args.output)}")
    except FactoryError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
