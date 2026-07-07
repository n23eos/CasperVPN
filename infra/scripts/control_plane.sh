#!/usr/bin/env bash
# Control-plane API client (contracts/openapi/control-plane.yaml). Registers,
# patches and retires Node records. Source, don't execute. Node JSON is built
# from env/args to match the FROZEN contracts.Node schema — no field invented.
#
# Env:
#   CONTROL_PLANE_URL    e.g. http://control-plane:8081
#   CONTROL_PLANE_TOKEN  internal service bearer token
set -euo pipefail

_cp_auth() { printf 'Authorization: Bearer %s' "${CONTROL_PLANE_TOKEN:?CONTROL_PLANE_TOKEN required}"; }

# build_node_json — emit a contracts.Node object from env vars.
# Required: NODE_ID NODE_ROLE NODE_STATUS PROVIDER CLOUD REGION
# Optional: ENTRY_IP EPHEMERAL_ENTRY_IP(true/false) ENTRY_NODE_ID TRANSPORTS_JSON
build_node_json() {
  require_env NODE_ID NODE_ROLE NODE_STATUS PROVIDER CLOUD REGION
  jq -n \
    --arg id "$NODE_ID" \
    --arg role "$NODE_ROLE" \
    --arg status "$NODE_STATUS" \
    --arg provider "$PROVIDER" \
    --arg cloud "$CLOUD" \
    --arg region "$REGION" \
    --arg entry_ip "${ENTRY_IP:-}" \
    --argjson ephemeral "${EPHEMERAL_ENTRY_IP:-false}" \
    --arg entry_node_id "${ENTRY_NODE_ID:-}" \
    --argjson transports "${TRANSPORTS_JSON:-[]}" \
    '{
       id: $id,
       role: $role,
       status: $status,
       provider: $provider,
       cloud: $cloud,
       region: $region,
       entry_ip: $entry_ip,
       ephemeral_entry_ip: $ephemeral,
       transports: $transports
     }
     + (if $entry_node_id != "" then {entry_node_id: $entry_node_id} else {} end)'
}

# cp_register_node <node-json> — POST /v1/nodes. Tolerates 409 (already exists).
cp_register_node() {
  local body="$1" code
  code="$(curl -sS -o /tmp/cp_reg.$$ -w '%{http_code}' \
    -X POST "${CONTROL_PLANE_URL:?}/v1/nodes" \
    -H "$(_cp_auth)" -H 'Content-Type: application/json' \
    -d "$body")" || die "control-plane unreachable"
  case "$code" in
    201|200) log "node registered ($code)";;
    409)     log "node already registered (409) — ok";;
    *)       cat "/tmp/cp_reg.$$" >&2; die "register failed: HTTP $code";;
  esac
  rm -f "/tmp/cp_reg.$$"
}

# cp_patch_node <id> <partial-json> — PATCH /v1/nodes/{id}.
cp_patch_node() {
  local id="$1" body="$2"
  curl -fsS -X PATCH "${CONTROL_PLANE_URL:?}/v1/nodes/${id}" \
    -H "$(_cp_auth)" -H 'Content-Type: application/json' \
    -d "$body" >/dev/null || die "patch node ${id} failed"
  log "node ${id} patched"
}

# cp_retire_node <id> — DELETE /v1/nodes/{id} (204). Tolerates 404.
cp_retire_node() {
  local id="$1" code
  code="$(curl -sS -o /dev/null -w '%{http_code}' \
    -X DELETE "${CONTROL_PLANE_URL:?}/v1/nodes/${id}" -H "$(_cp_auth)")" \
    || die "control-plane unreachable"
  case "$code" in
    204|200) log "node ${id} retired ($code)";;
    404)     log "node ${id} already gone (404) — ok";;
    *)       die "retire failed: HTTP $code";;
  esac
}
