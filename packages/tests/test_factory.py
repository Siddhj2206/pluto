"""Tests for the package factory configuration and tools.

These run in CI on every change under `packages/`, so a broken allow-list, a
recipe with no entry, or a spec referencing a missing patch fails the PR.
"""

from __future__ import annotations

import sys
from pathlib import Path

import pytest

TOOLS = Path(__file__).resolve().parents[1] / "tools"
sys.path.insert(0, str(TOOLS))

import factory  # noqa: E402
import audit_sources  # noqa: E402


def test_contract_loads() -> None:
    contract = factory.load_contract()
    assert contract["contract"] == "pluto-factory"
    assert contract["registry"].endswith("/pluto-packages")
    assert contract["rpm_suffix"] == ".hum1.pluto"


def test_allow_list_loads() -> None:
    packages = factory.load_allow_list()
    assert packages, "the allow-list is empty"
    names = [entry["name"] for entry in packages]
    assert len(names) == len(set(names)), "duplicate allow-list names"


def test_every_entry_has_exactly_one_spec() -> None:
    for entry in factory.load_allow_list():
        specs = factory.spec_files(entry["name"])
        assert len(specs) == 1, f"{entry['name']}: expected one spec, found {specs}"


def test_layout_is_valid() -> None:
    assert factory.validate_all() == []


def test_spec_sources_resolvable() -> None:
    errors = []
    for entry in factory.load_allow_list():
        errors.extend(audit_sources.audit_package(entry))
    assert errors == []


def test_sha512_rejects_non_hex() -> None:
    with pytest.raises(factory.FactoryError):
        # A 128-char string that is not lowercase hex.
        factory.SHA512_RE.match("z" * 128) or (_ for _ in ()).throw(factory.FactoryError("bad"))


def test_entry_for_unknown_name_raises() -> None:
    packages = factory.load_allow_list()
    with pytest.raises(factory.FactoryError):
        factory.entry_for(packages, "definitely-not-a-package")
