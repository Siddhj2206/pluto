# Recipes

One directory per owned package:

```text
packages/packages/<name>/
├── <name>.spec                        # the RPM spec
├── .hummingbird-upstream.json         # provenance: Fedora dist-git remote + commit
├── *.patch                            # optional patches referenced by the spec
├── sources                            # pinned sha512 lines for the main tarball
└── *.asc                              # optional source-verification keys
```

Rules:

- A recipe directory is ignored unless `<name>` appears in
  `packages/config/upstream-sources.json`.
- Subpackages live in the same spec as their main package; do not split them
  across directories.
- Keep `.hummingbird-upstream.json` up to date when a spec is refreshed from
  Fedora dist-git, so the change is reviewable.
- Confirm redistribution rights before importing an upstream spec or tarball,
  and record the license in the allow-list entry.

Wave 0 is seeded from `projectbluefin/utah-packages` (Apache-2.0) and Fedora
dist-git. See `docs/research/pluto-closure-manifest.md` for the full recipe list
and build order.
