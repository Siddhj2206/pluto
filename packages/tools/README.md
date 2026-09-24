# Tools

Stdlib-only Python that the factory and its CI call. No third-party
dependencies, so the validation job installs nothing.

- **`factory.py`** — the single reader of `config/factory-contract.json` and
  `config/upstream-sources.json`, plus the layout cross-check the tests use.
- **`validate.py`** — `just packages-validate` / CI: contract, allow-list schema,
  and that every entry has exactly one recipe spec (and vice versa).
- **`audit_sources.py`** — proves every local `SourceN`/`PatchN` a spec
  references exists in its recipe directory, and that a recipe's `sources` file
  agrees with the allow-list sha512.
- **`source_pipeline.py`** — fetches each allow-listed source (including
  `extra_sources` such as detached signatures) and verifies its sha512, trying
  `fallback_urls`; used by the build workflow before `rpmbuild`.
- **`packages.py`** — lists a dependency wave, used to compute the CI matrix.
- **`cache_key.py`** — prints a package's content-hash cache key.
- **`plan.py`** — splits a wave into packages to build and packages to reuse,
  using the published cache manifest.
- **`manifest.py`** — merges prior and newly built packages into
  `.pluto-cache.json` and prunes RPMs a rebuild replaced.
- **`build-in-container.sh`** — the in-container rpmbuild driver (buildroot
  repos, source staging, generated buildrequires, `rpmbuild -ba`).

Planned, added when the first recipe needs them: `sync_versions.py` (carry
Renovate version bumps from the allow-list into specs) and `spec_source_alias.py`
(keep spec `Source` names aligned with allow-list filenames).

Adapted from the Kestrel factory (`pigeon/`, Apache-2.0).
