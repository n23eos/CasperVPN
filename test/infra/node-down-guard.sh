#!/usr/bin/env bash
# node-down-guard.sh — B2: teardown is cost-safe (a retire/drain failure never
# blocks the destroy; only a destroy failure is fatal) and idempotent (a repeat
# run reads the durable manifest, not `terraform output`, and is a no-op). Pure
# shell: terraform/ansible-playbook/curl are stubbed on PATH.
set -uo pipefail
cd "$(dirname "$0")/../.."
ROOT="$(pwd)"

pass=0; fail=0
ok()  { echo "  ok: $1"; pass=$((pass+1)); }
bad() { echo "  FAIL: $1" >&2; fail=$((fail+1)); }

BIN="$(mktemp -d)"
cat >"$BIN/terraform" <<'SH'
#!/usr/bin/env bash
sub=""; for a in "$@"; do case "$a" in -chdir=*) ;; -*) ;; *) sub="$a"; break;; esac; done
case "$sub" in
  destroy) [ "${FAKE_DESTROY_FAIL:-0}" = 1 ] && exit 1 || exit 0 ;;
  output)  exit 1 ;;   # node_down must NOT depend on terraform output
  *)       exit 0 ;;   # init / workspace
esac
SH
cat >"$BIN/ansible-playbook" <<'SH'
#!/usr/bin/env bash
exit "${FAKE_DRAIN_FAIL:-0}"
SH
cat >"$BIN/curl" <<'SH'
#!/usr/bin/env bash
printf '%s' "${FAKE_RETIRE_CODE:-204}"   # cp_retire_node reads the http code
SH
chmod +x "$BIN"/*
export PATH="$BIN:$PATH"

RUN_DIR="$(mktemp -d)"; export RUN_DIR
export RUN_ID=down-test
export CONTROL_PLANE_URL=http://cp.invalid CONTROL_PLANE_TOKEN=t SSH_PUBKEY=unused
# Seed a durable manifest (as node_up would have written).
jq -n '{run_id:"down-test", tf_workspace:"ws-down-test",
  entry:{cp_id:"hetzner:e1", raw_id:"e1", ip:"192.0.2.1", cloud:"hetzner", region:"hel1"},
  exit:{cp_id:"vultr:x1", raw_id:"x1", ip:"198.51.100.1", cloud:"vultr", region:"waw"},
  state_backup:""}' >"$RUN_DIR/down-test.json"

run_down() { bash infra/scripts/node_down.sh 2>&1; }

# --- A. retire fails but the destroy STILL runs (cost-safe), exit 0 with warning ---
out="$(FAKE_RETIRE_CODE=500 run_down)"; rc=$?
if [ "$rc" -eq 0 ] && grep -q 'infra destroyed' <<<"$out" && grep -qi 'NON-FATAL' <<<"$out"; then
  ok "retire failure does not block destroy (cost-safe)"
else
  bad "retire failure was not cost-safe (rc=$rc): $out"
fi
# both nodes were attempted for retire
grep -q 'hetzner:e1' <<<"$out" && grep -q 'vultr:x1' <<<"$out" && ok "both nodes targeted for retire" || bad "did not retire both nodes"

# --- B. destroy failure is FATAL with an emergency manual command ---
out="$(FAKE_RETIRE_CODE=204 FAKE_DESTROY_FAIL=1 run_down)"; rc=$?
if [ "$rc" -ne 0 ] && grep -qi 'EMERGENCY' <<<"$out"; then
  ok "destroy failure exits non-zero with an emergency command"
else
  bad "destroy failure not fatal/emergency (rc=$rc): $out"
fi

# --- C. idempotent: repeat run (post-destroy: no tf output) reads the manifest, no-op ---
out="$(FAKE_RETIRE_CODE=404 run_down)"; rc=$?      # 404 = already retired, tolerated
if [ "$rc" -eq 0 ] && grep -q 'infra destroyed' <<<"$out"; then
  ok "repeat down is a clean no-op from the manifest"
else
  bad "repeat down failed (rc=$rc): $out"
fi
[ -f "$RUN_DIR/down-test.json" ] && ok "manifest preserved across teardown" || bad "manifest lost"

# --- D. no manifest => teardown fails closed (won't guess) ---
out="$(RUN_ID=no-such bash infra/scripts/node_down.sh 2>&1)"; rc=$?
[ "$rc" -ne 0 ] && grep -qi 'manifest not found' <<<"$out" && ok "missing manifest fails closed" || bad "missing manifest not handled"

# --- E. partial-apply STUB manifest (empty CP ids): destroy the orphan VM, no retire crash ---
jq -n '{run_id:"stub-run", tf_workspace:"ws-stub-run",
  entry:{cp_id:"", raw_id:"", ip:"", cloud:"", region:""},
  exit:{cp_id:"", raw_id:"", ip:"", cloud:"", region:""}, state_backup:""}' >"$RUN_DIR/stub-run.json"
out="$(RUN_ID=stub-run FAKE_RETIRE_CODE=204 run_down)"; rc=$?
if [ "$rc" -eq 0 ] && grep -q 'infra destroyed' <<<"$out" && grep -qi 'partial apply' <<<"$out"; then
  ok "partial-apply stub: reaps the VM without a retire crash"
else
  bad "partial-apply stub not handled (rc=$rc): $out"
fi

rm -rf "$BIN" "$RUN_DIR"
echo "node-down-guard: ${pass} ok, ${fail} fail"
[ "$fail" = 0 ]
