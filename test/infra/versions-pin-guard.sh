#!/usr/bin/env bash
# versions-pin-guard.sh — the sing-box pin has ONE source of truth
# (infra/versions.sh); the ansible role default is held equal to it, and
# real-node.sh sources it instead of carrying its own literal. Fails on drift.
set -uo pipefail
cd "$(dirname "$0")/../.."
fail=0

# shellcheck source=../../infra/versions.sh
source infra/versions.sh
[ -n "${SINGBOX_VERSION:-}" ] || { echo "FAIL: SINGBOX_VERSION unset in infra/versions.sh" >&2; fail=1; }

role="$(grep -E '^singbox_version:' infra/ansible/roles/singbox/defaults/main.yml | sed -E 's/.*"([^"]+)".*/\1/')"
if [ "$role" != "$SINGBOX_VERSION" ]; then
  echo "FAIL: ansible role default sing-box ${role} != versions.env ${SINGBOX_VERSION} (drift)" >&2; fail=1
fi

# No script anywhere may hardcode a sing-box image version — the pin is the ONLY
# source. Scan every e2e + infra script.
hits="$(grep -rnE 'sing-box:v[0-9]+\.[0-9]+\.[0-9]+' test/e2e/ infra/ 2>/dev/null || true)"
if [ -n "$hits" ]; then
  echo "FAIL: hardcoded sing-box version literal(s) — source infra/versions.sh instead:" >&2
  printf '%s\n' "$hits" >&2; fail=1
fi

[ "$fail" = 0 ] && echo "versions-pin-guard: ok (sing-box ${SINGBOX_VERSION}, single source)"
exit "$fail"
