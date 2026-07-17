#!/usr/bin/env bash
# reconcile-signal-guard.sh — process-level signal test for the reconciler (no
# docker/CP). Spawns a real child reconciler whose apply hook BLOCKS after the
# exit is activated, sends it SIGTERM, and asserts the signal actually terminates
# it (bash otherwise resumes after the handler): the exit is rolled back, the
# process exits 143, the code AFTER the hook never runs (entry not activated), and
# the lock is released so a new reconciler can acquire it.
set -uo pipefail
cd "$(dirname "$0")/../.."

pass=0; fail=0
ok()  { echo "  ok: $1"; pass=$((pass+1)); }
bad() { echo "  FAIL: $1" >&2; fail=$((fail+1)); }

TMP="$(mktemp -d)"
ACTIONS="$TMP/actions"; APPLY_STARTED="$TMP/apply_started"; : >"$ACTIONS"
EN="en-$(openssl rand -hex 3)"; EX="ex-$(openssl rand -hex 3)"
LOCKDIR="/tmp/caspervpn-reconcile-${EN}.d"
CHILD="$TMP/child.sh"

cat >"$CHILD" <<CHILD_EOF
#!/usr/bin/env bash
set -uo pipefail
cd "$(pwd)"
export RECONCILE_LIB_ONLY=true
source infra/scripts/reconcile_node.sh
set +e
# mock CP: record every call; GET reality-users returns a stable revision.
_cp() {
  echo "\$1 \$2" >> "$ACTIONS"
  case "\$1 \$2" in
    "GET "*"/reality-users") echo '{"revision":"R1","users":[]}' ;;
    "GET /v1/nodes/"*) echo '{"id":"n","status":"active"}' ;;
  esac
  return 0
}
_verify(){ return 0; }
_apply(){ touch "$APPLY_STARTED"; sleep 30; }   # blocks AFTER exit is active
_probe(){ echo '[]'; }
export RECONCILE_VERIFY_EXIT=_verify RECONCILE_APPLY_CMD=_apply RECONCILE_PROBE_CMD=_probe
export CONTROL_PLANE_URL=http://mock CONTROL_PLANE_TOKEN=t ENTRY="$EN" EXIT="$EX"
export LOCK_FORCE_MKDIR=true
reconcile_main
CHILD_EOF
chmod +x "$CHILD"

# launch child, wait until it has activated the exit and blocked in apply.
"$CHILD" >/dev/null 2>&1 &
CHILD_PID=$!
for _ in $(seq 1 50); do [ -f "$APPLY_STARTED" ] && break; sleep 0.2; done
[ -f "$APPLY_STARTED" ] || bad "child never reached apply (exit not activated?)"

# the lock must be held while the child runs.
[ -d "$LOCKDIR" ] && ok "lock held while reconcile runs" || bad "lock not held during run"

# send SIGTERM and reap.
kill -TERM "$CHILD_PID" 2>/dev/null
wait "$CHILD_PID"; RC=$?

[ "$RC" = 143 ] && ok "child exits 143 on SIGTERM (signal terminates)" || bad "child exit code = $RC, want 143"
grep -q "POST /v1/nodes/${EX}/activate" "$ACTIONS" && ok "exit was activated before the signal" || bad "exit not activated pre-signal"
# rollback: the exit is demoted again after activation (>=2 demotes of the exit).
[ "$(grep -c "POST /v1/nodes/${EX}/demote" "$ACTIONS")" -ge 2 ] && ok "exit rolled back on signal" || bad "exit not rolled back on signal"
# the code AFTER the blocking hook must never run: the entry is never activated.
grep -q "POST /v1/nodes/${EN}/activate" "$ACTIONS" && bad "entry activated despite signal (bash resumed after handler!)" || ok "entry NOT activated after signal"
# lock released -> reacquirable.
[ ! -d "$LOCKDIR" ] && ok "lock released on signal (reacquirable)" || bad "lock still held after signal"

rm -rf "$TMP" "$LOCKDIR" 2>/dev/null
echo
if [ "$fail" -eq 0 ]; then echo "PASS: reconciler signal handling ($pass checks)"; else echo "FAIL: $fail failed" >&2; exit 1; fi
