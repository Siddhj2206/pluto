"""Tests for the package factory configuration and tools.

These run in CI on every change under `packages/`, so a broken allow-list, a
recipe with no entry, or a spec referencing a missing patch fails the PR.
"""

from __future__ import annotations

import json
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


def test_cache_key_is_stable_and_hex() -> None:
    key = factory.cache_key("mtdev")
    assert key == factory.cache_key("mtdev")
    assert len(key) == 64
    assert all(character in "0123456789abcdef" for character in key)


def test_plan_builds_everything_without_a_prior(tmp_path: Path) -> None:
    import plan

    packages = factory.load_allow_list()
    wave0 = [entry["name"] for entry in packages if entry["wave"] == 0]
    result = plan.plan_wave(0, tmp_path)
    assert sorted(result["build"]) == sorted(wave0)
    assert result["cached"] == []


def test_recipe_dir_finds_grouped_recipe() -> None:
    # Wave-0 recipes are grouped under core/, so resolution must search by name.
    assert factory.recipe_dir("mtdev").parent.name == "core"
    assert factory.spec_files("mtdev") == [factory.recipe_dir("mtdev") / "mtdev.spec"]


def test_manifest_records_and_prunes(tmp_path: Path) -> None:
    import manifest

    built = tmp_path / "built" / "rpm-mtdev"
    built.mkdir(parents=True)
    (built / "mtdev-1.1.6-14.hum1.pluto.x86_64.rpm").write_text("new")

    prior = tmp_path / "prior"
    prior.mkdir()
    (prior / factory.load_contract()["cache_manifest"]).write_text(
        json.dumps(
            {
                "schema": 1,
                "packages": {
                    "mtdev": {
                        "key": "old",
                        "rpms": ["mtdev-1.1.6-13.hum1.pluto.x86_64.rpm"],
                    }
                },
            }
        )
    )

    repo = tmp_path / "repo"
    repo.mkdir()
    (repo / "mtdev-1.1.6-13.hum1.pluto.x86_64.rpm").write_text("old")

    manifest.update_manifest(prior, tmp_path / "built", repo)

    assert not (repo / "mtdev-1.1.6-13.hum1.pluto.x86_64.rpm").exists()
    written = json.loads((repo / factory.load_contract()["cache_manifest"]).read_text())
    assert written["packages"]["mtdev"]["key"] == factory.cache_key("mtdev")
    assert written["packages"]["mtdev"]["rpms"] == [
        "mtdev-1.1.6-14.hum1.pluto.x86_64.rpm"
    ]
