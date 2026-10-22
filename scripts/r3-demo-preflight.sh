#!/usr/bin/env bash
# scripts/r3-demo-preflight.sh — read-only checks before the review-3 demo.
#
# It never starts a box, writes state, installs a key, or changes a label. It
# answers: is the daemon reachable, is a base image imported, does the demo
# checkout declare the three jobs and ship the six scripts, are the secret
# environment names visible here, and does the GitHub repository carry the two
# labels the loop needs.
#
# usage: scripts/r3-demo-preflight.sh [--contract DIR] [--github OWNER/REPO]
#
#   --contract DIR      the demo repository checkout (default: .)
#   --github OWNER/REPO check labels on this repository; skipped when omitted
#
# Environment:
#   PLUTO  pluto binary (default: pluto)
#   GH     gh binary (default: gh)
#
# The token and secret checks read this shell, not the daemon. The values must
# be in the daemon's environment for the webhook and the jobs to use them; the
# runbook shows the systemd EnvironmentFile and the foreground equivalent.
set -euo pipefail

PLUTO=${PLUTO:-pluto}
GH=${GH:-gh}
contract=.
github=

usage() {
  printf 'usage: %s [--contract DIR] [--github OWNER/REPO]\n' "$0" >&2
}

while [ $# -gt 0 ]; do
  case "$1" in
    --contract)
      [ $# -ge 2 ] && [ -n "$2" ] || { usage; exit 2; }
      contract=$2
      shift 2
      ;;
    --github)
      [ $# -ge 2 ] && [ -n "$2" ] || { usage; exit 2; }
      github=$2
      shift 2
      ;;
    -h|--help)
      sed -n '2,24p' "$0"
      exit 0
      ;;
    *)
      printf 'unknown argument %q\n' "$1" >&2
      usage
      exit 2
      ;;
  esac
done

fail=0
ok() { printf 'ok:   %s\n' "$*"; }
warn() { printf 'warn: %s\n' "$*"; }
bad() { printf 'FAIL: %s\n' "$*" >&2; fail=1; }

if ! command -v "$PLUTO" >/dev/null 2>&1; then
  bad "pluto is not on PATH (set PLUTO=/path/to/pluto)"
  printf '\npreflight failed; fix the FAIL lines above\n' >&2
  exit 1
fi

if "$PLUTO" health >/dev/null 2>&1; then
  ok "daemon reachable"
else
  bad "daemon not reachable; start it with 'pluto install' or 'pluto daemon'"
fi

images=$("$PLUTO" image ls 2>/dev/null || true)
if [ "$(printf '%s\n' "$images" | wc -l)" -gt 1 ]; then
  ok "base image imported"
else
  bad "no base image; import one with 'pluto image import images/out'"
fi

if [ -f "$contract/.pluto.toml" ]; then
  jobs=$("$PLUTO" job ls "$contract" 2>/dev/null || true)
  for job in heartbeat issue-fix pr-test; do
    if printf '%s\n' "$jobs" | awk '{print $1}' | grep -qx "$job"; then
      ok "contract declares job $job"
    else
      bad "contract in $contract does not declare job $job"
    fi
  done
  for script in pluto-demo-heartbeat pluto-demo-agent pluto-demo-test pluto-demo-provision pluto-issue-fix pluto-pr-test; do
    if [ -x "$contract/scripts/$script" ]; then
      ok "script $script is executable"
    else
      bad "$contract/scripts/$script is missing or not executable (copy it from the pluto checkout and chmod +x)"
    fi
  done
  if origin=$(git -C "$contract" remote get-url origin 2>/dev/null); then
    case "$origin" in
      https://*) ok "origin is HTTPS ($origin)" ;;
      *)
        bad "origin is $origin; the issue job pushes with a token over HTTPS, so the demo checkout needs an https:// origin"
        ;;
    esac
  else
    warn "no git origin in $contract; the GitHub loop needs an HTTPS origin"
  fi
else
  bad "$contract/.pluto.toml is missing (copy demo/r3/.pluto.toml to the demo repository root)"
fi

for name in PLUTO_ISSUE_TOKEN PLUTO_PR_TOKEN PLUTO_GITHUB_WEBHOOK_SECRET; do
  if [ -n "${!name:-}" ]; then
    ok "$name is set in this shell"
  else
    bad "$name is not set; export it in the daemon environment before the demo"
  fi
done

if [ -n "$github" ]; then
  if ! command -v "$GH" >/dev/null 2>&1; then
    bad "gh is not on PATH; install it or omit --github"
  else
    labels=$("$GH" label list --repo "$github" --limit 100 --json name --jq '.[].name' 2>/dev/null || true)
    for label in pluto:fix pluto:trusted; do
      if printf '%s\n' "$labels" | grep -qx "$label"; then
        ok "label $label exists on $github"
      else
        bad "label $label is missing on $github (create it with 'gh label create $label --repo $github')"
      fi
    done
  fi
else
  warn "labels not checked; pass --github OWNER/REPO to check pluto:fix and pluto:trusted"
fi

if [ "$fail" -ne 0 ]; then
  printf '\npreflight failed; fix the FAIL lines above\n' >&2
  exit 1
fi
printf '\npreflight passed; follow docs/review-3-demo.md\n'
