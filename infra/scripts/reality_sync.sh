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
#   Hysteria2 (2nd client transport, optional — set HY2_PASSWORD to include it):
#                  HY2_PASSWORD HY2_SNI
#     optional:    HY2_INSECURE(true/false, e2e only) HY2_PORT HY2_TAG HY2_OBFS HY2_OBFS_PASSWORD

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

# _hy2_transport_json — build one contracts.Transport (hysteria2 variant), or
# nothing when HY2_PASSWORD is unset. The password is a client secret that flows
# to the client via /sub, so it is carried here by design. insecure is true only
# for the local e2e (self-signed); production sends a trusted cert => insecure false.
_hy2_transport_json() {
  [ -n "${HY2_PASSWORD:-}" ] || return 0
  require_env HY2_SNI
  local tag="${HY2_TAG:-hysteria2-in}" port="${HY2_PORT:-8443}"
  local insecure="${HY2_INSECURE:-false}"
  jq -n \
    --arg tag "$tag" --argjson port "$port" --arg pw "$HY2_PASSWORD" --arg sni "$HY2_SNI" \
    --argjson insecure "$insecure" --arg obfs "${HY2_OBFS:-}" --arg obfspw "${HY2_OBFS_PASSWORD:-}" \
    '{
       tag: $tag, type: "hysteria2", version: "v1", port: $port,
       enabled: true, priority: 1,
       hysteria2: ({ password: $pw, sni: $sni, insecure: $insecure }
                   + (if $obfs != "" then { obfs: $obfs, obfs_password: $obfspw } else {} end))
     }'
}

# _transports_json — the full enabled client transport array (vless-reality plus
# hysteria2 when configured). This is what gives a node the >=2 distinct types the
# activation gate requires.
_transports_json() {
  local vless hy2
  vless="$(_reality_transport_json)"
  hy2="$(_hy2_transport_json)"
  if [ -n "$hy2" ]; then
    jq -n --argjson v "$vless" --argjson h "$hy2" '[$v, $h]'
  else
    jq -n --argjson v "$vless" '[$v]'
  fi
}

# reality_sync_node — upsert the node with its client transports. Idempotent.
# NOTE: this only syncs the REALITY public material and the caller-supplied
# NODE_STATUS. It does NOT gate activation: it neither pushes the per-user
# allow-list (UUID/short-id) onto the node nor enforces the ≥2-client-transport
# anti-block rule. A node must therefore be registered as `provisioning` (not
# `active`) here and promoted to `active` only by a reconciler that has done both
# (see node_up.sh / node_rotate.sh comments and docs/FIRST-WORKING-USER.md).
reality_sync_node() {
  require_env NODE_ID NODE_ROLE NODE_STATUS PROVIDER CLOUD REGION
  local transports code
  transports="$(_transports_json)"

  # Attempt create with the full node (all enabled client transports).
  local node_json
  node_json="$(TRANSPORTS_JSON="$transports" build_node_json)"
  code="$(curl -sS -o "/tmp/reality_sync.$$" -w '%{http_code}' \
    -X POST "${CONTROL_PLANE_URL:?}/v1/nodes" \
    -H "$(_cp_auth)" -H 'Content-Type: application/json' \
    -d "$node_json")" || die "control-plane unreachable"

  case "$code" in
    201|200)
      log "reality_sync: node ${NODE_ID} created (pub=${REALITY_PUBLIC_KEY:0:12}..., hy2=$([ -n "${HY2_PASSWORD:-}" ] && echo yes || echo no))"
      rm -f "/tmp/reality_sync.$$"
      return 0
      ;;
    409)
      rm -f "/tmp/reality_sync.$$"
      log "reality_sync: node ${NODE_ID} exists — merging transports"
      ;;
    *)
      cat "/tmp/reality_sync.$$" >&2; rm -f "/tmp/reality_sync.$$"
      die "reality_sync: create failed: HTTP $code"
      ;;
  esac

  # Merge path: GET current node, update the MUTABLE node fields (a rotation bumps
  # entry_ip/ephemeral_entry_ip), then upsert EACH desired transport by type —
  # vless-reality in place (bump key, UNION short_ids so per-user isolation is
  # never collapsed), hysteria2 replaced wholesale, and any absent one appended.
  # Other existing transports are preserved.
  local current merged
  current="$(cp_get_node "$NODE_ID")"
  merged="$(printf '%s' "$current" | jq --argjson desired "$transports" --argjson new "$node_json" '
    .status = $new.status
    | .entry_ip = $new.entry_ip
    | .ephemeral_entry_ip = $new.ephemeral_entry_ip
    | (if $new.entry_node_id then .entry_node_id = $new.entry_node_id else . end)
    | reduce $desired[] as $d (.;
        .transports = (
          (.transports // []) as $ts
          | if any($ts[]?; .type == $d.type)
            then ($ts | map(
                   if .type == $d.type and $d.type == "vless-reality"
                   then $d + {vless_reality: ($d.vless_reality + {
                          short_ids: (((.vless_reality.short_ids // []) + $d.vless_reality.short_ids) | unique)})}
                   elif .type == $d.type
                   then $d
                   else . end))
            else ($ts + [$d])
            end))')"
  cp_patch_node "$NODE_ID" "$merged"
  log "reality_sync: node ${NODE_ID} merged (entry_ip=${ENTRY_IP:-<unset>}, transports=$(printf '%s' "$transports" | jq length))"
}
