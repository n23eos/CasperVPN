#!/usr/bin/env bash
# Synchronize one entry node from an authoritative access-users snapshot.
# The snapshot travels through a 0600 file, never argv. Empty snapshots stop the
# data plane immediately; non-empty snapshots converge both VLESS and Hysteria2.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "${HERE}/lib.sh"
# shellcheck source=run_manifest.sh
source "${HERE}/run_manifest.sh"
# shellcheck source=hy2_lifecycle.sh
source "${HERE}/hy2_lifecycle.sh"

require_cmd ansible ansible-playbook jq
require_env RUN_ID NODE ACCESS_SNAPSHOT_FILE
[ -f "$ACCESS_SNAPSHOT_FILE" ] || die "access snapshot file not found"
require_manifest_node "$RUN_ID" "$NODE" entry

REVISION="$(jq -er '.revision | select(type == "string" and length > 0)' "$ACCESS_SNAPSHOT_FILE")" \
  || die "access snapshot revision missing"
VALID_UNTIL="$(jq -er '.valid_until | select(type == "string" and length > 0)' "$ACCESS_SNAPSHOT_FILE")" \
  || die "access snapshot valid_until missing"
USER_COUNT="$(jq -er '.users | length' "$ACCESS_SNAPSHOT_FILE")" \
  || die "access snapshot users missing"

ENTRY_RAW_ID="$(manifest_field "$RUN_ID" '.entry.raw_id')"
ENTRY_IP="$(manifest_field "$RUN_ID" '.entry.ip')"
EXIT_IP="$(manifest_field "$RUN_ID" '.exit.ip')"
[ -n "$ENTRY_RAW_ID" ] && [ -n "$ENTRY_IP" ] || die "manifest entry identity incomplete"

INV="$(mktemp)"
LEASE="$(mktemp)"
VARS_FILE=""
cleanup_access_sync() { rm -f "$INV" "$LEASE" ${VARS_FILE:+"$VARS_FILE"}; }
trap cleanup_access_sync EXIT
printf '[entry]\n%s ansible_host=%s node_id=%s node_role=entry\n' "$ENTRY_RAW_ID" "$ENTRY_IP" "$NODE" >"$INV"
( umask 077; jq '{revision,valid_until}' "$ACCESS_SNAPSHOT_FILE" >"$LEASE" )

if [ "$USER_COUNT" -eq 0 ]; then
  log "access sync: empty snapshot for ${NODE}; stopping sing-box fail-closed"
  ansible -i "$INV" entry -b -m file -a 'path=/etc/caspervpn state=directory owner=root group=root mode=0700' >/dev/null
  ansible -i "$INV" entry -b -m copy -a "src=${LEASE} dest=/etc/caspervpn/access-lease.json owner=root group=root mode=0600" >/dev/null
  ansible -i "$INV" entry -b -m systemd -a 'name=sing-box state=stopped' >/dev/null
  exit 0
fi

# Every admitted user must have all credentials. A missing personal Hysteria2
# password must never fall back to the historical node-level shared password.
jq -e '
  all(.users[];
    (.uuid | type == "string" and length > 0) and
    (.short_id | type == "string" and length > 0) and
    (.hysteria2_password | type == "string" and length > 0))
' "$ACCESS_SNAPSHOT_FILE" >/dev/null || die "access snapshot has incomplete personal credentials"
require_env PAIR_PSK HY2_SNI

ACCESS_VARS="$(jq -n \
  --slurpfile snap "$ACCESS_SNAPSHOT_FILE" \
  --arg psk "$PAIR_PSK" --arg exip "$EXIT_IP" \
  --argjson port "${EXIT_LINK_PORT:-8388}" --arg sni "$HY2_SNI" \
  '{
    reality_users: [$snap[0].users[] | {uuid, short_id}],
    hysteria2_users: [$snap[0].users[] | {password:.hysteria2_password}],
    access_snapshot_revision: $snap[0].revision,
    access_snapshot_valid_until: $snap[0].valid_until,
    reality_reuse_existing_key: true,
    hy2_sni: $sni,
    exit_endpoint:{server:$exip, server_port:$port, psk:$psk}
  }')"
VARS_FILE="$(write_secure_vars_file "$ACCESS_VARS")"
ansible-playbook -i "$INV" "$(repo_root)/${ANSIBLE_DIR_DEFAULT}/playbooks/node-up.yml" \
  -e target=entry -e "@${VARS_FILE}"
log "access sync: node=${NODE} users=${USER_COUNT} revision=${REVISION:0:12} valid_until=${VALID_UNTIL}"
