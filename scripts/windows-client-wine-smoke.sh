#!/usr/bin/env bash
#
# Optional local rehearsal of the Windows client under Wine. This is not
# Windows acceptance: it proves the shipped pluto.exe's argv handling, stream
# plumbing, exit codes, and %AppData% config path against a fake ssh, and it
# runs the repository's own Windows tests without a Windows machine. Real
# OpenSSH, Tailscale, and terminal behaviour are only proven on Windows.
#
# Wine is often installed outside the host, for example in a toolbox. The
# script builds on the host (Go lives there) and runs only the Wine commands in
# the environment that has Wine, named by a command prefix:
#
#   scripts/windows-client-wine-smoke.sh
#   WINE_RUNNER='toolbox run -c devbox' scripts/windows-client-wine-smoke.sh
#
# The scratch tree holds a Wine prefix (a few hundred MB) and defaults to
# ~/.cache/pluto-wine-smoke; it must be visible to the Wine environment.
# Override it with PLUTO_WINE_SMOKE_DIR. Exit status is the number of failed
# checks.
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
SMOKE=${PLUTO_WINE_SMOKE_DIR:-${XDG_CACHE_HOME:-"$HOME/.cache"}/pluto-wine-smoke}
RUNNER_SPEC=${WINE_RUNNER:-}
BIN="$SMOKE/bin"
PREFIX="$SMOKE/prefix"
INNER="$SMOKE/inner.sh"

if ! command -v go >/dev/null 2>&1; then
  echo "pluto: go is required to cross-build the Windows client" >&2
  exit 2
fi

echo "building the Windows client and the fake ssh"
mkdir -p "$BIN"
( cd "$ROOT" && GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o "$BIN/pluto.exe" ./cmd/pluto )
( cd "$ROOT" && GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o "$BIN/ssh.exe" ./internal/testssh/testdata/fakessh )
( cd "$ROOT" && GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o "$BIN/pluto-windows-test.exe" ./cmd/pluto )
( cd "$ROOT" && GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o "$BIN/remotecli-windows-test.exe" ./internal/remotecli )
( cd "$ROOT" && GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o "$BIN/devices-windows-test.exe" ./internal/devices )

# The check script is generated because it runs inside the Wine environment,
# where environment variables set on the host are not necessarily forwarded.
{
  printf 'BIN=%q\nSMOKE=%q\nPREFIX=%q\n' "$BIN" "$SMOKE" "$PREFIX"
  cat <<'INNER'
set -u
export WINEDEBUG=-all
export WINEPREFIX="$PREFIX"
WORK="$SMOKE/work"
rm -rf "$WORK"; mkdir -p "$WORK"

# The Windows form of a Unix path under this checkout's root filesystem, for
# the variables the Windows process reads.
winpath() { printf 'Z:%s' "$(printf '%s' "$1" | tr '/' '\\')"; }
export FAKE_SSH_LOG="$(winpath "$WORK/ssh.args")"

# A fresh prefix races Wine's own initialization, which makes the first run's
# file writes fail intermittently. Creating the prefix and waiting for the
# wineserver before any check removes that startup race; it is a no-op once the
# prefix exists.
wineboot -u >/dev/null 2>&1 || true
wineserver -w >/dev/null 2>&1 || true

# Wine builds the Windows PATH from its own registry and ignores the Unix
# PATH, so the fake ssh is put there where `ssh` is resolved.
wine reg add 'HKCU\Environment' /v PATH \
  /d "$(winpath "$BIN");C:\windows\system32;C:\windows" /f >/dev/null 2>&1

fails=0
check() { # desc expected actual
  if [ "$2" = "$3" ]; then echo "ok:   $1"; else echo "FAIL: $1 (want [$2] got [$3])"; fails=$((fails+1)); fi
}
contains() { # desc needle haystack
  case "$3" in *"$2"*) echo "ok:   $1";; *) echo "FAIL: $1 (missing [$2] in [$3])"; fails=$((fails+1));; esac
}

wine "$BIN/pluto.exe" version >"$WORK/version.out" 2>"$WORK/version.err"
check "version exits 0" "0" "$?"
contains "version prints the binary name" "pluto" "$(cat "$WORK/version.out")"

wine "$BIN/pluto.exe" status mybox >"$WORK/nodevice.out" 2>"$WORK/nodevice.err"
check "host command without --device exits 2" "2" "$?"
contains "no-device hint names device add" "device add" "$(cat "$WORK/nodevice.err")"

FAKE_SSH_EXIT=0 wine "$BIN/pluto.exe" device add neptuno sid@neptuno >"$WORK/add.out" 2>"$WORK/add.err"
check "device add exits 0" "0" "$?"
contains "device add reports the save" "saved device neptuno (sid@neptuno)" "$(cat "$WORK/add.out")"
contains "probe argv carries the host-key policy" "StrictHostKeyChecking=accept-new" "$(cat "$WORK/ssh.args")"
appdir=$(ls -d "$PREFIX"/drive_c/users/*/AppData/Roaming 2>/dev/null | head -1)
[ -f "$appdir/pluto/devices.toml" ] \
  && echo "ok:   devices.toml written under %AppData%" \
  || { echo "FAIL: devices.toml not under %AppData% ($appdir/pluto)"; fails=$((fails+1)); }

FAKE_SSH_EXIT=0 FAKE_SSH_STDOUT='remote says hi' FAKE_SSH_STDERR='remote says hmm' \
  wine "$BIN/pluto.exe" --device neptuno up --repo 'https://example.com/app.git' \
  >"$WORK/remote.out" 2>"$WORK/remote.err"
check "remote dispatch exits 0" "0" "$?"
contains "remote stdout streamed" "remote says hi" "$(cat "$WORK/remote.out")"
contains "remote stderr streamed" "remote says hmm" "$(cat "$WORK/remote.err")"
contains "remote argv keeps the repo URL" "https://example.com/app.git" "$(cat "$WORK/ssh.args")"

FAKE_SSH_EXIT=7 wine "$BIN/pluto.exe" --device neptuno status mybox >/dev/null 2>&1
check "remote exit status propagates" "7" "$?"

FAKE_SSH_EXIT=0 FAKE_SSH_ECHO_STDIN=1 wine "$BIN/pluto.exe" --device neptuno run mybox -- cat \
  >"$WORK/stdin.out" 2>/dev/null <<<"piped-through"
contains "stdin streams to the remote" "piped-through" "$(cat "$WORK/stdin.out")"

# The repository's own Windows tests, with the prebuilt binaries so the tests
# need no Go toolchain inside Wine.
export PLUTO_WIN_TEST_BINARY="$(winpath "$BIN/pluto.exe")"
export PLUTO_FAKE_SSH_BINARY="$(winpath "$BIN/ssh.exe")"
echo
echo "running the Windows test binaries under Wine"
for pkg in pluto remotecli devices; do
  if wine "$BIN/$pkg-windows-test.exe" -test.timeout 300s; then
    echo "ok:   $pkg windows tests passed"
  else
    echo "FAIL: $pkg windows tests failed"
    fails=$((fails+1))
  fi
done

echo
if [ "$fails" -eq 0 ]; then echo "wine smoke: all checks passed"; else echo "wine smoke: $fails check(s) failed"; fi
exit "$fails"
INNER
} >"$INNER"

read -r -a RUNNER <<<"$RUNNER_SPEC"
if [ "${#RUNNER[@]}" -eq 0 ] || [ -z "${RUNNER[0]:-}" ]; then
  bash "$INNER"
else
  "${RUNNER[@]}" bash "$INNER"
fi
