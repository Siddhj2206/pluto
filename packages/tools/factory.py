"""Shared loading and validation for the pluto package factory.

The factory contract (`packages/config/factory-contract.json`) and the allow-list
(`packages/config/upstream-sources.json`) are the front door: a recipe that is not
allow-listed must not build or publish. This module is the single reader of both,
so the CLI tools and the tests agree on what "valid" means.

Stdlib only: CI runs these with the runner's Python and installs nothing.
"""

from __future__ import annotations

import hashlib
import json
import re
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
PACKAGES_DIR = REPO_ROOT / "packages"
CONFIG_DIR = PACKAGES_DIR / "config"
CONTRACT_PATH = CONFIG_DIR / "factory-contract.json"
ALLOW_LIST_PATH = CONFIG_DIR / "upstream-sources.json"
RECIPES_DIR = PACKAGES_DIR / "packages"

SHA512_RE = re.compile(r"^[0-9a-f]{128}$")

REQUIRED_CONTRACT_KEYS = (
    "contract",
    "registry",
    "rpm_suffix",
    "allow_list",
    "packages_dir",
    "base_image",
    "build_container",
    "build_script",
    "cache_manifest",
    "target_arch",
)

REQUIRED_ENTRY_KEYS = ("name", "wave", "version", "url", "filename", "sha512")


class FactoryError(Exception):
    """A factory configuration problem worth failing a build over."""


def _load_json(path: Path) -> dict:
    try:
        with path.open() as handle:
            return json.load(handle)
    except FileNotFoundError as exc:
        raise FactoryError(f"missing file: {path}") from exc
    except json.JSONDecodeError as exc:
        raise FactoryError(f"invalid JSON in {path}: {exc}") from exc


def load_contract() -> dict:
    """Load and validate the factory contract."""
    contract = _load_json(CONTRACT_PATH)
    missing = [key for key in REQUIRED_CONTRACT_KEYS if not contract.get(key)]
    if missing:
        raise FactoryError(f"{CONTRACT_PATH}: missing keys: {', '.join(missing)}")
    return contract


def load_allow_list() -> list[dict]:
    """Load and validate the allow-list, returning its package entries."""
    document = _load_json(ALLOW_LIST_PATH)
    if document.get("schema") != 1:
        raise FactoryError(f"{ALLOW_LIST_PATH}: schema must be 1")
    packages = document.get("packages")
    if not isinstance(packages, list):
        raise FactoryError(f"{ALLOW_LIST_PATH}: 'packages' must be a list")

    errors: list[str] = []
    seen: set[str] = set()
    for index, entry in enumerate(packages):
        where = f"{ALLOW_LIST_PATH}: packages[{index}]"
        if not isinstance(entry, dict):
            errors.append(f"{where}: not an object")
            continue
        missing = [key for key in REQUIRED_ENTRY_KEYS if entry.get(key) in (None, "")]
        if missing:
            errors.append(f"{where}: missing keys: {', '.join(missing)}")
        name = entry.get("name")
        if name in seen:
            errors.append(f"{where}: duplicate name {name!r}")
        seen.add(name)
        if not isinstance(entry.get("wave"), int) or entry.get("wave", -1) < 0:
            errors.append(f"{where}: 'wave' must be a non-negative integer")
        sha512 = entry.get("sha512", "")
        if not SHA512_RE.match(sha512):
            errors.append(f"{where}: 'sha512' must be 128 lowercase hex characters")
        if not str(entry.get("url", "")).startswith(("http://", "https://", "ftp://")):
            errors.append(f"{where}: 'url' must be an http(s)/ftp URL")
        for fallback in entry.get("fallback_urls", []) or []:
            if not str(fallback).startswith(("http://", "https://", "ftp://")):
                errors.append(f"{where}: fallback URL must be http(s)/ftp: {fallback!r}")
        for extra in entry.get("extra_sources", []) or []:
            if not isinstance(extra, dict):
                errors.append(f"{where}: extra_sources entries must be objects")
                continue
            missing_extra = [key for key in ("url", "filename", "sha512") if not extra.get(key)]
            if missing_extra:
                errors.append(f"{where}: extra_sources entry missing: {', '.join(missing_extra)}")
            if not SHA512_RE.match(extra.get("sha512", "")):
                errors.append(f"{where}: extra_sources sha512 must be 128 lowercase hex characters")
            if not str(extra.get("url", "")).startswith(("http://", "https://", "ftp://")):
                errors.append(f"{where}: extra_sources url must be http(s)/ftp")
    if errors:
        raise FactoryError("\n".join(errors))
    return packages


def entry_for(packages: list[dict], name: str) -> dict:
    for entry in packages:
        if entry["name"] == name:
            return entry
    raise FactoryError(f"{name!r} is not in the allow-list")


def recipe_dir(name: str) -> Path:
    return RECIPES_DIR / name


def spec_files(name: str) -> list[Path]:
    directory = recipe_dir(name)
    if not directory.is_dir():
        return []
    return sorted(directory.glob("*.spec"))


def _sha256_file(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def recipe_file_hashes(name: str) -> list[list[str]]:
    """Every file in a recipe directory as [relative path, sha256], sorted."""
    directory = recipe_dir(name)
    files: list[list[str]] = []
    for path in sorted(directory.rglob("*")):
        if path.is_file():
            files.append([str(path.relative_to(directory)), _sha256_file(path)])
    return files


def cache_key(name: str) -> str:
    """A content hash of everything that can change a package's build output.

    Spec, patches, keys, pinned sources, the buildroot, the disttag, and the
    build script. Change any of them and the key changes, so the package
    rebuilds; otherwise the published RPMs are reused.
    """
    contract = load_contract()
    entry = entry_for(load_allow_list(), name)
    build_script = REPO_ROOT / contract["build_script"]
    payload = {
        "name": entry["name"],
        "version": entry["version"],
        "sha512": entry["sha512"],
        "extra_sources": sorted(
            extra["sha512"] for extra in entry.get("extra_sources", []) or []
        ),
        "recipe_files": recipe_file_hashes(name),
        "buildroot": contract["build_container"],
        "rpm_suffix": contract["rpm_suffix"],
        "build_script": _sha256_file(build_script),
    }
    canonical = json.dumps(payload, sort_keys=True, separators=(",", ":"))
    return hashlib.sha256(canonical.encode()).hexdigest()


def manifest_path(repo_dir: Path) -> Path:
    return Path(repo_dir) / load_contract()["cache_manifest"]


def load_manifest(repo_dir: Path) -> dict:
    """The cache manifest from a repository directory, or an empty one."""
    path = manifest_path(repo_dir)
    if not path.is_file():
        return {"schema": 1, "packages": {}}
    try:
        with path.open() as handle:
            manifest = json.load(handle)
    except (OSError, json.JSONDecodeError) as exc:
        raise FactoryError(f"{path}: unreadable manifest: {exc}") from exc
    if not isinstance(manifest.get("packages"), dict):
        raise FactoryError(f"{path}: 'packages' must be an object")
    return manifest


def validate_layout() -> list[str]:
    """Cross-check the allow-list against the recipe directories.

    Every allow-listed package needs exactly one recipe directory with exactly
    one spec, and every recipe directory needs an allow-list entry. Returns a
    list of human-readable errors (empty means valid).
    """
    errors: list[str] = []
    packages = load_allow_list()
    names = {entry["name"] for entry in packages}

    for entry in packages:
        name = entry["name"]
        specs = spec_files(name)
        if not specs:
            errors.append(f"{name}: no .spec under {recipe_dir(name)}")
        elif len(specs) > 1:
            errors.append(f"{name}: more than one .spec: {[s.name for s in specs]}")

    if RECIPES_DIR.is_dir():
        for child in sorted(RECIPES_DIR.iterdir()):
            if child.is_dir() and child.name not in names:
                errors.append(f"{child}: recipe directory has no allow-list entry")
    return errors


def validate_all() -> list[str]:
    """Return every configuration error, or an empty list when the factory is valid."""
    errors: list[str] = []
    try:
        load_contract()
    except FactoryError as exc:
        errors.append(str(exc))
    try:
        errors.extend(validate_layout())
    except FactoryError as exc:
        errors.append(str(exc))
    return errors
