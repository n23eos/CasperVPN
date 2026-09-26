#!/usr/bin/env bash
# reconcile-live-guard.sh — B1: the production reconcile wrapper is wired and
# fail-closed. Proves the DECISION logic (echo contract, egress==exit, >=2 gate,
# no-rotate apply, exit-evidence-from-entry, PSK off argv) with the network
# primitives stubbed. It does NOT assert a live data plane — that is GATE-PAID.
set -uo pipefail
cd "$(dirname "$0")/../.."
export RECONCILE_LIVE_LIB_ONLY=true
# shellcheck source=../../infra/scripts/reconcile_live.sh
source infra/scripts/reconcile_live.sh
set +e

pass=0; fail=0
ok()  { echo "  ok: $1"; pass=$((pass+1)); }
bad() { echo "  FAIL: $1" >&2; fail=$((fail+1)); }

# --- hooks are defined ---
for h in hook_verify_exit hook_apply hook_probe; do
  declare -F "$h" >/dev/null && ok "hook defined: $h" || bad "missing hook: $h"
done
# main wires them to the reconciler's RECONCILE_* env
grep -q 'RECONCILE_VERIFY_EXIT=hook_verify_exit' infra/scripts/reconcile_live.sh \
  && grep -q 'RECONCILE_APPLY_CMD=hook_apply' infra/scripts/reconcile_live.sh \
  && grep -q 'RECONCILE_PROBE_CMD=hook_probe' infra/scripts/reconcile_live.sh \
  && ok "main wires all three live hooks" || bad "hooks not wired in main"
if grep -q '\.type == "vless_reality"' infra/scripts/reconcile_live.sh infra/scripts/reconcile_fleet_access.sh; then
  bad "transport comparison uses union key instead of canonical enum"
else
  ok "VLESS transport comparisons use canonical vless-reality enum"
fi

# --- echo contract: strict, fail-closed ---
[ "$(echo_observed_ip '{"ip":"203.0.113.9"}')" = "203.0.113.9" ] && ok "echo parses {\"ip\":..}" || bad "echo parse"
( echo_observed_ip '<html>not json</html>' ) >/dev/null 2>&1 && bad "echo accepted non-JSON" || ok "echo rejects non-JSON (fail-closed)"
( echo_observed_ip '{"other":1}' ) >/dev/null 2>&1 && bad "echo accepted missing ip" || ok "echo rejects missing ip"

# --- hook_verify_exit: entry-host evidence, egress==exit, PSK off argv ---
export ENTRY_IP=192.0.2.10 EXIT_IP=198.51.100.20 EGRESS_ECHO_URL=https://echo.test/ip
export PAIR_PSK="SECRET-PSK-DO-NOT-LOG-$(head -c 16 /dev/zero | base64)"
export RL_REMOTE_VERIFY=infra/scripts/remote/verify_exit_remote.sh
SSH_LOG="$(mktemp)"
# stub the entry-host primitive: staging call drains the piped PSK; run call
# returns the canned echo body.
rl_ssh_entry() {
  echo "ARGS: $*" >>"$SSH_LOG"
  if [[ "$*" == *bash* ]]; then printf '%s' "$ECHO_BODY"; else cat >/dev/null; fi
}
ECHO_BODY='{"ip":"198.51.100.20"}'; ( hook_verify_exit ) >/dev/null 2>&1 && ok "verify passes when egress==exit" || bad "verify rejected a valid exit"
ECHO_BODY='{"ip":"10.9.9.9"}';      ( hook_verify_exit ) >/dev/null 2>&1 && bad "verify passed when egress!=exit" || ok "verify fails when egress!=exit"
ECHO_BODY='forbidden';              ( hook_verify_exit ) >/dev/null 2>&1 && bad "verify passed on a junk echo" || ok "verify fails closed on a junk echo"
# the run always goes to the ENTRY host, and the PSK is NEVER an argument
grep -q 'bash' "$SSH_LOG" && ok "exit evidence runs via the entry host (ssh)" || bad "verify did not use the entry host"
grep -qF "$PAIR_PSK" "$SSH_LOG" && bad "PSK leaked into ssh argv" || ok "PSK never passed as argv (stdin only)"
rm -f "$SSH_LOG"

# --- hook_probe: >=2 distinct verified transports gate ---
IDENTITY_SNAPSHOT="$(mktemp)"
printf '%s' '{"users":[{"uuid":"canonical-user","short_id":"aa","hysteria2_password":"canonical-hy2"}]}' >"$IDENTITY_SNAPSHOT"
export RECON_ACCESS_SNAPSHOT_FILE="$IDENTITY_SNAPSHOT" ENTRY=cloud:entry
cp_get_node() {
  printf '%s' '{"transports":[
    {"enabled":true,"type":"vless-reality","vless_reality":{"public_key":"canonical-pub","server_names":["canonical-sni.test"]}},
    {"enabled":true,"type":"hysteria2","hysteria2":{"sni":"canonical-hy2.test"}}]}'
}
if rl_load_probe_identity \
   && [ "$RL_VLESS_PUBKEY" = canonical-pub ] \
   && [ "$RL_HY2_PASSWORD" = canonical-hy2 ]; then
  ok "probe identity loads canonical transport enums and personal credentials"
else
  bad "probe identity rejected canonical transport fixture"
fi
rm -f "$IDENTITY_SNAPSHOT"
PROBE_HY2=true
rl_load_probe_identity() {
  export RL_VLESS_UUID=u RL_VLESS_SHORT_ID=ab RL_VLESS_PUBKEY=pk RL_REALITY_SNI=sni.test RL_HY2_SNI=sni.test
  if [ "$PROBE_HY2" = true ]; then export RL_HY2_PASSWORD=pw; else unset RL_HY2_PASSWORD; fi
}
rl_run_client_probe() { _rl_probe_verdict "$1" true true; }   # both transports verified
res="$(hook_probe)"
[ "$(jq 'length' <<<"$res")" = 2 ] && ok "probe emits both transports" || bad "probe did not emit 2: $res"
transport_gate "$res" && ok "gate passes with 2 verified transports" || bad "gate rejected 2 verified"
# only one transport -> gate fails closed
rl_run_client_probe() { _rl_probe_verdict "$1" true true; }
PROBE_HY2=false          # hy2 client can't build -> only vless
res1="$(hook_probe)"
transport_gate "$res1" && bad "gate passed with <2 transports" || ok "gate fails closed with <2 transports"
PROBE_HY2=true

# --- hook_apply: reuses on-node state, never rotates secrets ---
VARS_SEEN="$(mktemp)"
RECON_ACCESS_SNAPSHOT_FILE="$(mktemp)"; export RECON_ACCESS_SNAPSHOT_FILE
export RUN_ID=run-test ENTRY=cloud:entry
printf '%s' '{"revision":"r1","valid_until":"2999-01-01T00:00:00Z","users":[{"uuid":"u","short_id":"ab","hysteria2_password":"pw"}]}' >"$RECON_ACCESS_SNAPSHOT_FILE"
rl_sync_access() { cp "$1" "$VARS_SEEN"; return 0; }
( hook_apply ) >/dev/null 2>&1 && ok "apply converges" || bad "apply failed"
jq -e '.revision == "r1" and .users[0].hysteria2_password == "pw"' "$VARS_SEEN" >/dev/null 2>&1 && ok "apply uses the authoritative access snapshot" || bad "apply did not use access snapshot"
rm -f "$VARS_SEEN" "$RECON_ACCESS_SNAPSHOT_FILE"

echo "reconcile-live-guard: ${pass} ok, ${fail} fail"
[ "$fail" = 0 ]
