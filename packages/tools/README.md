# Tools

Helper programs the factory and its CI call. None exist yet; this is the planned
set, adapted from the Kestrel factory (Apache-2.0 — keep attribution in file
headers when porting).

Planned:

- **`source_pipeline.py`** — fetch each allow-list source and verify its
  `sha512` against the recorded value. Fail closed.
- **`audit_sources.py`** — prove every `SourceN`/`PatchN` a spec references can
  be resolved before `rpmbuild` runs.
- **`validate.py`** — schema-check `config/upstream-sources.json` and
  `config/factory-contract.json`, and check each spec has a matching entry.
- **`spec_source_alias.py`** — keep spec `Source` names aligned with the
  allow-list filenames.
- **`sync_versions.py`** — carry Renovate version bumps from the allow-list into
  the specs.
- **`fetch_vendored.py`** — resolve vendored/cargo dependencies for offline
  builds.

Add each when the first recipe that needs it lands, not before.
