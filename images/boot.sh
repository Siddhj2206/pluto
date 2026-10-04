#!/usr/bin/env bash
# Boot the M0 image rootless and verify it end to end: vsock SSH, egress,
# boot timing. Everything runs as the invoking user.
#
#   images/boot.sh            verify and exit
#   images/boot.sh --shell    drop into an interactive SSH session
#   images/boot.sh --keep     leave the run dir and VM up on failure
set -euo pipefail

IMAGES=$(cd "$(dirname "$0")" && pwd)
OUT=${PLUTO_IMAGE_OUT:-$IMAGES/out}
BOOT_ARGS=${BOOT_ARGS:-"console=ttyS0 root=/dev/vda rw reboot=k panic=1"}
KEEP=0
SHELL_MODE=0
for a in "$@"; do
  case "$a" in
    --keep) KEEP=1 ;;
    --shell) SHELL_MODE=1 ;;
    *) echo "unknown flag: $a" >&2; exit 2 ;;
  esac
done

need() { command -v "$1" >/dev/null || { echo "missing required tool: $1" >&2; exit 1; }; }
for t in slirp4netns ssh-keygen ssh debugfs sha256sum; do need "$t"; done
[ -x /usr/bin/unshare ] || { echo "missing /usr/bin/unshare" >&2; exit 1; }
[ -x "$OUT/cache/firecracker" ] || { echo "run images/build.sh first" >&2; exit 1; }
[ -f "$OUT/rootfs.img" ] || { echo "run images/build.sh first" >&2; exit 1; }

RUN=$(mktemp -d "${TMPDIR:-/tmp}/pluto-boot.XXXXXX")
FC_PID=""
SLIRP_PID=""
cleanup() {
  if [ "$KEEP" = 1 ]; then
    echo "kept: $RUN (VM left running; kill with: pkill -x firecracker; pkill -x slirp4netns)"
    return
  fi
  [ -n "$FC_PID" ] && kill "$FC_PID" 2>/dev/null || true
  [ -n "$SLIRP_PID" ] && kill "$SLIRP_PID" 2>/dev/null || true
  rm -rf "$RUN"
}
trap cleanup EXIT

echo "==> preparing $RUN"
cp "$OUT/rootfs.img" "$RUN/rootfs.img"
ssh-keygen -q -t ed25519 -N '' -f "$RUN/id"
debugfs -w -R "write $RUN/id.pub /home/dev/.ssh/authorized_keys" "$RUN/rootfs.img" >/dev/null 2>&1
debugfs -w -R "sif /home/dev/.ssh/authorized_keys mode 0100600" "$RUN/rootfs.img" >/dev/null 2>&1
debugfs -w -R "sif /home/dev/.ssh/authorized_keys uid 1000" "$RUN/rootfs.img" >/dev/null 2>&1
debugfs -w -R "sif /home/dev/.ssh/authorized_keys gid 1000" "$RUN/rootfs.img" >/dev/null 2>&1

cat > "$RUN/fc.json" <<EOF
{
  "boot-source": {
    "kernel_image_path": "$OUT/vmlinuz",
    "boot_args": "$BOOT_ARGS"
  },
  "drives": [
    {
      "drive_id": "rootfs",
      "path_on_host": "$RUN/rootfs.img",
      "is_root_device": true,
      "is_read_only": false,
      "cache_type": "Writeback"
    }
  ],
  "machine-config": { "vcpu_count": 2, "mem_size_mib": 1024 },
  "vsock": { "guest_cid": 3, "uds_path": "$RUN/v.sock" },
  "network-interfaces": [
    { "iface_id": "eth0", "host_dev_name": "tap-fc", "guest_mac": "06:00:AC:10:00:0F" }
  ],
  "logger": { "log_path": "$RUN/fc.log", "level": "Warning" }
}
EOF

# Inside the namespace: Firecracker owns tap-fc, slirp4netns owns tap-slirp,
# and a bridge joins them (slirp cannot share a tap with the VMM).
cat > "$RUN/holder.sh" <<'EOS'
set -euo pipefail
firecracker="$1"
config="$2"
"$firecracker" --no-api --config-file "$config" &
FC=$!
trap 'kill $FC 2>/dev/null || true' TERM INT
until ip link show tap-fc >/dev/null 2>&1; do sleep 0.05; done
ip link add br0 type bridge
ip link set tap-fc master br0
ip link set tap-fc up
ip link set br0 up
until ip link show tap-slirp >/dev/null 2>&1; do sleep 0.05; done
ip link set tap-slirp master br0
ip link set tap-slirp up
wait $FC
EOS

echo "==> booting (rootless firecracker + slirp4netns)"
START=$(date +%s.%N)
/usr/bin/unshare -Urn -- bash "$RUN/holder.sh" "$OUT/cache/firecracker" "$RUN/fc.json" \
  > "$RUN/serial.log" 2>&1 &
FC_PID=$!
slirp4netns --configure --mtu=1500 "$FC_PID" tap-slirp > "$RUN/slirp.log" 2>&1 &
SLIRP_PID=$!

BANNER=$("$OUT/bin/pluto-vsock" wait "$RUN/v.sock" 22 60)
END=$(date +%s.%N)
BOOT=$(awk -v a="$START" -v b="$END" 'BEGIN { printf "%.2f", b - a }')
echo "==> guest sshd up in ${BOOT}s: $BANNER"

SSH_CMD=(ssh -i "$RUN/id" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null
         -o LogLevel=ERROR -o "ProxyCommand=$OUT/bin/pluto-vsock connect $RUN/v.sock 22"
         dev@box)

CHECK=$("${SSH_CMD[@]}" 'uname -sr; id -un; loginctl show-user dev -p Linger; systemctl is-active pluto-agent')
echo "==> ssh: $(echo "$CHECK" | tr '\n' ' ')"
EGRESS=$("${SSH_CMD[@]}" 'timeout 30 git ls-remote https://github.com/octocat/Hello-World HEAD | cut -f1')
echo "==> egress: $EGRESS"
if [ -z "$EGRESS" ]; then
  echo "egress check failed" >&2
  exit 1
fi

if [ "$SHELL_MODE" = 1 ]; then
  echo "==> interactive shell (exit to shut the box down)"
  "${SSH_CMD[@]}" || true
fi

echo "==> ok: boot ${BOOT}s, ssh ok, egress ok"
