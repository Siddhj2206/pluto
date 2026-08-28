# Build Scripts

This directory contains build scripts used during image creation. The
Containerfile explicitly runs each script in order; extra scripts must be
explicitly added to the Containerfile.

## How It Works

Scripts are named with a number prefix (`10-`, `20-`, `25-`, `40-`) and run in
ascending order during the container build process. Each script is one
**layer**: overlays, wm-agnostic packages, multimedia, and the compositor.

## Included Scripts

- **`00-image-info.sh`** - Image identity (os-release / image-info.json), ARG-driven
- **`10-build.sh`** - Overlays + custom files: brew OCI layer, common `shared/` layer (ujust, flatpak-preinstall, brew-preinstall), `custom/files/` → `/`, `custom/config/` → `/etc/skel`, just consolidation, flatpak preinstall files; wm-agnostic
- **`20-base.sh`** - WM-agnostic desktop foundation packages (fonts, graphics, audio, portals, keyring, display manager, zram, power) from `packages/base.toml`; COPR sections installed per-repo; wm-agnostic
- **`25-multimedia.sh`** - Full multimedia (ffmpeg + non-FOSS codecs, mesa/VA overrides) from the negativo17 `fedora-multimedia` repo via `packages/multimedia.toml`; wm-agnostic
- **`40-niri.sh`** - Compositor layer: niri + DMS stack from `packages/niri.toml` + dynamic wiring (greeter, first-boot units, schemas); **wm-specific — renumber/replace for a different compositor**
- `clean-stage.sh`, `copr-helpers.sh` - Cleanup stage and COPR helpers
- `packages/` - TOML manifests (the "manifest of record": `base.toml`, `multimedia.toml`, `niri.toml`)
- `scripts/` - Shared helpers (`read-packages`, `package-lib.sh`)

## Creating Your Own Scripts

Create numbered scripts between the layers above (e.g. `30-…` for a
wm-agnostic app layer, `45-…` for compositor extras):

```bash
# 30-development.sh - Development tools (wm-agnostic)
# 45-niri-extra.sh   - Compositor-specific extras
```

### Script Template

```bash
#!/usr/bin/env bash
set -oue pipefail

echo "Running custom setup..."
# Your commands here
```

### Best Practices

- **One purpose per script**: Easier to debug and maintain
- **Package installs go in the TOML manifests**, not inline `dnf5 install`
  lines — see `packages/base.toml` for the format and `scripts/read-packages`
- **Keep composition swap-friendly**: wm-agnostic things in `10/20/25`,
  wm-specific things in `40-*` (+ `custom/config`, `custom/files`)
- **Clean up after yourself**: remove temporary files and disable temporary repos
- **Test incrementally**: one script at a time, then `just build`
- **Comment your code**: Future you will thank present you

### Disabling a Script

Remove its corresponding `RUN` block from `Containerfile` (or delete the script).

## Execution Order

The template runs scripts explicitly, rather than automatically discovering
files by prefix. Place extra script blocks between the existing layers and
before `clean-stage.sh`. Use numbered names to communicate the intended order.

## Notes

- Scripts run as root during build
- Build context is available at `/ctx` (`/ctx/build`, `/ctx/custom`)
- Use dnf5 for package management (not dnf or yum)
- Always use `-y` flag for non-interactive installs
- System-wide static files live in `custom/files/`; user config defaults in
  `custom/config/` — prefer those over heredocs in scripts