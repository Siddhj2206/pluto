# Recipes

One directory per owned package:

```text
packages/packages/<name>/
├── <name>.spec                    # the RPM spec
├── <name>.hummingbird-upstream.json   # provenance: Fedora dist-git remote + commit
├── *.patch                        # optional patches referenced by the spec
└── sources                        # optional pinned source list
```

Rules:

- A recipe directory is ignored unless `<name>` appears in
  `packages/config/upstream-sources.json`.
- Subpackages live in the same spec as their main package; do not split them
  across directories.
- Record provenance in `<name>.hummingbird-upstream.json` when the spec is
  seeded from Fedora dist-git, so refreshes are reviewable:
  `{"remote": "...", "commit": "...", "tree": "...", "time": 0}`.
- Confirm redistribution rights before importing an upstream spec or tarball,
  and record the license in the allow-list entry.

No recipes yet.
