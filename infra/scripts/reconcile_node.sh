#!/usr/bin/env bash
# reconcile_node.sh — declarative, fail-closed reconciler that promotes a
# provisioning entry+exit PAIR to active. It is the ONLY path to active: it syncs
# the user allow-list onto the node, verifies >=2 client transports protocol-aware,
# and calls the guarded /activate — exit first, entry last.
#
# STRICT rollback/retry model (so "any error leaves the pair provisioning" holds
# AND a re-run is idempotent):
#   0. serialize per pair (portable lock).
#   1. HARD-DEMOTE both entry and exit to provisioning up front. This is a barrier:
#      a CP error here aborts immediately (an old active entry must not keep
#      serving while we converge). Demoting both means every run starts clean, so
#      re-activating is never a 409-on-already-active.
#   2. verify the EXIT data plane -> activate the EXIT (evidence).
#   3. from here, ANY failure best-effort demotes the EXIT back to provisioning,
#      so a partial run leaves entry=provisioning AND exit=provisioning.
#   4. fetch R1 -> sync allow-list onto the entry -> converge.
#   5. re-fetch R2; R2 != R1 (raced ban/rotation) -> abort (roll back exit).
#   6. probe >=2 distinct verified client transports (transport_gate).
#   7. activate the ENTRY {expected_revision: R1}.
#
# Node mutation + probe-config building are caller hooks (RECONCILE_VERIFY_EXIT,
# RECONCILE_APPLY_CMD, RECONCILE_PROBE_CMD) so the DECISION logic is testable; the
# full drive is the e2e. Source with RECONCILE_LIB_ONLY=true to load functions
# without running.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "${HERE}/lib.sh"
# shellcheck source=control_plane.sh
source "${HERE}/control_plane.sh"
# shellcheck source=probe.sh
source "${HERE}/probe.sh"

# _cp METHOD PATH [body] -> echoes body; returns 0 on 2xx, the HTTP code otherwise.
_cp() {
  local method="$1" path="$2" body="${3:-}"
  local code out; out="$(mktemp)"
  if [ -n "$body" ]; then
    code="$(curl -sS -o "$out" -w '%{http_code}' -X "$method" "${CONTROL_PLANE_URL}${path}" \
      -H "$(_cp_auth)" -H 'Content-Type: application/json' -d "$body")" || { rm -f "$out"; return 22; }
  else
    code="$(curl -sS -o "$out" -w '%{http_code}' -X "$method" "${CONTROL_PLANE_URL}${path}" -H "$(_cp_auth)")" \
      || { rm -f "$out"; return 22; }
  fi
  cat "$out"; rm -f "$out"
  case "$code" in 2??) return 0 ;; *) return 1 ;; esac
}

# _cp_demote <id> -> set the node's status to provisioning. PATCH /v1/nodes is a
# full-body replace, so GET first, flip status, PATCH the whole object back.
_cp_demote() {
  local id="$1" node
  node="$(_cp GET "/v1/nodes/${id}")" || return 1
  node="$(jq -c '.status = "provisioning"' <<<"$node")" || return 1
  _cp PATCH "/v1/nodes/${id}" "$node" >/dev/null || return 1
}

# reconcile_pair <entry-id> <exit-id> -> 0 iff the entry ends active. Any failure
# leaves BOTH provisioning.
reconcile_pair() {
  local entry="$1" exit_id="$2"

  # 1. Barrier: hard-demote both. A CP error aborts before anything is activated.
  _cp_demote "$entry"   || { log "reconcile: demote entry ${entry} failed — aborting (node may still be serving; not converging)"; return 1; }
  _cp_demote "$exit_id" || { log "reconcile: demote exit ${exit_id} failed — aborting"; return 1; }
  log "reconcile: entry+exit demoted to provisioning"

  # 2. Exit first — the reconciler's authenticated probe is the evidence.
  if ! "${RECONCILE_VERIFY_EXIT:?RECONCILE_VERIFY_EXIT hook required}"; then
    log "reconcile: exit ${exit_id} data-plane not verified — leaving provisioning"; return 1
  fi
  _cp POST "/v1/nodes/${exit_id}/activate" "$(jq -n '{expected_revision:"", evidence:"exit_data_plane_verified"}')" >/dev/null \
    || { log "reconcile: exit activation refused — leaving provisioning"; return 1; }
  log "reconcile: exit ${exit_id} active"

  # 3. From here roll the exit back on any failure.
  local rc=0
  _reconcile_after_exit "$entry" || rc=$?
  if [ "$rc" -ne 0 ]; then
    log "reconcile: failed after exit activation — rolling exit ${exit_id} back to provisioning"
    _cp_demote "$exit_id" || log "reconcile: WARN exit rollback demote failed — exit ${exit_id} may be left active"
    return "$rc"
  fi
  log "reconcile: pair active (exit-first, entry-last complete)"
}

# _reconcile_after_exit <entry-id> — steps 4-7 (sync, R2 gate, probe, activate).
_reconcile_after_exit() {
  local entry="$1" r1 users rev1 rev2 results
  r1="$(_cp GET "/v1/nodes/${entry}/reality-users")" || { log "reconcile: allow-list read failed"; return 1; }
  users="$(jq -c '.users' <<<"$r1")"; rev1="$(jq -r '.revision' <<<"$r1")"
  log "reconcile: syncing $(jq 'length' <<<"$users") users onto ${entry} (rev ${rev1:0:12}...)"
  RECONCILE_USERS="$users" "${RECONCILE_APPLY_CMD:?RECONCILE_APPLY_CMD hook required}" \
    || { log "reconcile: node apply/converge failed"; return 1; }

  rev2="$(_cp GET "/v1/nodes/${entry}/reality-users" | jq -r '.revision')" \
    || { log "reconcile: allow-list re-read failed"; return 1; }
  if [ "$rev2" != "$rev1" ]; then
    log "reconcile: allow-list changed during converge (${rev1:0:8} != ${rev2:0:8}) — retry, not activating"; return 1
  fi

  results="$("${RECONCILE_PROBE_CMD:?RECONCILE_PROBE_CMD hook required}")" \
    || { log "reconcile: probing failed"; return 1; }
  if ! transport_gate "$results"; then
    log "reconcile: fewer than 2 distinct verified client transports — leaving provisioning: ${results}"; return 1
  fi

  _cp POST "/v1/nodes/${entry}/activate" "$(jq -n --arg r "$rev1" '{expected_revision:$r}')" >/dev/null \
    || { log "reconcile: entry activation refused (revision race or structure)"; return 1; }
  log "reconcile: entry ${entry} ACTIVE"
}

reconcile_main() {
  require_cmd jq curl
  require_env ENTRY EXIT CONTROL_PLANE_URL CONTROL_PLANE_TOKEN
  local lock="/tmp/caspervpn-reconcile-${ENTRY}"
  acquire_lock "$lock" || die "another reconcile for ${ENTRY} is in progress"
  reconcile_pair "$ENTRY" "$EXIT"
}

# Run only when executed, not when sourced (tests source with RECONCILE_LIB_ONLY).
if [ "${RECONCILE_LIB_ONLY:-false}" != "true" ]; then
  reconcile_main
fi
