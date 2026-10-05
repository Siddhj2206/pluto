#!/usr/bin/env bash
# scripts/e2e-m2.sh — real-host end-to-end run of the M2 agent-session demo (#59).
#
# Proves the Orbs loop on one dev host: a hermetic stand-in agent declared
# with [sessions.agent] starts under tmux, survives a client disconnect,
# keeps the box awake while it burns CPU/IO, the box auto-pauses when the
# session goes idle, waking restarts the session from its own on-disk state,
# a branch is pushed from inside the box to a scratch bare origin, and
# `pluto --device` attaches into the session from a second machine. Every
# assertion is explicit and names itself; the script exits non-zero on the
# first failure.
#
# Everything is scratch: its own pluto binary, state dir, socket, D-Bus
# session bus, systemd user manager, git repo, bare origin served by a
# throwaway git daemon, ssh server, and client ssh config. The developer's
# daemon, boxes, and systemd units are never touched.
#
# Usage:
#   scripts/e2e-m2.sh                       # run
#   PLUTO_E2E_KEEP=1 scripts/e2e-m2.sh      # keep the scratch dir and processes
#
# Environment:
#   PLUTO_E2E_IMAGE_DIR   image artifact dir (default: <repo>/images/out)
#   PLUTO_E2E_DIR         parent of the scratch run dir. Default:
#                         ${XDG_CACHE_HOME:-$HOME/.cache}/pluto-e2e — on the
#                         user's disk, not a small tmpfs (/tmp is often a size-
#                         limited tmpfs that cannot hold the image and box disks,
#                         ~2GB each). Keep it short: Firecracker's API socket is
#                         a unix socket under the scratch dir and caps the path
#                         near 107 bytes; the script fails early with a clear
#                         message if the resolved path is too long.
#   PLUTO_E2E_KEEP=1      on exit, leave everything up and print how to inspect
#
# Host requirements (checked up front): a writable /dev/kvm, /dev/net/tun,
# unprivileged user namespaces, slirp4netns, debugfs, rootless git (with
# `git daemon`), util-linux `script`, ssh and an sshd binary (a running system
# sshd is not needed), systemd's user manager, and a D-Bus session bus binary.
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

E2E_BASE=${PLUTO_E2E_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/pluto-e2e}
mkdir -p "$E2E_BASE"
# A short random leaf keeps the deepest path (Firecracker's API socket) under
# the unix-socket path limit; the base is documented above.
SCRATCH=$(mktemp -d "$E2E_BASE/XXXXXX")
PLUTO="$SCRATCH/bin/pluto"
SSH_CLIENT_CONFIG="$SCRATCH/ssh/client_config"
ATTACH_FIFO="$SCRATCH/attach.stdin"
BUS_PID=""
SYSTEMD_PID=""
DAEMON_PID=""
SSHD_PID=""
GITD_PID=""
ATTACH_PID=""
BOX_ID=""
PROJECT="m2-agent"
GUEST_WORKTREE=""
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
    printf '\n--- daemon log (last 40 lines) ---\n' >&2
    tail -n 40 "$SCRATCH/daemon.log" >&2 || true
  fi
  if [ -f "$SCRATCH/git-daemon.log" ]; then
    printf '\n--- git daemon log (last 20 lines) ---\n' >&2
    tail -n 20 "$SCRATCH/git-daemon.log" >&2 || true
  fi
  local serial
  for serial in "$SCRATCH"/state/boxes/*/serial.log "$SCRATCH"/state/boxes/*/slirp.log; do
    [ -f "$serial" ] || continue
    printf '\n--- %s (last 30 lines) ---\n' "$serial" >&2
    tail -n 30 "$serial" >&2 || true
  done
  local typescript
  for typescript in "$SCRATCH"/*.typescript; do
    [ -f "$typescript" ] || continue
    printf '\n--- %s (last 40 lines) ---\n' "$typescript" >&2
    tail -n 40 "$typescript" >&2 || true
  done
  if [ -x "$PLUTO" ] && [ -n "${PLUTO_SOCKET:-}" ] && [ -S "$PLUTO_SOCKET" ]; then
    printf '\n--- pluto ls ---\n' >&2
    "$PLUTO" ls >&2 || true
    local id
    for id in $("$PLUTO" ls 2>/dev/null | awk 'NR>1 {print $1}'); do
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

# A pty is needed to run an interactive `tmux attach` (a detached client has
# no terminal and tmux refuses). `script` supplies one; its stdin is a FIFO
# held open so the client stays attached until we detach it explicitly.
start_pty() { # COMMAND TYPESCRIPT
  local cmd=$1 out=$2
  : > "$out"
  script -qef -c "$cmd" "$out" <"$ATTACH_FIFO" >/dev/null 2>&1 &
  ATTACH_PID=$!
}

stop_pty() {
  local pid=${ATTACH_PID:-}
  [ -n "$pid" ] || return 0
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  ATTACH_PID=""
}

pty_gone() { [ -z "${ATTACH_PID:-}" ] || ! kill -0 "$ATTACH_PID" 2>/dev/null; }

# detach_session detaches every client attached to the box's tmux session,
# leaving the session (and the box) running.
detach_session() { # SESSION
  "$PLUTO" attach "$BOX_ID" -- tmux detach-client -s "$1" >/dev/null 2>&1 || true
}

cleanup() {
  trap - EXIT
  set +e
  stop_pty
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
  stop_pid "$GITD_PID" "git daemon"
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
for tool in go git ssh ssh-keygen unshare slirp4netns debugfs systemctl script; do need "$tool"; done
SSHD_BIN=$(command -v sshd || true)
[ -n "$SSHD_BIN" ] || [ -x /usr/sbin/sshd ] || fail "required tool missing: sshd (install openssh-server)"
[ -n "$SSHD_BIN" ] || SSHD_BIN=/usr/sbin/sshd
SYSTEMD_BIN=""
for candidate in /usr/lib/systemd/systemd /lib/systemd/systemd; do
  if [ -x "$candidate" ]; then SYSTEMD_BIN="$candidate"; break; fi
done
[ -n "$SYSTEMD_BIN" ] || fail "systemd user manager binary not found (/usr/lib/systemd/systemd)"
GIT_DAEMON_BIN="$(git --exec-path)/git-daemon"
[ -x "$GIT_DAEMON_BIN" ] || GIT_DAEMON_BIN=$(command -v git-daemon || true)
[ -n "$GIT_DAEMON_BIN" ] && [ -x "$GIT_DAEMON_BIN" ] ||
  fail "git daemon is required to serve the scratch origin (part of git)"
[ -w /dev/kvm ] || fail "writable /dev/kvm is required to boot boxes"
[ -e /dev/net/tun ] || fail "/dev/net/tun is required for rootless box networking"
[ -f "$IMAGE_DIR/manifest.json" ] || fail "image artifact missing at $IMAGE_DIR (run images/build.sh or set PLUTO_E2E_IMAGE_DIR)"
# Firecracker's API socket is a unix socket at
# <scratch>/state/boxes/<36-char-id>/firecracker.sock. SUN_LEN caps that path
# near 107 bytes; a longer one fails deep in VMM boot with an opaque error, so
# check it up front and name the fix.
api_sock_sample="$SCRATCH/state/boxes/00000000-0000-0000-0000-000000000000/firecracker.sock"
if [ "${#api_sock_sample}" -gt 107 ]; then
  fail "PLUTO_E2E_DIR is too long for Firecracker's API socket (${#api_sock_sample} bytes > 107): set PLUTO_E2E_DIR to a shorter path"
fi
REAL_SSH=$(command -v ssh)
mkdir -p "$SCRATCH/bin" "$SCRATCH/home" "$SCRATCH/config" "$SCRATCH/run" "$SCRATCH/ssh" "$SCRATCH/git"
mkfifo "$ATTACH_FIFO"
exec 9<>"$ATTACH_FIFO"

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

step "provisioning the scratch repo and bare origin"
REPO_DIR="$SCRATCH/repo/$PROJECT"
mkdir -p "$REPO_DIR/bin"
git init -q -b main "$REPO_DIR"
git -C "$REPO_DIR" config user.name "pluto e2e"
git -C "$REPO_DIR" config user.email "e2e@pluto.invalid"

# The hermetic stand-in agent: no network, tmux-owned, and durable through a
# pause via its own on-disk turn counter. It prints a marker on every start so
# reattaching can prove which turn the conversation resumed on, burns CPU and
# IO for a bounded working spell (so auto-pause sees a busy cgroup), then idles
# forever so the pane — and the session — stays alive.
cat > "$REPO_DIR/bin/agent.sh" <<'AGENT'
#!/bin/sh
# Stand-in coding agent for scripts/e2e-m2.sh: agent-agnostic, no network.
set -eu
wt=${PLUTO_WORKTREE:-$(pwd)}
state="$wt/.agent-state"
mkdir -p "$state"
turn=0
if [ -f "$state/counter" ]; then turn=$(cat "$state/counter"); fi
turn=$((turn + 1))
printf '%s\n' "$turn" > "$state/counter"
printf 'pluto-stand-in-agent turn=%s\n' "$turn"
printf 'turn=%s at %s\n' "$turn" "$(date -u +%FT%TZ)" >> "$state/turns.log"

# A bounded working spell: burn CPU and IO so the session's cgroup counters
# move and auto-pause keeps the box awake.
work=${AGENT_WORK_SECONDS:-60}
end=$(( $(date +%s) + work ))
while [ "$(date +%s)" -lt "$end" ]; do
  i=0
  while [ "$i" -lt 120000 ]; do i=$((i + 1)); done
  dd if=/dev/zero of="$state/burn.bin" bs=64k count=8 conv=fsync 2>/dev/null || true
done

# Idle forever: the pane (and therefore the tmux session) outlives the burn.
while :; do sleep 3600; done
AGENT
chmod +x "$REPO_DIR/bin/agent.sh"

cat > "$REPO_DIR/.pluto.toml" <<EOF
[box]
# A short idle window so the e2e does not wait an hour for the pause; the
# session's bounded burn is what keeps the box awake while it works.
auto_pause = "20s"

[env]
AGENT_WORK_SECONDS = "150"

[sessions.agent]
description = "hermetic stand-in agent"
command = "bin/agent.sh"
EOF
git -C "$REPO_DIR" add bin/agent.sh .pluto.toml
git -C "$REPO_DIR" commit -qm "declare the M2 stand-in agent session"

# A scratch bare origin, served to the guest by a throwaway git daemon on a
# random high port. The guest reaches the host at slirp's 10.0.2.2, so the
# worktree's origin is a git:// URL the box can push to.
git init -q --bare "$SCRATCH/git/$PROJECT.git"
GIT_PORT=$((20000 + RANDOM % 40000))
"$GIT_DAEMON_BIN" --reuseaddr --export-all --enable=receive-pack \
  --base-path="$SCRATCH/git" --listen=0.0.0.0 --port="$GIT_PORT" "$SCRATCH/git" \
  >>"$SCRATCH/git-daemon.log" 2>&1 &
GITD_PID=$!
sleep 0.3
kill -0 "$GITD_PID" 2>/dev/null || fail "throwaway git daemon did not start"
git -C "$REPO_DIR" remote add origin "git://10.0.2.2:$GIT_PORT/$PROJECT.git"
pass "scratch origin git://10.0.2.2:$GIT_PORT/$PROJECT.git served on the host"

step "pluto up starts the declared session"
UP_OUT=$(cd "$REPO_DIR" && "$PLUTO" up) || fail "pluto up"
assert_contains "box created and running" "$UP_OUT" "running"

LS=$("$PLUTO" ls)
BOX_ID=$(awk -v p="$PROJECT/main" 'NR>1 && $2==p {print $1; exit}' <<<"$LS")
[ -n "$BOX_ID" ] || fail "box registered as $PROJECT/main in pluto ls"
pass "box for $PROJECT is $BOX_ID"

STATUS=$("$PLUTO" status "$BOX_ID")
PROJECT_NAME=$(awk -F': *' '$1=="project" {print $2; exit}' <<<"$STATUS")
[ -n "$PROJECT_NAME" ] || fail "pluto status reports the box's project"
GUEST_WORKTREE="/home/dev/work/$PROJECT_NAME"
pass "in-box worktree is $GUEST_WORKTREE"

session_running() {
  STATUS=$("$PLUTO" status "$BOX_ID" 2>&1) || return 1
  grep -q '^session: *agent running' <<<"$STATUS"
}
session_attached() {
  STATUS=$("$PLUTO" status "$BOX_ID" 2>&1) || return 1
  grep -q '^session: *agent running (attached)' <<<"$STATUS"
}
poll_until "the declared session is running under tmux" 180 session_running

step "entering the session, then detaching without ending it"
start_pty "$PLUTO attach $BOX_ID --session agent" "$SCRATCH/session-attach.typescript"
poll_until "a client is attached to the session" 120 session_attached
detach_session agent
poll_until "the detaching client exits" 90 pty_gone
stop_pty
assert_not_contains "the session reports no client attached after detach" "$("$PLUTO" status "$BOX_ID")" "(attached)"
poll_until "the session keeps running after the client detaches" 60 session_running

step "the box stays awake while the session works"
# No client is attached now; only the session's cgroup CPU/IO can keep it up.
# Wait well past the 20s idle window and assert it did not sleep mid-turn.
sleep 45
STATUS=$("$PLUTO" status "$BOX_ID")
assert_contains "the box is still running past the idle window" "$STATUS" "state:    running"
assert_contains "the working session is still running" "$STATUS" "session:  agent running"

step "waiting for auto-pause once the session goes idle"
paused() {
  STATUS=$("$PLUTO" status "$BOX_ID" 2>&1) || return 1
  grep -q '^state: *paused' <<<"$STATUS"
}
poll_until "auto-pause slept the box once the session went idle" 360 paused

step "waking the box and reattaching to the resumed session"
(cd "$REPO_DIR" && "$PLUTO" up) >/dev/null || fail "pluto up (wake)"
poll_until "wake restarted the declared session" 180 session_running

# The agent's counter is the program's own on-disk state: pause killed the
# process, wake restarted it, and it resumed from the file on the box disk.
counter_value() {
  "$PLUTO" attach "$BOX_ID" -- cat "$GUEST_WORKTREE/.agent-state/counter" 2>/dev/null | tr -d '[:space:]'
}
counter_advanced() {
  local n
  n=$(counter_value) || return 1
  [ -n "$n" ] && [ "$n" -ge 2 ]
}
poll_until "the stand-in agent's on-disk turn counter advanced past turn 1" 120 counter_advanced
pass "resumed on turn $(counter_value) ($(date -u +%FT%TZ))"

pane_shows_turn() { # TURN
  local out
  out=$("$PLUTO" attach "$BOX_ID" -- tmux capture-pane -p -t agent 2>/dev/null) || return 1
  grep -q "pluto-stand-in-agent turn=$1" <<<"$out"
}
poll_until "the restarted session's pane shows the resumed conversation (turn 2)" 120 pane_shows_turn 2

: > "$SCRATCH/resume-attach.typescript"
start_pty "$PLUTO attach $BOX_ID --session agent" "$SCRATCH/resume-attach.typescript"
poll_until "reattaching enters the resumed session" 120 session_attached
detach_session agent
poll_until "the reattaching client exits" 90 pty_gone
stop_pty

step "pushing a branch from inside the box to the scratch origin"
# One shell-string command so ssh passes it to the box's shell verbatim.
PUSH_CMD="cd '$GUEST_WORKTREE' && git checkout -q -b agent-work && echo agent > agent.txt && git add agent.txt && git commit -qm 'agent work' && git push origin agent-work"
PUSH_OUT=$("$PLUTO" attach "$BOX_ID" -- "$PUSH_CMD" 2>&1) || fail "push a branch from inside the box"
assert_contains "the box pushed the branch to its origin" "$PUSH_OUT" "agent-work"
git -C "$SCRATCH/git/$PROJECT.git" show-ref --verify --quiet refs/heads/agent-work ||
  fail "the scratch origin received agent-work"
pass "scratch origin has refs/heads/agent-work"
ORIGIN_SUBJECT=$(git -C "$SCRATCH/git/$PROJECT.git" log -1 --format=%s agent-work)
assert_contains "the pushed commit is the agent's work" "$ORIGIN_SUBJECT" "agent work"

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

step "pluto --device attaches into the session from a second machine"
DEVICE_TARGET="$(id -un)@pluto-e2e-local"
ADD_OUT=$("$PLUTO" device add local "$DEVICE_TARGET" 2>&1) || fail "pluto device add"
assert_contains "device saved and verified over ssh" "$ADD_OUT" "saved device local"
assert_not_contains "device verification answered over ssh" "$ADD_OUT" "did not answer"

DEVICE_CMD="$PLUTO --socket $PLUTO_SOCKET --state-dir $PLUTO_STATE_DIR --device local attach $BOX_ID --session agent"
: > "$SCRATCH/device-attach.typescript"
start_pty "$DEVICE_CMD" "$SCRATCH/device-attach.typescript"
poll_until "the remote client attached to the session" 150 session_attached
pane_shows_any_turn() {
  local out
  out=$("$PLUTO" attach "$BOX_ID" -- tmux capture-pane -p -t agent 2>/dev/null) || return 1
  grep -q "pluto-stand-in-agent turn=" <<<"$out"
}
poll_until "the session pane is live through the remote attach" 60 pane_shows_any_turn
detach_session agent
poll_until "the remote client detaches" 90 pty_gone
stop_pty
assert_contains "the box is still running after the remote attach" "$("$PLUTO" status "$BOX_ID")" "state:    running"

step "destroying the box"
DESTROY_OUT=$("$PLUTO" destroy "$BOX_ID" --yes) || fail "pluto destroy"
assert_contains "box destroyed" "$DESTROY_OUT" "destroyed box"
BOX_ID=""
LS=$("$PLUTO" ls)
assert_not_contains "destroy removed the box from pluto ls" "$LS" "$PROJECT/main"
BOXES_LEFT=$(ls -A "$PLUTO_STATE_DIR/boxes" 2>/dev/null || true)
[ -z "$BOXES_LEFT" ] || fail "destroy removed the box disk (boxes dir still has: $BOXES_LEFT)"
pass "destroy removed the box disk"

printf '\ne2e: all checks passed in %ss\n' "$((SECONDS - STARTED_AT))"
