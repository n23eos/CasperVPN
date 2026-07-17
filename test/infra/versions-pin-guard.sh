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

grep -q 'source infra/versions.sh' test/e2e/real-node.sh \
  || { echo "FAIL: real-node.sh does not source infra/versions.sh" >&2; fail=1; }
if grep -qE 'sing-box:v[0-9]+\.[0-9]+\.[0-9]+' test/e2e/real-node.sh; then
  echo "FAIL: real-node.sh still hardcodes a sing-box version literal" >&2; fail=1
fi

[ "$fail" = 0 ] && echo "versions-pin-guard: ok (sing-box ${SINGBOX_VERSION}, single source)"
exit "$fail"
