#!/usr/bin/env bash
# Boot-time measurement matrix: FC stock, FC selftest-init, CH stock (serial
# socket), CH selftest-init. Fresh ext4 image per run.
set -u
cd /tmp/opencode/pluto-research-boot

FC="$PWD/tools/release-v1.17.0-x86_64/firecracker-v1.17.0-x86_64"
CH="$PWD/tools/cloud-hypervisor-static"
KERNEL="$PWD/assets/vmlinux-6.1.186"
RES="vm/logs/results.jsonl"
: > "$RES"

mkimg() { # $1=stock|selftest  $2=outfile
  rm -f "$2"
  truncate -s 1536M "$2"
  mkfs.ext4 -q -d vm/rootfs -F -L pluto-root "$2"
  if [ "$1" = selftest ]; then
    debugfs -w -R "write vm/pluto-init /pluto-init" "$2" >/dev/null 2>&1
    debugfs -w -R "sif /pluto-init mode 0100755" "$2" >/dev/null 2>&1
  fi
}

run_fc_stock() {
  local i="$1"
  mkimg stock vm/run-stock.ext4
  python3 run_vm.py --name "fc-stock-$i" --timeout 30 --log-dir vm/logs \
    --done-on shell_echo_ok \
    --rule 'Kernel panic|record:panic' \
    --rule 'Linux version|record:kernel_entry' \
    --rule 'root@ubuntu-fc-uvm.*#|record:shell_prompt' \
    --rule 'root@ubuntu-fc-uvm.*#|send:sync; echo PLUTO_SHELL_OK' \
    --rule 'PLUTO_SHELL_OK|record:shell_echo_ok' \
    -- "$FC" --no-api --config-file vm/fc-stock.json | tee -a "$RES"
}

run_fc_selftest() {
  local i="$1"
  mkimg selftest vm/run-selftest.ext4
  python3 run_vm.py --name "fc-selftest-$i" --timeout 30 --log-dir vm/logs \
    --done-on stdin_ok \
    --rule 'Kernel panic|record:panic' \
    --rule 'Linux version|record:kernel_entry' \
    --rule 'PLUTO_INIT_MARKER|record:init_marker' \
    --rule 'PLUTO_SHELL_ALIVE|record:shell_self_test' \
    --rule 'PLUTO_INIT_MARKER|send:echo PLUTO_STDIN_OK' \
    --rule 'PLUTO_STDIN_OK|record:stdin_ok' \
    -- "$FC" --no-api --config-file vm/fc-selftest.json | tee -a "$RES"
}

run_ch_stock() {
  local i="$1"
  mkimg stock vm/run-stock.ext4
  rm -f vm/ch-serial.sock
  python3 run_vm.py --name "ch-stock-$i" --timeout 30 --log-dir vm/logs \
    --connect-unix "$PWD/vm/ch-serial.sock" \
    --done-on shell_echo_ok \
    --rule 'Kernel panic|record:panic' \
    --rule 'Linux version|record:kernel_entry' \
    --rule 'root@ubuntu-fc-uvm.*#|record:shell_prompt' \
    --rule 'root@ubuntu-fc-uvm.*#|send:sync; echo PLUTO_SHELL_OK' \
    --rule 'PLUTO_SHELL_OK|record:shell_echo_ok' \
    -- "$CH" --kernel "$KERNEL" --disk "path=vm/run-stock.ext4,image_type=raw" \
       --cpus boot=1 --memory size=256M \
       --cmdline "console=ttyS0 root=/dev/vda rw reboot=k panic=1" \
       --serial "socket=$PWD/vm/ch-serial.sock" --console off | tee -a "$RES"
}

run_ch_selftest() {
  local i="$1"
  mkimg selftest vm/run-selftest.ext4
  python3 run_vm.py --name "ch-selftest-$i" --timeout 30 --log-dir vm/logs \
    --done-on shell_self_test \
    --rule 'Kernel panic|record:panic' \
    --rule 'Linux version|record:kernel_entry' \
    --rule 'PLUTO_INIT_MARKER|record:init_marker' \
    --rule 'PLUTO_SHELL_ALIVE|record:shell_self_test' \
    -- "$CH" --kernel "$KERNEL" --disk "path=vm/run-selftest.ext4,image_type=raw" \
       --cpus boot=1 --memory size=256M \
       --cmdline "console=ttyS0 root=/dev/vda rw reboot=k panic=1 init=/pluto-init" \
       --serial tty --console off | tee -a "$RES"
}

for i in 1 2 3; do
  run_fc_stock "$i"
  run_fc_selftest "$i"
  run_ch_stock "$i"
  run_ch_selftest "$i"
done
echo "DONE"
