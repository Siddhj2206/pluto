#!/usr/bin/env bash

set -euo pipefail

###############################################################################
# Bootstrap
###############################################################################
# Hummingbird's bootc-os base carries no desktop and no rsync, but the overlay
# phase below uses rsync to apply the Common and Brew runtime files. Install it
# from pluto's own factory before that phase runs.
#
# This is the first phase that consumes the factory repository image, which the
# Containerfile bind-mounts read-only at /var/pluto-packages.
###############################################################################

# shellcheck source=/dev/null
source /ctx/build/local-packages-helpers.sh

local_packages_install /var/pluto-packages rsync

echo "Bootstrap phase complete!"
