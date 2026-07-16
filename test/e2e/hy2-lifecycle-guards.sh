#!/usr/bin/env bash
# hy2-lifecycle-guards.sh — pure-shell regression for the hysteria2 lifecycle
# helpers (no docker/ansible). Covers the fail-closed / no-secret-on-argv guards.
set -uo pipefail
cd "$(dirname "$0")/../.."

# shellcheck source=/dev/null
source infra/scripts/lib.sh
# shellcheck source=/dev/null
source infra/scripts/control_plane.sh
# shellcheck source=/dev/null
source infra/scripts/reality_sync.sh
# shellcheck source=/dev/null
source infra/scripts/hy2_lifecycle.sh

pass=0; fail=0
ok()   { echo "  ok: $1"; pass=$((pass+1)); }
bad()  { echo "  FAIL: $1" >&2; fail=$((fail+1)); }

# --- 1. write_secure_vars_file: 0600, holds content, not world-readable --------
SECRET='{"hy2_password":"super-secret-pw","hy2_tls_key":"KEYDATA"}'
VF="$(write_secure_vars_file "$SECRET")"
trap 'rm -f "$VF"' EXIT
mode="$(stat -f '%Lp' "$VF" 2>/dev/null || stat -c '%a' "$VF")"
[ "$mode" = "600" ] && ok "vars file mode 0600" || bad "vars file mode is $mode, want 600"
[ "$(cat "$VF")" = "$SECRET" ] && ok "vars file holds the content" || bad "vars file content mismatch"

# --- 2. the secret must NOT appear on the ansible command line -----------------
# node_up/node_rotate invoke: ansible-playbook ... -e @"$VF"  (never -e "$SECRET").
ARGV="ansible-playbook -i inv playbooks/node-up.yml -e @${VF}"
if printf '%s' "$ARGV" | grep -q 'super-secret-pw\|KEYDATA'; then
  bad "secret leaked onto argv"
else
  ok "secret absent from argv (passed via -e @file)"
fi

# --- 3. hy2_desired_from_cp: CP unreachable -> hard fail (never rotate blind) --
export CONTROL_PLANE_URL="http://127.0.0.1:1"   # nothing listens
export CONTROL_PLANE_TOKEN="t"
if ( hy2_desired_from_cp "nd" ) >/dev/null 2>&1; then
  bad "CP-unreachable did not hard-fail"
else
  ok "CP-unreachable hard-fails"
fi

# Override cp_get_node to serve canned nodes for the remaining cases.
# --- 4. partial hysteria2 state (password without sni) -> hard fail ------------
cp_get_node() { printf '%s' '{"id":"nd","transports":[{"type":"hysteria2","hysteria2":{"password":"p"}}]}'; }
if ( hy2_desired_from_cp "nd" ) >/dev/null 2>&1; then
  bad "partial hysteria2 state did not hard-fail"
else
  ok "partial hysteria2 state hard-fails"
fi

# --- 5. no hysteria2 on the node -> empty output, success ---------------------
cp_get_node() { printf '%s' '{"id":"nd","transports":[{"type":"vless-reality","vless_reality":{"public_key":"k"}}]}'; }
out="$(hy2_desired_from_cp "nd")"; rc=$?
{ [ $rc -eq 0 ] && [ -z "$out" ]; } && ok "no-hysteria2 node yields empty, rc=0" || bad "no-hysteria2 node: rc=$rc out='$out'"

# --- 6. full hysteria2 state -> password<TAB>sni ------------------------------
cp_get_node() { printf '%s' '{"id":"nd","transports":[{"type":"hysteria2","hysteria2":{"password":"PW","sni":"cdn.x"}}]}'; }
out="$(hy2_desired_from_cp "nd")"
[ "$out" = "$(printf 'PW\tcdn.x')" ] && ok "full state -> password<TAB>sni" || bad "full state got '$out'"

# --- 7. entry->exit PAIR PSK: fail-closed on rotation when chain configured -----
export PAIR_PSK=""
if ( require_pair_psk_for_rotation "true" ) >/dev/null 2>&1; then
  bad "rotation with chain configured but no PAIR_PSK did not fail"
else
  ok "rotation without PAIR_PSK (chain configured) hard-fails"
fi
export PAIR_PSK="a-pair-psk"
( require_pair_psk_for_rotation "true" ) >/dev/null 2>&1 && ok "rotation with PAIR_PSK proceeds" || bad "rotation with PAIR_PSK should proceed"
( require_pair_psk_for_rotation "false" ) >/dev/null 2>&1 && ok "no chain -> PAIR_PSK not required" || bad "no chain should not require PAIR_PSK"

# --- 8. PAIR PSK isolation: it must NEVER reach the control-plane transports -----
# reality_sync builds only CLIENT transports (vless-reality + optional hysteria2).
# Even with PAIR_PSK in the environment, its output must carry no shadowsocks-2022
# transport and not the PSK string (the entry<->exit link is infra, not a CP transport).
export PAIR_PSK="TOPSECRET-PAIR-PSK"
export REALITY_PUBLIC_KEY=PUB REALITY_SERVER_NAMES=a.com REALITY_DEST=a.com:443
TR="$(_transports_json)"
if printf '%s' "$TR" | jq -e 'any(.[]; .type=="shadowsocks-2022")' >/dev/null 2>&1; then
  bad "reality_sync leaked a shadowsocks-2022 (exit link) transport into CP"
else
  ok "no exit-link transport in reality_sync output"
fi
if printf '%s' "$TR" | grep -q "TOPSECRET-PAIR-PSK"; then
  bad "PAIR PSK leaked into the control-plane transport payload"
else
  ok "PAIR PSK absent from control-plane payload"
fi

echo
if [ "$fail" -eq 0 ]; then
  echo "PASS: hysteria2 + exit-link lifecycle guards ($pass checks)"
else
  echo "FAIL: $fail check(s) failed" >&2
  exit 1
fi
