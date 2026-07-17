#!/usr/bin/env bash
# reconcile-state-guards.sh — pure-shell regression for the reconciler state
# machine (no docker/CP). Sources reconcile_node.sh in lib-only mode, mocks the
# control-plane (_cp) and the node hooks, and asserts the rollback/retry model:
# demotion is a hard barrier, any failure after exit activation rolls the exit
# back, a retry after a partial failure completes, and locking is mutually exclusive.
set -uo pipefail
cd "$(dirname "$0")/../.."
export RECONCILE_LIB_ONLY=true
# shellcheck source=/dev/null
source infra/scripts/reconcile_node.sh
set +e   # reconcile_node.sh sets -e; the guards drive failures deliberately

pass=0; fail=0
ok()  { echo "  ok: $1"; pass=$((pass+1)); }
bad() { echo "  FAIL: $1" >&2; fail=$((fail+1)); }

# --- mock control-plane -------------------------------------------------------
# Note: _cp GET runs inside $(...) — a subshell — so the reality-users call
# counter is kept in a FILE (survives subshells). Reads of RU2_REV work because a
# subshell inherits the parent's variables; only WRITES would not propagate.
RU_CALL_FILE="$(mktemp)"
reset() { ACTIVATED=(); DEMOTED=(); : >"$RU_CALL_FILE"; FAIL_DEMOTE=""; FAIL_ACTIVATE=""; FAIL_CP_TIMEOUT=""; RU2_REV="R1"; }
_cp() {
  local m="$1" p="$2"
  # Simulate a hung control-plane: the bounded curl exits 22 (timeout), which the
  # real _cp maps to return 22. Any matching call fails as a timeout would.
  if [ -n "${FAIL_CP_TIMEOUT:-}" ] && [[ "$m $p" == $FAIL_CP_TIMEOUT ]]; then return 22; fi
  case "$m $p" in
    "GET "*"/reality-users")
      local n; n=$(( $(wc -l <"$RU_CALL_FILE" 2>/dev/null || echo 0) + 1 )); echo x >>"$RU_CALL_FILE"
      if [ "$n" -ge 2 ]; then echo "{\"revision\":\"${RU2_REV}\",\"users\":[]}"; else echo '{"revision":"R1","users":[{"uuid":"u","short_id":"s"}]}'; fi
      return 0 ;;
    "GET /v1/nodes/"*) echo '{"id":"n","role":"entry","status":"active","transports":[]}'; return 0 ;;
    "POST /v1/nodes/"*"/demote")
      local id="${p#/v1/nodes/}"; id="${id%/demote}"; DEMOTED+=("$id")
      [ "$FAIL_DEMOTE" = "$id" ] && return 1 || return 0 ;;
    "POST /v1/nodes/"*"/activate")
      local id="${p#/v1/nodes/}"; id="${id%/activate}"
      [ "$FAIL_ACTIVATE" = "$id" ] && return 1
      ACTIVATED+=("$id"); return 0 ;;
  esac
  return 0
}
has() { local x="$1"; shift; for e in "$@"; do [ "$e" = "$x" ] && return 0; done; return 1; }
count() { local x="$1"; shift; local c=0; for e in "$@"; do [ "$e" = "$x" ] && c=$((c+1)); done; echo "$c"; }

# hooks
verify_ok() { return 0; }
verify_bad() { return 1; }
apply_ok() { return 0; }
apply_bad() { return 1; }
probe_good() { echo '[{"type":"vless-reality","authenticated_http":true,"exit_ip_verified":true},{"type":"hysteria2","authenticated_http":true,"exit_ip_verified":true}]'; }
probe_bad()  { echo '[{"type":"vless-reality","authenticated_http":true,"exit_ip_verified":true}]'; }  # only 1 distinct

export RECONCILE_VERIFY_EXIT=verify_ok RECONCILE_APPLY_CMD=apply_ok RECONCILE_PROBE_CMD=probe_good

# --- 1. demotion failure is a hard barrier: nothing gets activated -------------
reset; FAIL_DEMOTE="en"
if reconcile_pair en ex >/dev/null 2>&1; then bad "reconcile continued past a failed demotion"; else
  { [ "${#ACTIVATED[@]}" -eq 0 ]; } && ok "demotion failure aborts before any activation" || bad "activated despite demotion failure: ${ACTIVATED[*]}"
fi

# --- 2. failure after exit activation rolls the exit back ----------------------
reset; RECONCILE_PROBE_CMD=probe_bad   # gate fails after exit is active
reconcile_pair en ex >/dev/null 2>&1 && bad "reconcile succeeded with <2 transports"
has ex "${ACTIVATED[@]}" && ok "exit was activated (step 2 reached)" || bad "exit not activated"
has en "${ACTIVATED[@]}" && bad "entry activated despite probe failure" || ok "entry NOT activated on probe failure"
[ "$(count ex "${DEMOTED[@]}")" -ge 2 ] && ok "exit demoted again (rolled back) after failure" || bad "exit not rolled back (demoted x$(count ex "${DEMOTED[@]}"))"
RECONCILE_PROBE_CMD=probe_good

# --- 2c. a control-plane timeout after exit activation rolls the exit back -----
reset; FAIL_CP_TIMEOUT="GET *reality-users"
reconcile_pair en ex >/dev/null 2>&1 && bad "reconcile succeeded despite a CP timeout"
has ex "${ACTIVATED[@]}" && ok "exit activated before the CP timeout (step 2 reached)" || bad "exit not activated"
has en "${ACTIVATED[@]}" && bad "entry activated despite CP timeout" || ok "entry NOT activated on CP timeout"
[ "$(count ex "${DEMOTED[@]}")" -ge 2 ] && ok "exit rolled back after CP timeout" || bad "exit not rolled back on CP timeout"

# --- 3. R2 != R1 (raced ban/rotation) aborts and rolls back --------------------
reset; RU2_REV="R2-changed"
reconcile_pair en ex >/dev/null 2>&1 && bad "activated despite R2 != R1"
has en "${ACTIVATED[@]}" && bad "entry activated despite revision race" || ok "revision race blocks entry activation"
[ "$(count ex "${DEMOTED[@]}")" -ge 2 ] && ok "exit rolled back on revision race" || bad "exit not rolled back on race"

# --- 4. clean run / retry after a partial failure completes --------------------
reset
if reconcile_pair en ex >/dev/null 2>&1; then
  { has ex "${ACTIVATED[@]}" && has en "${ACTIVATED[@]}"; } && ok "clean run activates exit then entry" || bad "clean run missing activations: ${ACTIVATED[*]}"
else bad "clean run failed"; fi

# --- 4b. signal/crash after exit activation -> cleanup trap rolls exit back ----
reset; RECON_ROLLBACK_EXIT="ex"; RECON_LOCK=""
reconcile_cleanup
{ has ex "${DEMOTED[@]}" && [ -z "$RECON_ROLLBACK_EXIT" ]; } \
  && ok "cleanup trap demotes the exit on interruption (crash/signal-safe)" || bad "cleanup did not roll exit back (demoted=${DEMOTED[*]}, flag='$RECON_ROLLBACK_EXIT')"

# --- 5. lock contention (portable mkdir path) ---------------------------------
LP="$(mktemp -u)"
if LOCK_FORCE_MKDIR=true acquire_lock "$LP"; then
  # holding it; a second acquire must fail, then release makes it acquirable again.
  if LOCK_FORCE_MKDIR=true acquire_lock "$LP" 2>/dev/null; then bad "second lock acquire succeeded (no mutual exclusion)"; else ok "lock is mutually exclusive"; fi
  release_lock "$LP"
  LOCK_FORCE_MKDIR=true acquire_lock "$LP" 2>/dev/null && { ok "lock reacquirable after release"; release_lock "$LP"; } || bad "lock not reacquirable after release"
else bad "could not acquire lock"; fi

echo
if [ "$fail" -eq 0 ]; then echo "PASS: reconcile state-machine guards ($pass checks)"; else echo "FAIL: $fail failed" >&2; exit 1; fi
