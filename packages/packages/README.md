# Recipes

One directory per owned package, grouped under a folder for readability:

```text
packages/packages/
├── core/<name>/<name>.spec            # the base graphics/wayland/input stack
├── niri/<name>/<name>.spec            # (future) the compositor
└── multimedia/<name>/<name>.spec      # (future)
```

A recipe directory may be nested at any depth; its identity is the directory
name, not the folder it sits in, so a recipe can be regrouped without changing
its cache key. Each recipe holds:

```text
<name>/
├── <name>.spec                        # the RPM spec
├── .hummingbird-upstream.json         # provenance: Fedora dist-git remote + commit
├── *.patch                            # optional patches referenced by the spec
├── sources                            # pinned sha512 lines for the main tarball
└── *.asc                              # optional source-verification keys
```

Rules:

- A recipe directory is ignored unless its name appears in
  `packages/config/upstream-sources.json`.
- Subpackages live in the same spec as their main package; do not split them
  across directories.
- Keep `.hummingbird-upstream.json` up to date when a spec is refreshed from
  Fedora dist-git, so the change is reviewable.
- Confirm redistribution rights before importing an upstream spec or tarball,
  and record the license in the allow-list entry.

Wave 0 lives under `core/`, seeded from `projectbluefin/utah-packages`
(Apache-2.0) and Fedora dist-git. See
`docs/research/pluto-closure-manifest.md` for the full recipe list and build
order.
