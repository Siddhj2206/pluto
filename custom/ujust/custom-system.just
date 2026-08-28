# vim: set ft=make :
#######################
### custom-system.just
#######################
## Example system configuration commands
## These are simplified examples adapted from Bluefin

# Run a system benchmark (requires stress-ng from Homebrew)
[group('System')]
benchmark:
    #!/usr/bin/env bash
    source /usr/lib/ujust/ujust.sh
    if ! type -P "stress-ng" &>/dev/null ; then
        echo "stress-ng is not installed."
        if command -v brew &>/dev/null; then
            if gum confirm "Install stress-ng via Homebrew?" ; then
                brew install stress-ng
                brew link stress-ng
            else
                exit 0
            fi
        else
            echo "Please install stress-ng to run benchmarks."
            exit 1
        fi
    fi
    echo 'Running a 1 minute benchmark...'
    pushd $(mktemp -d) > /dev/null
    stress-ng --matrix 0 -t 1m --times
    popd > /dev/null

# Configure docker and libvirt groups for development
[group('System')]
configure-dev-groups:
    #!/usr/bin/pkexec bash
    CURRENT_USER="{{ `id -un` }}"
    echo "Adding $CURRENT_USER to docker and libvirt groups..."

    # Ensure groups exist in /etc/group
    for group in docker libvirt; do
        if ! grep -q "^$group:" /etc/group; then
            echo "Adding $group to /etc/group"
            grep "^$group:" /usr/lib/group | tee -a /etc/group > /dev/null
        fi
        usermod -aG $group $CURRENT_USER
    done

    echo "Groups configured. Log out and back in for changes to take effect."

# Example toggle command with user choice
[group('System')]
toggle-example-feature:
    #!/usr/bin/bash
    source /usr/lib/ujust/ujust.sh
    echo "This is an example toggle command."
    echo "Current status: [check your status here]"
    OPTION=$(Choose "Enable" "Disable" "Cancel")
    case "$OPTION" in
        "Enable")
            echo "Enabling feature..."
            # Add your enable logic here
            ;;
        "Disable")
            echo "Disabling feature..."
            # Add your disable logic here
            ;;
        "Cancel")
            echo "No changes made."
            ;;
    esac

# Clean up container images and volumes
[group('Maintenance')]
clean-containers:
    #!/usr/bin/bash
    echo "Cleaning up Podman containers, images, and volumes..."
    podman system prune -af
    podman volume prune -f
    echo "Cleanup complete!"

# Update system and reboot if needed
[group('Maintenance')]
update-and-reboot:
    #!/usr/bin/bash
    source /usr/lib/ujust/ujust.sh
    echo "Updating system..."
    sudo bootc upgrade
    if gum confirm "Reboot now to apply updates?"; then
        systemctl reboot
    else
        echo "Reboot later to apply updates."
    fi

# Restore DMS configs from system defaults (skel) to this user's home.
# Existing files are backed up with a timestamp before being replaced.
# Ported from siddhj2206/neptuno.
install-dms-config:
    #!/usr/bin/bash
    set -euo pipefail

    SKEL_CONFIG="${SKEL_CONFIG:-/etc/skel/.config}"
    USER_CONFIG="${USER_CONFIG:-${HOME}/.config}"
    TIMESTAMP=$(date +"%Y-%m-%d_%H-%M-%S")

    bold="$(tput bold 2>/dev/null || printf '\033[1m')"
    normal="$(tput sgr0 2>/dev/null || printf '\033[0m')"

    if [[ ! -d "$SKEL_CONFIG" ]]; then
        echo "Error: $SKEL_CONFIG not found"
        exit 1
    fi

    echo "${bold}DMS Config Restore${normal}"
    echo "Restores DMS configs from system defaults to your home."
    echo "Existing files are backed up before being replaced."
    echo ""

    CONFLICTS=()
    while IFS= read -r -d '' file; do
        rel_path="${file#$SKEL_CONFIG/}"
        target="$USER_CONFIG/$rel_path"
        if [[ -e "$target" || -L "$target" ]]; then
            CONFLICTS+=("$rel_path")
        fi
    done < <(find "$SKEL_CONFIG" -type f -print0)

    if [[ ${#CONFLICTS[@]} -gt 0 ]]; then
        echo "The following files will be backed up:"
        for f in "${CONFLICTS[@]}"; do
            echo "  - $f"
        done
        echo ""
    fi

    if command -v gum >/dev/null 2>&1; then
        if ! gum confirm "Proceed with restore?"; then
            echo "Cancelled."
            exit 0
        fi
    else
        echo "gum not found — proceeding without confirmation."
    fi

    BACKED_UP=()
    INSTALLED=()

    while IFS= read -r -d '' file; do
        rel_path="${file#$SKEL_CONFIG/}"
        target="$USER_CONFIG/$rel_path"
        target_dir=$(dirname "$target")

        if ! mkdir -p "$target_dir" 2>/dev/null; then
            echo "  WARN: cannot create $target_dir — skipping $rel_path"
            continue
        fi

        if [[ -e "$target" || -L "$target" ]]; then
            backup="${target}.backup.${TIMESTAMP}"
            n=1
            while [[ -e "$backup" || -L "$backup" ]]; do
                backup="${target}.backup.${TIMESTAMP}.$((n++))"
            done
            mv "$target" "$backup"
            BACKED_UP+=("$rel_path")
        fi

        cp "$file" "$target"
        INSTALLED+=("$rel_path")
    done < <(find "$SKEL_CONFIG" -type f -print0)

    echo ""
    echo "${bold}=== Restore Complete ===${normal}"
    echo ""
    echo "Restored ${#INSTALLED[@]} files"

    if [[ ${#BACKED_UP[@]} -gt 0 ]]; then
        echo ""
        echo "Backups created:"
        for b in "${BACKED_UP[@]}"; do
            echo "  $b"
        done
    fi

# Show release changelogs for the running image (bctl first, else GitHub).
# Ported from siddhj2206/neptuno (repo adjusted for pluto).
changelogs:
    #!/usr/bin/bash
    set -euo pipefail
    if command -v bctl &>/dev/null; then
        exec bctl changelogs
    fi

    IMAGE_INFO_FILE="${IMAGE_INFO_FILE:-/usr/share/ublue-os/image-info.json}"
    TAG="$(jq -r '.["image-tag"]' < "${IMAGE_INFO_FILE}")"
    REPO="siddhj2206/pluto"

    DATE="$(grep -oP "OSTREE_VERSION=.*\d{2}\.\K\d{8}[.0-9]*" /etc/os-release 2>/dev/null || true)"
    CONTENT=""
    if [[ "$TAG" =~ gts$|stable$|^lts ]] && [[ -n "${DATE:-}" ]]; then
        CONTENT="$(curl -Ls "https://api.github.com/repos/${REPO}/releases" | jq -r ".[] | select(.tag_name==\"${TAG}-${DATE}\") | .body")"
    fi

    if [[ -n "${CONTENT:-}" ]]; then
        echo "$CONTENT" | glow -p
    else
        echo "WARN: Could not find a release specific to your image"
        curl -Ls "https://api.github.com/repos/${REPO}/releases/latest" | jq -r ".body" | glow -p
    fi
