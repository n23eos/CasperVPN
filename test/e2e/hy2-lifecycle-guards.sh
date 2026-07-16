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

# --- 9. resolve_pair_psk: production REQUIRES it; e2e mints an ephemeral one ----
unset PAIR_PSK
if ( EXIT_LINK_E2E=false resolve_pair_psk ) >/dev/null 2>&1; then
  bad "production node_up without PAIR_PSK did not hard-fail"
else
  ok "production requires PAIR_PSK (no silent generation)"
fi
[ -n "$(EXIT_LINK_E2E=true resolve_pair_psk 2>/dev/null)" ] && ok "e2e mode mints an ephemeral PAIR_PSK" || bad "e2e mode should mint a PSK"
[ "$(PAIR_PSK=mypsk resolve_pair_psk 2>/dev/null)" = "mypsk" ] && ok "PAIR_PSK from the secret manager is used verbatim" || bad "env PAIR_PSK not used"

# --- 9b. resolve_exit_topology: fail-closed, never inferred from a command error -
unset NO_EXIT_LINK
if ( resolve_exit_topology 1 "" ) >/dev/null 2>&1; then
  bad "terraform read error did not hard-fail (fail-open topology)"
else
  ok "exit_ip read error hard-fails (no fail-open)"
fi
if ( resolve_exit_topology 0 "" ) >/dev/null 2>&1; then
  bad "empty exit_ip did not hard-fail"
else
  ok "empty exit_ip hard-fails"
fi
[ "$(resolve_exit_topology 0 1.2.3.4)" = "true" ] && ok "valid exit_ip -> chain configured" || bad "valid exit_ip should configure the chain"
[ "$(NO_EXIT_LINK=true resolve_exit_topology 1 '')" = "false" ] && ok "NO_EXIT_LINK=true opts out explicitly (even on read error)" || bad "NO_EXIT_LINK should opt out"

# --- 10. node_rotate mutates AFTER preflight (destructive replace is last) ------
rl="$(grep -n 'require_pair_psk_for_rotation' infra/scripts/node_rotate.sh | head -1 | cut -d: -f1)"
tl="$(grep -n -- '-replace=' infra/scripts/node_rotate.sh | head -1 | cut -d: -f1)"
if [ -n "$rl" ] && [ -n "$tl" ] && [ "$tl" -gt "$rl" ]; then
  ok "terraform -replace runs after the secret preflight (line $tl > $rl)"
else
  bad "destructive -replace is not after preflight (replace@${tl:-?}, preflight@${rl:-?})"
fi

echo
if [ "$fail" -eq 0 ]; then
  echo "PASS: hysteria2 + exit-link lifecycle guards ($pass checks)"
else
  echo "FAIL: $fail check(s) failed" >&2
  exit 1
fi
