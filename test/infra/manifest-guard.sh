#!/usr/bin/env bash
# manifest-guard.sh — B3/B5: logical CP ids, isolated-workspace enforcement, and
# the 0600 secret-free run manifest. Pure shell, no cloud.
set -uo pipefail
cd "$(dirname "$0")/../.."
# shellcheck source=../../infra/scripts/lib.sh
source infra/scripts/lib.sh
# shellcheck source=../../infra/scripts/run_manifest.sh
source infra/scripts/run_manifest.sh
set +e

pass=0; fail=0
ok()  { echo "  ok: $1"; pass=$((pass+1)); }
bad() { echo "  FAIL: $1" >&2; fail=$((fail+1)); }
perms() { stat -c '%a' "$1" 2>/dev/null || stat -f '%Lp' "$1"; }

# --- logical id (B5) ---
[ "$(logical_id hetzner 123)" = "hetzner:123" ] && ok "logical_id namespaces by cloud" || bad "logical_id format"
( logical_id "" 1 ) >/dev/null 2>&1 && bad "logical_id accepted empty cloud" || ok "logical_id rejects empty cloud"
( logical_id vultr "" ) >/dev/null 2>&1 && bad "logical_id accepted empty raw id" || ok "logical_id rejects empty raw id"

# --- workspace isolation (B3) ---
( RUN_ID=r1 TF_WORKSPACE=default require_live_run ) 2>/dev/null && bad "accepted default workspace" || ok "rejects default workspace"
( TF_WORKSPACE=ws1 require_live_run ) 2>/dev/null && bad "accepted missing RUN_ID" || ok "requires RUN_ID"
( RUN_ID='bad id!' TF_WORKSPACE=ws1 require_live_run ) 2>/dev/null && bad "accepted bad RUN_ID" || ok "validates RUN_ID charset"
( RUN_ID=run-1 TF_WORKSPACE=run-1 require_live_run ) 2>/dev/null && ok "accepts isolated workspace" || bad "rejected a valid live run"

# --- manifest write/read + perms + no secrets ---
RUN_DIR="$(mktemp -d)"; export RUN_DIR
export RUN_ID=run-test TF_WORKSPACE=ws-run-test
export ENTRY_LOGICAL_ID=hetzner:e1 EXIT_LOGICAL_ID=vultr:x1 ENTRY_RAW_ID=e1 EXIT_RAW_ID=x1
export ENTRY_CLOUD=hetzner EXIT_CLOUD=vultr ENTRY_REGION=hel1 EXIT_REGION=waw
export ENTRY_IP=192.0.2.1 EXIT_IP=198.51.100.1 STATE_BACKUP="${RUN_DIR}/run-test.tfstate.backup"
p="$(write_manifest)"
[ -f "$p" ] && ok "manifest written" || bad "manifest not written"
[ "$(perms "$p")" = "600" ] && ok "manifest is 0600" || bad "manifest perms = $(perms "$p"), want 600"
[ "$(manifest_field run-test '.entry.cp_id')" = "hetzner:e1" ] && ok "reads entry logical id" || bad "entry cp_id"
[ "$(manifest_field run-test '.exit.cp_id')" = "vultr:x1" ] && ok "reads exit logical id" || bad "exit cp_id"
[ "$(manifest_field run-test '.exit.cloud')" = "vultr" ] && ok "exit cloud is vultr (not entry cloud)" || bad "exit cloud"
[ "$(manifest_field run-test '.exit.region')" = "waw" ] && ok "exit region is exit-region" || bad "exit region"
[ "$(manifest_field run-test '.tf_workspace')" = "ws-run-test" ] && ok "records workspace" || bad "workspace"
if grep -qiE 'psk|secret|private|token|password' "$p"; then bad "manifest appears to leak a secret"; else ok "manifest carries no secrets"; fi
( manifest_field no-such-run '.entry.cp_id' ) >/dev/null 2>&1 && bad "read a missing manifest" || ok "missing manifest fails closed"

# --- stub manifest (written BEFORE apply) so a partial apply is still tearable ---
export RUN_ID=stub-test TF_WORKSPACE=ws-stub
sp="$(write_stub_manifest)"
[ -f "$sp" ] && [ "$(perms "$sp")" = "600" ] && ok "stub manifest written 0600" || bad "stub manifest perms"
[ "$(manifest_field stub-test '.tf_workspace')" = "ws-stub" ] && ok "stub records the workspace" || bad "stub workspace"
[ "$(manifest_field stub-test '.entry.cp_id')" = "" ] && ok "stub has empty ids (nothing provisioned yet)" || bad "stub ids not empty"
rm -rf "$RUN_DIR"

echo "manifest-guard: ${pass} ok, ${fail} fail"
[ "$fail" = 0 ]
