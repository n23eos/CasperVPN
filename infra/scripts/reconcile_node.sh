#!/usr/bin/env bash
# reconcile_node.sh — declarative reconciler that promotes a provisioning
# entry+exit PAIR to active, fail-closed. It is the ONLY thing that flips a node
# to active: it syncs the user allow-list onto the node, verifies >=2 client
# transports protocol-aware, and calls the guarded /activate — exit first, entry
# last. ANY error leaves the node(s) provisioning.
#
# Pair state machine (each step aborts -> pair stays provisioning):
#   0. serialize per pair (flock) — a stale run must not race a newer one.
#   1. demote the entry to provisioning before any mutation.
#   2. prepare + verify the EXIT (its SS2022 egress works) -> activate EXIT
#      (POST /activate {evidence: exit_data_plane_verified}).
#   3. fetch R1 (allow-list) -> push each user's uuid/short-id onto the entry ->
#      converge + restart the node config.
#   4. re-fetch R2; if R2 != R1 (a ban/rotation raced the converge) -> DO NOT
#      activate; redo the full sync.
#   5. probe every client transport; require >=2 distinct verified (transport_gate).
#   6. activate the ENTRY (POST /activate {expected_revision: R1}); the CP re-checks
#      the digest under a lock, so a 409 just means "retry".
#
# Usage:  ENTRY=<entry-id> EXIT=<exit-id> reconcile_node.sh
# Env:    CONTROL_PLANE_URL CONTROL_PLANE_TOKEN; probing needs PROBE_IMG/PROBE_NET
#         and the node-config apply hook (RECONCILE_APPLY_CMD) supplied by the
#         orchestrator/e2e. This script owns the DECISIONS; the caller supplies the
#         node-mutation + probe-config builders as hooks so the logic is testable.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "${HERE}/lib.sh"
# shellcheck source=control_plane.sh
source "${HERE}/control_plane.sh"
# shellcheck source=probe.sh
source "${HERE}/probe.sh"

require_cmd jq curl flock
require_env ENTRY EXIT CONTROL_PLANE_URL CONTROL_PLANE_TOKEN

_cp() { # _cp METHOD PATH [body] -> echoes body, dies on non-2xx
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
  case "$code" in 2??) return 0 ;; *) return "$code" ;; esac
}

# reconcile_pair — the state machine. Returns non-zero (pair stays provisioning)
# on any failed step. Idempotent: a re-run of an already-active pair is a no-op
# via the CP's not-provisioning 409s.
reconcile_pair() {
  # 1. demote entry (fail-closed: never leave a half-synced node serving).
  log "reconcile: demoting entry ${ENTRY} to provisioning before sync"
  _cp PATCH "/v1/nodes/${ENTRY}" "$(jq -n '{status:"provisioning"}')" >/dev/null 2>&1 || true

  # 2. EXIT first — the reconciler's authenticated entry->exit probe is the evidence.
  if ! "${RECONCILE_VERIFY_EXIT:?RECONCILE_VERIFY_EXIT hook required}"; then
    log "reconcile: exit ${EXIT} data-plane not verified — leaving provisioning"; return 1
  fi
  _cp POST "/v1/nodes/${EXIT}/activate" "$(jq -n '{expected_revision:"", evidence:"exit_data_plane_verified"}')" >/dev/null \
    || { log "reconcile: exit activation refused — leaving provisioning"; return 1; }
  log "reconcile: exit ${EXIT} active"

  # 3. fetch R1 + sync the allow-list onto the entry, converge.
  local r1 users
  r1="$(_cp GET "/v1/nodes/${ENTRY}/reality-users")" || { log "reconcile: allow-list read failed"; return 1; }
  users="$(jq -c '.users' <<<"$r1")"; local rev1; rev1="$(jq -r '.revision' <<<"$r1")"
  log "reconcile: syncing $(jq 'length' <<<"$users") users onto ${ENTRY} (rev ${rev1:0:12}...)"
  RECONCILE_USERS="$users" "${RECONCILE_APPLY_CMD:?RECONCILE_APPLY_CMD hook required}" \
    || { log "reconcile: node apply/converge failed"; return 1; }

  # 4. re-fetch R2; a mid-converge ban/rotation means redo, do NOT activate.
  local rev2; rev2="$(_cp GET "/v1/nodes/${ENTRY}/reality-users" | jq -r '.revision')" \
    || { log "reconcile: allow-list re-read failed"; return 1; }
  if [ "$rev2" != "$rev1" ]; then
    log "reconcile: allow-list changed during converge (R1 ${rev1:0:8} != R2 ${rev2:0:8}) — retry, not activating"
    return 1
  fi

  # 5. protocol-aware probes: >=2 distinct verified client transports.
  local results; results="$("${RECONCILE_PROBE_CMD:?RECONCILE_PROBE_CMD hook required}")" \
    || { log "reconcile: probing failed"; return 1; }
  if ! transport_gate "$results"; then
    log "reconcile: fewer than 2 distinct verified client transports — leaving provisioning: ${results}"; return 1
  fi

  # 6. activate the ENTRY, guarded on the exact revision we synced (R1).
  _cp POST "/v1/nodes/${ENTRY}/activate" "$(jq -n --arg r "$rev1" '{expected_revision:$r}')" >/dev/null \
    || { log "reconcile: entry activation refused (revision race or structure) — leaving provisioning"; return 1; }
  log "reconcile: entry ${ENTRY} ACTIVE (exit-first, entry-last complete)"
}

# Serialize per pair so a stale run never activates after a newer failed one.
LOCK="/tmp/caspervpn-reconcile-${ENTRY}.lock"
exec 9>"$LOCK"
flock -n 9 || die "another reconcile for ${ENTRY} is in progress"
reconcile_pair
