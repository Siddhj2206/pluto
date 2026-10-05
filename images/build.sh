#!/usr/bin/env bash
# Build the M0 base image artifact: vmlinuz + rootfs.img + manifest.json.
# Unprivileged: rootless podman for the rootfs, `podman unshare` for the
# ext4 assembly. No sudo, no system-wide changes.
set -euo pipefail

IMAGES=$(cd "$(dirname "$0")" && pwd)
ROOT=$(dirname "$IMAGES")
OUT=${PLUTO_IMAGE_OUT:-$IMAGES/out}

FC_VERSION=${FC_VERSION:-1.17.0}
KERNEL_URL=${KERNEL_URL:-https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/20260930-a738f18a8db0-0/x86_64/vmlinux-6.1.186}
KERNEL_SHA256=${KERNEL_SHA256:-ea0e55d03dbaebc79a58644308e0517b7a33f1530a84848d9edf47ffa61f69c8}
FC_URL=https://github.com/firecracker-microvm/firecracker/releases/download/v${FC_VERSION}/firecracker-v${FC_VERSION}-x86_64.tgz
DISK_MB=${DISK_MB:-2048}

need() { command -v "$1" >/dev/null || { echo "missing required tool: $1" >&2; exit 1; }; }
for t in podman curl tar sha256sum mkfs.ext4; do need "$t"; done

mkdir -p "$OUT/cache" "$OUT/bin" "$OUT/context"

echo "==> kernel"
if [ ! -f "$OUT/cache/vmlinuz" ]; then
  curl -fL --retry 3 -o "$OUT/cache/vmlinuz" "$KERNEL_URL"
fi
# A truncated or substituted kernel must fail here, before anything is copied
# into the artifact or recorded in the manifest: the manifest hashes whatever
# `vmlinuz` it finds, so a bad download would otherwise import cleanly and only
# surface as a Firecracker load failure on the first boot.
got_kernel=$(sha256sum "$OUT/cache/vmlinuz" | awk '{print $1}')
if [ "$got_kernel" != "$KERNEL_SHA256" ]; then
  echo "kernel checksum mismatch: $OUT/cache/vmlinuz" >&2
  echo "  want: $KERNEL_SHA256" >&2
  echo "  got:  $got_kernel" >&2
  echo "  delete the file and rebuild; update KERNEL_SHA256 only if the kernel moved intentionally" >&2
  exit 1
fi
cp -f "$OUT/cache/vmlinuz" "$OUT/vmlinuz"

echo "==> firecracker v$FC_VERSION"
if [ ! -x "$OUT/cache/firecracker" ]; then
  curl -fL --retry 3 -o "$OUT/cache/firecracker.tgz" "$FC_URL"
  tar -xzf "$OUT/cache/firecracker.tgz" -C "$OUT/cache"
  found=$(find "$OUT/cache" -maxdepth 3 -type f -name 'firecracker-v*' ! -name '*.debug' -print -quit)
  if [ -z "$found" ]; then
    echo "firecracker binary not found in tarball" >&2
    exit 1
  fi
  cp "$found" "$OUT/cache/firecracker"
  chmod +x "$OUT/cache/firecracker"
fi

echo "==> go helpers"
(cd "$ROOT" && CGO_ENABLED=0 go build -o "$OUT/bin/pluto-agent" ./cmd/pluto-agent)
(cd "$ROOT" && CGO_ENABLED=0 go build -o "$OUT/bin/pluto-vsock" ./cmd/pluto-vsock)
cp "$OUT/bin/pluto-agent" "$OUT/context/pluto-agent"
cp "$IMAGES/files/pluto-agent.service" "$OUT/context/pluto-agent.service"

echo "==> rootfs (podman build)"
podman build -q -t pluto-m0-base -f "$IMAGES/Containerfile" "$OUT/context" >/dev/null

echo "==> assemble rootfs.img (podman unshare)"
cid=$(podman create pluto-m0-base)
trap 'podman rm -f "$cid" >/dev/null 2>&1 || true' EXIT
podman export "$cid" > "$OUT/rootfs.tar"
podman rm -f "$cid" >/dev/null
trap - EXIT

cat > "$OUT/assemble.sh" <<'EOS'
set -euo pipefail
out="$1"
rm -rf "$out/rootfs"
mkdir -p "$out/rootfs"
tar -x --numeric-owner -f "$out/rootfs.tar" -C "$out/rootfs"
install -m 0755 -o root -g root "$out/bin/pluto-agent" "$out/rootfs/usr/local/bin/pluto-agent"
rm -f "$out/rootfs.img"
truncate -s "${DISK_MB}M" "$out/rootfs.img"
mkfs.ext4 -q -F -L pluto-root -U 0f15a7e1-8b1c-4a53-9c2e-706c75746f00 -d "$out/rootfs" "$out/rootfs.img"
rm -rf "$out/rootfs"
EOS
podman unshare env DISK_MB="$DISK_MB" bash "$OUT/assemble.sh" "$OUT"
rm -f "$OUT/rootfs.tar" "$OUT/assemble.sh"

echo "==> manifest"
base=$(awk '/^FROM /{print $2; exit}' "$IMAGES/Containerfile")
sha_kernel=$(sha256sum "$OUT/vmlinuz" | cut -d' ' -f1)
sha_fc=$(sha256sum "$OUT/cache/firecracker" | cut -d' ' -f1)
sha_root=$(sha256sum "$OUT/rootfs.img" | cut -d' ' -f1)
sha_agent=$(sha256sum "$OUT/bin/pluto-agent" | cut -d' ' -f1)
size=$(stat -c%s "$OUT/rootfs.img")
cat > "$OUT/manifest.json" <<EOF
{
  "schema": 1,
  "built_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "kernel": { "name": "vmlinux-6.1.186", "url": "$KERNEL_URL", "sha256": "$sha_kernel" },
  "firecracker": { "version": "$FC_VERSION", "url": "$FC_URL", "sha256": "$sha_fc" },
  "rootfs": {
    "file": "rootfs.img",
    "base": "$base",
    "sha256": "$sha_root",
    "size_bytes": $size,
    "disk_mb": $DISK_MB
  },
  "agent": { "sha256": "$sha_agent" }
}
EOF

echo "==> done: $OUT/{vmlinuz,rootfs.img,manifest.json}"
