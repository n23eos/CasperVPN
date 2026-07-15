#!/usr/bin/env bash
# reality_sync.sh — single point that syncs a node's REALITY material into the
# control-plane. Source, don't execute. Shared by node_up.sh (first registration),
# node_rotate.sh (re-key), and the local real-node e2e.
#
# UPSERT, not a bare PATCH: on the very first node-up the Node object does not
# exist yet, so a PATCH would 404. reality_sync_node() POSTs the full Node; on
# 409 it GETs the current Node, merges the vless-reality transport in place
# (bump public_key, UNION short_ids — never replace the pool, that collapses
# per-user isolation), preserves every other transport, and PATCHes it back.
#
# Depends on lib.sh (log/die/require_env) and control_plane.sh (cp_get_node,
# cp_patch_node, _cp_auth) being sourced by the caller.
#
# Inputs (env):
#   Node identity: NODE_ID NODE_ROLE NODE_STATUS PROVIDER CLOUD REGION
#     optional:    ENTRY_IP EPHEMERAL_ENTRY_IP(true/false) ENTRY_NODE_ID
#   REALITY:       REALITY_PUBLIC_KEY REALITY_SERVER_NAMES(csv) REALITY_DEST
#     optional:    REALITY_SHORT_IDS(csv) REALITY_FLOW REALITY_TAG REALITY_PORT

# _reality_transport_json — build one contracts.Transport (vless-reality variant).
_reality_transport_json() {
  require_env REALITY_PUBLIC_KEY REALITY_SERVER_NAMES REALITY_DEST
  local flow="${REALITY_FLOW:-xtls-rprx-vision}" tag="${REALITY_TAG:-vless-reality-in}"
  local port="${REALITY_PORT:-443}" fp="${REALITY_FINGERPRINT:-chrome}"
  # csv -> JSON array (via --arg so an empty short-id list yields [], not invalid
  # JSON — jq -R on empty stdin emits nothing, which breaks --argjson).
  local names_json sids_json
  names_json="$(jq -cn --arg s "$REALITY_SERVER_NAMES" '$s|split(",")|map(select(length>0))')"
  sids_json="$(jq -cn --arg s "${REALITY_SHORT_IDS:-}" '$s|split(",")|map(select(length>0))')"
  jq -n \
    --arg tag "$tag" --argjson port "$port" --arg pub "$REALITY_PUBLIC_KEY" \
    --arg dest "$REALITY_DEST" --arg flow "$flow" --arg fp "$fp" \
    --argjson names "$names_json" --argjson sids "$sids_json" \
    '{
       tag: $tag, type: "vless-reality", version: "v1", port: $port,
       enabled: true, priority: 0,
       vless_reality: {
         server_names: $names, dest: $dest, public_key: $pub,
         short_ids: $sids, flow: $flow, fingerprint: $fp
       }
     }'
}

# reality_sync_node — upsert the node with its REALITY transport. Idempotent.
# NOTE: this only syncs the REALITY public material and the caller-supplied
# NODE_STATUS. It does NOT gate activation: it neither pushes the per-user
# allow-list (UUID/short-id) onto the node nor enforces the ≥2-client-transport
# anti-block rule. A node must therefore be registered as `provisioning` (not
# `active`) here and promoted to `active` only by a reconciler that has done both
# (see node_up.sh / node_rotate.sh comments and docs/FIRST-WORKING-USER.md).
reality_sync_node() {
  require_env NODE_ID NODE_ROLE NODE_STATUS PROVIDER CLOUD REGION
  local transport code
  transport="$(_reality_transport_json)"

  # Attempt create with the full node (transports carrying vless-reality).
  local node_json
  node_json="$(TRANSPORTS_JSON="[$transport]" build_node_json)"
  code="$(curl -sS -o "/tmp/reality_sync.$$" -w '%{http_code}' \
    -X POST "${CONTROL_PLANE_URL:?}/v1/nodes" \
    -H "$(_cp_auth)" -H 'Content-Type: application/json' \
    -d "$node_json")" || die "control-plane unreachable"

  case "$code" in
    201|200)
      log "reality_sync: node ${NODE_ID} created with REALITY pub=${REALITY_PUBLIC_KEY:0:12}..."
      rm -f "/tmp/reality_sync.$$"
      return 0
      ;;
    409)
      rm -f "/tmp/reality_sync.$$"
      log "reality_sync: node ${NODE_ID} exists — merging REALITY transport"
      ;;
    *)
      cat "/tmp/reality_sync.$$" >&2; rm -f "/tmp/reality_sync.$$"
      die "reality_sync: create failed: HTTP $code"
      ;;
  esac

  # Merge path: GET current node, then update the MUTABLE node fields (a rotation
  # bumps entry_ip/ephemeral_entry_ip, not just the key) AND upsert the
  # vless-reality transport in place. Fields taken from the freshly built node
  # JSON so callers' new IP/status actually land; the short-id pool is unioned so
  # per-user isolation is never collapsed. Identity fields (id, role) are left as
  # the control-plane holds them.
  local current merged
  current="$(cp_get_node "$NODE_ID")"
  merged="$(printf '%s' "$current" | jq --argjson t "$transport" --argjson new "$node_json" '
    .status = $new.status
    | .entry_ip = $new.entry_ip
    | .ephemeral_entry_ip = $new.ephemeral_entry_ip
    | (if $new.entry_node_id then .entry_node_id = $new.entry_node_id else . end)
    | .transports = (
        (.transports // []) as $ts
        | if any($ts[]?; .type == "vless-reality")
          then ($ts | map(
                 if .type == "vless-reality"
                 then .vless_reality = ($t.vless_reality + {
                        short_ids: (((.vless_reality.short_ids // []) + $t.vless_reality.short_ids) | unique)
                      })
                 else . end))
          else ($ts + [$t])
          end)')"
  cp_patch_node "$NODE_ID" "$merged"
  log "reality_sync: node ${NODE_ID} merged (entry_ip=${ENTRY_IP:-<unset>}, pub=${REALITY_PUBLIC_KEY:0:12}...)"
}
