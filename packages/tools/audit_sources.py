#!/usr/bin/env python3
"""Audit that every SourceN/PatchN a spec references can be resolved.

Remote sources are staged and checksum-verified by `source_pipeline.py`; this
tool only proves the *local* references (patches, keys, bare source files) exist
in the recipe directory, and that a recipe's `sources` file agrees with the
allow-list sha512 for its main tarball. It catches the class of mistake that
otherwise fails deep inside `rpmbuild`.
"""

from __future__ import annotations

import re
import sys

from factory import FactoryError, load_allow_list, recipe_dir, spec_files

# Source0: ... / Patch0: ...
SOURCE_RE = re.compile(r"^(Source|Patch)\d*\s*:\s*(\S+)", re.MULTILINE)
# SHA512 (filename) = hash   (a `sources` file line)
SOURCES_LINE_RE = re.compile(r"^SHA512\s+\((\S+)\)\s*=\s*([0-9a-f]+)\s*$", re.MULTILINE)


def _is_remote(ref: str) -> bool:
    return ref.startswith(("http://", "https://", "ftp://"))


def audit_package(entry: dict) -> list[str]:
    name = entry["name"]
    directory = recipe_dir(name)
    errors: list[str] = []

    for spec in spec_files(name):
        text = spec.read_text(errors="replace")
        for kind, ref in SOURCE_RE.findall(text):
            ref = ref.strip()
            if _is_remote(ref) or ref.startswith("%"):
                continue
            if not (directory / ref).exists():
                errors.append(f"{name}: {kind} references missing file {ref}")

    sources_file = directory / "sources"
    if sources_file.exists():
        for filename, digest in SOURCES_LINE_RE.findall(sources_file.read_text(errors="replace")):
            if filename == entry["filename"] and digest != entry["sha512"]:
                errors.append(
                    f"{name}: sources file sha512 for {filename} disagrees with the allow-list"
                )
    return errors


def main() -> int:
    try:
        packages = load_allow_list()
    except FactoryError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 1

    errors: list[str] = []
    for entry in packages:
        errors.extend(audit_package(entry))

    if errors:
        for error in errors:
            print(f"ERROR: {error}", file=sys.stderr)
        return 1
    print(f"spec sources resolvable for {len(packages)} package(s)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
