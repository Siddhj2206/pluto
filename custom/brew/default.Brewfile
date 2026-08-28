# pluto — default brew preinstalls (auto-installed at first login).
#
# Every Brewfile in this directory is copied to
# /usr/share/ublue-os/homebrew/preinstall.d/ and applied automatically by
# brew-preinstall.service (content-addressed by hash — edit to re-apply).
#
# Policy: only USER tools live here. System tools (build-time, systemd
# services, first boot before brew, desktop integration) stay in the base
# image (build/packages/base.toml) — e.g. ghostty is native on purpose.
#
# All entries verified in Homebrew core with Linux bottles, 2026-08-28.

# --- CLI tools (moved out of the base image) ---------------------------------
brew "htop"         # interactive process viewer
brew "nvtop"        # GPU process monitor
brew "fzf"          # fuzzy finder
brew "glow"         # markdown pager (used by the `changelogs` ujust recipe)
brew "zenity"       # GTK dialogs for scripts

# --- add more user tools below ------------------------------------------------
# brew "starship"    # shell prompt
# brew "btop"        # system monitor
# brew "gh"          # GitHub CLI
# brew "lazygit"     # git TUI
# brew "bun"
# brew "uv"
# brew "neovim"