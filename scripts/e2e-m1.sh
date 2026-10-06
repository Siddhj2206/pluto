#!/usr/bin/env bash
# scripts/e2e-m1.sh — real-host end-to-end run of the M1 demo (ticket #45).
#
# Proves the M1 loop on one dev host: a schedule declared in a scratch repo
# fires at its computed minute, the job it names runs and lands in job
# history, auto-pause sleeps the box, attach enters the machine that did the
# work, and `pluto --device` drives the same daemon from a second machine
# over real ssh. Every assertion is explicit and names itself; the script
# exits non-zero on the first failure.
#
# Everything is scratch: its own pluto binary, state dir, socket, D-Bus
# session bus, systemd user manager, git repo, ssh server, and client ssh
# config. The developer's daemon, boxes, and systemd units are never touched.
#
# Usage:
#   scripts/e2e-m1.sh                       # run
#   PLUTO_E2E_KEEP=1 scripts/e2e-m1.sh      # keep the scratch dir and processes
#
# Environment:
#   PLUTO_E2E_IMAGE_DIR   image artifact dir (default: <repo>/images/out)
#   PLUTO_E2E_DIR         parent of the scratch run dir (default: ${TMPDIR:-/tmp})
#   PLUTO_E2E_KEEP=1      on exit, leave everything up and print how to inspect
#
# Host requirements (checked up front): a writable /dev/kvm, /dev/net/tun,
# unprivileged user namespaces, slirp4netns, debugfs, rootless git, ssh and
# an sshd binary (a running system sshd is not needed), systemd's user
# manager, and a D-Bus session bus binary.
set -euo pipefail

REPO=$(cd "$(dirname "$0")/.." && pwd)
IMAGE_DIR=${PLUTO_E2E_IMAGE_DIR:-$REPO/images/out}
KEEP=${PLUTO_E2E_KEEP:-0}
STARTED_AT=$SECONDS

# A private D-Bus session is needed to host a throwaway systemd user manager
# (the real one belongs to the developer's session; its box unit template
# points at the developer's state dir). Prefer starting the bus in-process;
# fall back to re-exec under dbus-run-session, which sets the bus up itself.
if [ -z "${PLUTO_E2E_BUS_READY:-}" ] && ! command -v dbus-daemon >/dev/null 2>&1; then
  if command -v dbus-run-session >/dev/null 2>&1; then
    export PLUTO_E2E_BUS_READY=1
    exec dbus-run-session -- "$0" "$@"
  fi
  echo "e2e: need dbus-daemon or dbus-run-session to host a private systemd user manager" >&2
  exit 1
fi

E2E_BASE=${PLUTO_E2E_DIR:-${TMPDIR:-/tmp}}
mkdir -p "$E2E_BASE"
SCRATCH=$(mktemp -d "$E2E_BASE/pluto-e2e-m1.XXXXXX")
PLUTO="$SCRATCH/bin/pluto"
SSH_CLIENT_CONFIG="$SCRATCH/ssh/client_config"
BUS_PID=""
SYSTEMD_PID=""
DAEMON_PID=""
SSHD_PID=""
BOX_ID=""
CURRENT="startup"

step() { CURRENT="$1"; printf '\n==> %s\n' "$1"; }
pass() { printf 'ok: %s\n' "$1"; }
fail() {
  printf '\ne2e FAILED during "%s": %s\n' "$CURRENT" "$1" >&2
  dump_diagnostics
  exit 1
}

dump_diagnostics() {
  [ -n "${SCRATCH:-}" ] || return 0
  if [ -f "$SCRATCH/daemon.log" ]; then
    printf '\n--- daemon log (last 30 lines) ---\n' >&2
    tail -n 30 "$SCRATCH/daemon.log" >&2 || true
  fi
  local serial
  for serial in "$SCRATCH"/state/boxes/*/serial.log; do
    [ -f "$serial" ] || continue
    printf '\n--- %s (last 20 lines) ---\n' "$serial" >&2
    tail -n 20 "$serial" >&2 || true
  done
  if [ -x "$PLUTO" ] && [ -S "$PLUTO_SOCKET" ]; then
    printf '\n--- pluto ls ---\n' >&2
    "$PLUTO" ls >&2 || true
    local id
    for id in $("$PLUTO" ls 2>/dev/null | awk 'NR>1 {print $1}'); do
      printf '\n--- pluto jobs %s ---\n' "$id" >&2
      "$PLUTO" jobs "$id" >&2 || true
      printf '\n--- pluto status %s ---\n' "$id" >&2
      "$PLUTO" status "$id" >&2 || true
    done
    local rec
    for rec in "$SCRATCH"/state/boxes/*/box.json; do
      [ -f "$rec" ] || continue
      printf '\n--- %s ---\n' "$rec" >&2
      cat "$rec" >&2 || true
    done
  fi
}

# poll_until NAME TIMEOUT CHECK... runs CHECK every 5s until it succeeds or
# TIMEOUT seconds pass; a timeout names the check.
poll_until() {
  local name=$1 timeout=$2
  shift 2
  local deadline=$((SECONDS + timeout))
  while :; do
    if "$@"; then pass "$name"; return 0; fi
    if [ "$SECONDS" -ge "$deadline" ]; then
      fail "$name (still failing after ${timeout}s)"
    fi
    sleep 5
  done
}

assert_contains() { # NAME HAYSTACK NEEDLE
  if [[ $2 == *"$3"* ]]; then
    pass "$1"
  else
    printf '\ne2e FAILED during "%s": %s\n  expected to find: %s\n  in output:\n%s\n' "$CURRENT" "$1" "$3" "$2" >&2
    dump_diagnostics
    exit 1
  fi
}

assert_not_contains() { # NAME HAYSTACK NEEDLE
  if [[ $2 != *"$3"* ]]; then
    pass "$1"
  else
    printf '\ne2e FAILED during "%s": %s\n  did not expect: %s\n  in output:\n%s\n' "$CURRENT" "$1" "$3" "$2" >&2
    dump_diagnostics
    exit 1
  fi
}

need() {
  command -v "$1" >/dev/null 2>&1 || fail "required tool missing: $1"
}

stop_pid() { # PID NAME
  local pid=$1 name=$2
  [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null || return 0
  kill "$pid" 2>/dev/null || true
  for _ in $(seq 1 50); do
    kill -0 "$pid" 2>/dev/null || return 0
    sleep 0.1
  done
  echo "e2e: $name did not stop; killing" >&2
  kill -9 "$pid" 2>/dev/null || true
}

cleanup() {
  trap - EXIT
  set +e
  if [ "$KEEP" = 1 ]; then
    printf '\ne2e: kept scratch dir: %s\n' "$SCRATCH"
    printf 'e2e: daemon log: %s\n' "$SCRATCH/daemon.log"
    printf 'e2e: inspect with: HOME=%s XDG_CONFIG_HOME=%s XDG_RUNTIME_DIR=%s \\\n' \
      "$SCRATCH/home" "$SCRATCH/config" "$SCRATCH/run"
    # shellcheck disable=SC2016  # $PATH is meant literally in the instructions
    printf '      PLUTO_STATE_DIR=%s PLUTO_SOCKET=%s PATH=%s/bin:\$PATH %s status\n' \
      "$SCRATCH/state" "$SCRATCH/pluto.sock" "$SCRATCH" "$PLUTO"
    return
  fi
  # Destroy the box first: it stops the unit and removes the disk while the
  # daemon and its private systemd manager are still alive.
  if [ -n "$BOX_ID" ] && [ -S "$SCRATCH/pluto.sock" ]; then
    "$PLUTO" destroy "$BOX_ID" --yes >/dev/null 2>&1 || true
  fi
  stop_pid "$SSHD_PID" "sshd"
  stop_pid "$DAEMON_PID" "daemon"
  stop_pid "$SYSTEMD_PID" "systemd user manager"
  stop_pid "$BUS_PID" "dbus-daemon"
  if pgrep -f "$SCRATCH" >/dev/null 2>&1; then
    echo "e2e: warning: processes from this run may still be alive:" >&2
    pgrep -af "$SCRATCH" >&2 || true
  fi
  # systemd creates 000-mode placeholder nodes in XDG_RUNTIME_DIR; make them
  # writable so the scratch dir can actually go away.
  chmod -R u+rwx "$SCRATCH" 2>/dev/null || true
  rm -rf "$SCRATCH"
}
trap cleanup EXIT

step "preflight"
for tool in go git ssh ssh-keygen unshare slirp4netns debugfs systemctl; do need "$tool"; done
SSHD_BIN=$(command -v sshd || true)
[ -n "$SSHD_BIN" ] || [ -x /usr/sbin/sshd ] || fail "required tool missing: sshd (install openssh-server)"
[ -n "$SSHD_BIN" ] || SSHD_BIN=/usr/sbin/sshd
SYSTEMD_BIN=""
for candidate in /usr/lib/systemd/systemd /lib/systemd/systemd; do
  if [ -x "$candidate" ]; then SYSTEMD_BIN="$candidate"; break; fi
done
[ -n "$SYSTEMD_BIN" ] || fail "systemd user manager binary not found (/usr/lib/systemd/systemd)"
[ -w /dev/kvm ] || fail "writable /dev/kvm is required to boot boxes"
[ -e /dev/net/tun ] || fail "/dev/net/tun is required for rootless box networking"
[ -f "$IMAGE_DIR/manifest.json" ] || fail "image artifact missing at $IMAGE_DIR (run go run ./cmd/pluto-image-builder or set PLUTO_E2E_IMAGE_DIR)"
REAL_SSH=$(command -v ssh)
mkdir -p "$SCRATCH/bin" "$SCRATCH/home" "$SCRATCH/config" "$SCRATCH/run" "$SCRATCH/ssh"

step "building pluto from $REPO"
(cd "$REPO" && go build -o "$PLUTO" ./cmd/pluto)

# From here on the run is hermetic: scratch HOME/XDG dirs, state, socket,
# and PATH (whose bin dir holds the pluto build and an ssh shim).
export HOME="$SCRATCH/home"
export XDG_CONFIG_HOME="$SCRATCH/config"
export XDG_RUNTIME_DIR="$SCRATCH/run"
export PLUTO_STATE_DIR="$SCRATCH/state"
export PLUTO_SOCKET="$SCRATCH/pluto.sock"
export PATH="$SCRATCH/bin:$PATH"
chmod 700 "$XDG_RUNTIME_DIR"

step "starting a private D-Bus session bus"
if [ -z "${PLUTO_E2E_BUS_READY:-}" ]; then
  bus_info=$(dbus-daemon --session --fork --print-address=1 --print-pid=1) ||
    fail "start private dbus-daemon"
  DBUS_SESSION_BUS_ADDRESS=$(printf '%s\n' "$bus_info" | sed -n 1p)
  BUS_PID=$(printf '%s\n' "$bus_info" | sed -n 2p)
  [ -n "$DBUS_SESSION_BUS_ADDRESS" ] && [ -n "$BUS_PID" ] || fail "start private dbus-daemon"
  export DBUS_SESSION_BUS_ADDRESS
fi

step "starting a private systemd user manager"
"$SYSTEMD_BIN" --user >>"$SCRATCH/systemd.log" 2>&1 &
SYSTEMD_PID=$!
manager_ready=0
for _ in $(seq 1 50); do
  if systemctl --user daemon-reload >/dev/null 2>&1; then manager_ready=1; break; fi
  sleep 0.2
done
[ "$manager_ready" = 1 ] || fail "private systemd user manager did not start (see $SCRATCH/systemd.log)"
# systemd resets PATH for spawned units to the compile-time default, which
# hides tools the runner needs (slirp4netns lives outside /usr/bin here).
# Hand the manager this run's PATH so box units inherit it.
systemctl --user set-environment "PATH=$PATH" || fail "set the private manager's PATH"

step "starting the scratch daemon"
"$PLUTO" --socket "$PLUTO_SOCKET" --state-dir "$PLUTO_STATE_DIR" daemon >>"$SCRATCH/daemon.log" 2>&1 &
DAEMON_PID=$!
daemon_ready=0
for _ in $(seq 1 100); do
  if [ -S "$PLUTO_SOCKET" ] && kill -0 "$DAEMON_PID" 2>/dev/null; then daemon_ready=1; break; fi
  sleep 0.1
done
[ "$daemon_ready" = 1 ] || fail "scratch daemon did not listen on $PLUTO_SOCKET"
"$PLUTO" version | grep -q '^pluto ' || fail "scratch pluto binary does not answer version"

step "importing the base image from $IMAGE_DIR"
IMPORT_OUT=$("$PLUTO" image import "$IMAGE_DIR") || fail "pluto image import $IMAGE_DIR"
assert_contains "image imported" "$IMPORT_OUT" "imported image"

step "provisioning the scratch repo"
REPO_DIR="$SCRATCH/repo/e2e-repo"
mkdir -p "$REPO_DIR"
git init -q -b main "$REPO_DIR"
git -C "$REPO_DIR" config user.name "pluto e2e"
git -C "$REPO_DIR" config user.email "e2e@pluto.invalid"
# The schedule targets a full minute at least two minutes out (UTC, minute
# resolution), so handoff has landed well before the trigger is due.
TARGET_DATE=$(date -u -d '+2 minutes' +'%Y-%m-%d %H:%M')
TARGET_EPOCH=$(date -u -d "$TARGET_DATE" +%s)
TARGET_MINUTE=${TARGET_DATE##*:}
TARGET_HOUR=${TARGET_DATE#* }
TARGET_HOUR=${TARGET_HOUR%%:*}
CRON="$TARGET_MINUTE $TARGET_HOUR * * *"
cat > "$REPO_DIR/.pluto.toml" <<EOF
[box]
auto_pause = "10s"

[jobs.work]
description = "the scheduled demo job"
command = "echo scheduled-marker-ok > \$PLUTO_WORKTREE/scheduled.marker && echo job-output-ok"

[[schedule]]
name = "demo"
cron = "$CRON"
job = "work"
EOF
git -C "$REPO_DIR" add .pluto.toml
git -C "$REPO_DIR" commit -qm "declare the M1 demo schedule"
printf 'schedule %s fires at %s:%s UTC\n' "$CRON" "$TARGET_HOUR" "$TARGET_MINUTE"

step "pluto up"
UP_OUT=$(cd "$REPO_DIR" && "$PLUTO" up) || fail "pluto up"
assert_contains "box created and running" "$UP_OUT" "running"

LS=$("$PLUTO" ls)
BOX_ID=$(awk 'NR>1 && $2=="e2e-repo/main" {print $1; exit}' <<<"$LS")
[ -n "$BOX_ID" ] || fail "box registered as e2e-repo/main in pluto ls"
pass "box for e2e-repo/main is $BOX_ID"

step "waiting for the schedule to fire ($CRON UTC)"
JOBS=""
check_job_done() {
  JOBS=$("$PLUTO" jobs "$BOX_ID" 2>&1) || return 1
  [ "$(awk 'NR>1 && /scheduled\.marker/ {print $2" "$3; exit}' <<<"$JOBS")" = "done 0" ]
}
poll_until "schedule fired: pluto jobs records the declared job done, exit 0" 300 check_job_done
JOB_ROW=$(awk 'NR>1 && /scheduled\.marker/ {print; exit}' <<<"$JOBS")
printf 'job record: %s\n' "$JOB_ROW"

# The job's start time must fall just after the cron minute the contract
# declared: proof the trigger fired the job, not a manual run.
STARTED_LOCAL=$(awk 'NR>1 && /scheduled\.marker/ {print $5" "$6; exit}' <<<"$JOBS")
STARTED_EPOCH=$(date -d "$STARTED_LOCAL" +%s) || fail "parse job start time: $STARTED_LOCAL"
DELTA=$((STARTED_EPOCH - TARGET_EPOCH))
if [ "$DELTA" -lt 0 ] || [ "$DELTA" -gt 120 ]; then
  fail "scheduled job started in the target minute (cron $CRON UTC at $TARGET_EPOCH, started $STARTED_LOCAL local = ${DELTA}s after)"
fi
pass "job started ${DELTA}s after the scheduled minute $TARGET_HOUR:$TARGET_MINUTE UTC"

assert_contains "box still running after the job (auto-pause has not fired yet)" \
  "$("$PLUTO" status "$BOX_ID")" "state:    running"

step "reading the recorded job output"
JOB_LOG=$("$PLUTO" logs "$BOX_ID" --job last) || fail "pluto logs --job last"
assert_contains "job output is retained" "$JOB_LOG" "job-output-ok"

step "waiting for auto-pause to sleep the box"
STATUS=""
check_paused() {
  STATUS=$("$PLUTO" status "$BOX_ID" 2>&1) || return 1
  [ "$(awk -F': *' '$1=="state" {print $2; exit}' <<<"$STATUS")" = "paused" ]
}
poll_until "auto-pause slept the box (pluto status: paused)" 300 check_paused

step "attaching to the machine that did the work"
ATTACH_OUT=$("$PLUTO" attach "$BOX_ID" -- cat /home/dev/work/e2e-repo/scheduled.marker) || fail "pluto attach"
assert_contains "attach sees the scheduled job's marker" "$ATTACH_OUT" "scheduled-marker-ok"
assert_contains "attach woke the box" "$("$PLUTO" status "$BOX_ID")" "state:    running"

step "starting a throwaway ssh server for the --device check"
# The remote leg needs an ssh server. A system sshd needs root and the
# developer's ssh config decides ports and keys, so this runs an unprivileged
# sshd on a random high port with scratch keys. The CLI's `ssh` calls are
# pointed at the scratch client config by a PATH shim (bin/ssh); the
# transport is still the real OpenSSH client. No system sshd is required.
ssh-keygen -q -t ed25519 -N '' -f "$SCRATCH/ssh/hostkey"
ssh-keygen -q -t ed25519 -N '' -f "$SCRATCH/ssh/id"
cp "$SCRATCH/ssh/id.pub" "$SCRATCH/ssh/authorized_keys"
cat > "$SCRATCH/ssh/remote-run.sh" <<EOF
#!/bin/sh
# The simulated remote machine's profile: put this run's pluto on PATH and
# run whatever command the client sent.
PATH="$SCRATCH/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
export PATH
exec /bin/sh -c "\$SSH_ORIGINAL_COMMAND"
EOF
chmod +x "$SCRATCH/ssh/remote-run.sh"
SSHD_OK=0
for _ in $(seq 1 10); do
  PORT=$((20000 + RANDOM % 40000))
  cat > "$SCRATCH/ssh/sshd_config" <<EOF
Port $PORT
ListenAddress 127.0.0.1
HostKey $SCRATCH/ssh/hostkey
PidFile $SCRATCH/ssh/sshd.pid
AuthorizedKeysFile $SCRATCH/ssh/authorized_keys
StrictModes no
UsePAM no
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
PrintMotd no
AllowUsers $(id -un)
ForceCommand $SCRATCH/ssh/remote-run.sh
LogLevel ERROR
EOF
  "$SSHD_BIN" -D -f "$SCRATCH/ssh/sshd_config" >>"$SCRATCH/sshd.log" 2>&1 &
  SSHD_PID=$!
  sleep 0.3
  if kill -0 "$SSHD_PID" 2>/dev/null && \
     "$REAL_SSH" -p "$PORT" -i "$SCRATCH/ssh/id" -o IdentitiesOnly=yes \
       -o StrictHostKeyChecking=no -o UserKnownHostsFile="$SCRATCH/ssh/known_hosts" \
       -o BatchMode=yes -o ConnectTimeout=3 "$(id -un)@127.0.0.1" true >/dev/null 2>&1; then
    SSHD_OK=1
    break
  fi
  kill "$SSHD_PID" 2>/dev/null || true
  wait "$SSHD_PID" 2>/dev/null || true
  SSHD_PID=""
done
[ "$SSHD_OK" = 1 ] || fail "the --device check needs an ssh server: install openssh-server (a running system sshd is not required; see $SCRATCH/sshd.log)"
pass "throwaway sshd listening on 127.0.0.1:$PORT"

cat > "$SSH_CLIENT_CONFIG" <<EOF
Host pluto-e2e-local
  HostName 127.0.0.1
  Port $PORT
  User $(id -un)
  IdentityFile $SCRATCH/ssh/id
  IdentitiesOnly yes
  StrictHostKeyChecking no
  UserKnownHostsFile $SCRATCH/ssh/known_hosts
  BatchMode yes
  ConnectTimeout 5
  LogLevel ERROR
EOF
cat > "$SCRATCH/bin/ssh" <<EOF
#!/bin/sh
# e2e shim: the CLI shells out to plain ssh, which always reads the real
# user's ~/.ssh/config. Prepend -F so this run's connections use the scratch
# config instead. The transport is the real OpenSSH client.
exec "$REAL_SSH" -F "$SSH_CLIENT_CONFIG" "\$@"
EOF
chmod +x "$SCRATCH/bin/ssh"
"$REAL_SSH" -F "$SSH_CLIENT_CONFIG" "pluto-e2e-local" true >/dev/null 2>&1 ||
  fail "scratch ssh config did not reach the throwaway sshd"

step "pluto --device runs the CLI on a remote machine"
DEVICE_TARGET="$(id -un)@pluto-e2e-local"
ADD_OUT=$("$PLUTO" device add local "$DEVICE_TARGET" 2>&1) || fail "pluto device add"
assert_contains "device saved and verified over ssh" "$ADD_OUT" "saved device local"
assert_not_contains "device verification answered over ssh" "$ADD_OUT" "did not answer"

REMOTE_OUT=$("$PLUTO" --socket "$PLUTO_SOCKET" --state-dir "$PLUTO_STATE_DIR" \
  --device local status "$BOX_ID" 2>&1) || fail "pluto --device local status"
assert_contains "remote CLI sees the box's project" "$REMOTE_OUT" "project:  e2e-repo"
assert_contains "remote CLI sees the same box id" "$REMOTE_OUT" "$BOX_ID"

step "destroying the box"
DESTROY_OUT=$("$PLUTO" destroy "$BOX_ID" --yes) || fail "pluto destroy"
assert_contains "box destroyed" "$DESTROY_OUT" "destroyed box"
BOX_ID=""
LS=$("$PLUTO" ls)
assert_not_contains "destroy removed the box from pluto ls" "$LS" "e2e-repo/main"
BOXES_LEFT=$(ls -A "$PLUTO_STATE_DIR/boxes" 2>/dev/null || true)
[ -z "$BOXES_LEFT" ] || fail "destroy removed the box disk (boxes dir still has: $BOXES_LEFT)"
pass "destroy removed the box disk"

printf '\ne2e: all checks passed in %ss\n' "$((SECONDS - STARTED_AT))"
