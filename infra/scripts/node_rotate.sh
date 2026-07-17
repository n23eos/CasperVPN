#!/usr/bin/env bash
# node_rotate.sh — regeneration cheaper than detection. Stands up a FRESH
# ephemeral entry IP (terraform -replace), re-keys REALITY on it, drains the old
# IP (replaced away), and PATCHes the Node in the control-plane. One command.
#
# Usage:  NODE=<entry-node-id> infra/scripts/node_rotate.sh
# Env:    SSH_PUBKEY, provider tokens, CONTROL_PLANE_URL, CONTROL_PLANE_TOKEN
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "${HERE}/lib.sh"
# shellcheck source=control_plane.sh
source "${HERE}/control_plane.sh"
# shellcheck source=reality_sync.sh
source "${HERE}/reality_sync.sh"

require_cmd terraform ansible-playbook ansible jq curl
require_env NODE SSH_PUBKEY REALITY_SERVER_NAMES REALITY_DEST
ROOT="$(repo_root)"
TF_DIR="${ROOT}/${TF_ENV_DIR_DEFAULT}"
ANSIBLE_DIR="${ROOT}/${ANSIBLE_DIR_DEFAULT}"

OLD_ENTRY_IP="$(terraform -chdir="${TF_DIR}" output -raw entry_ip 2>/dev/null || echo '')"
log "rotating ${NODE}; old entry IP: ${OLD_ENTRY_IP:-none}"

# 1. Fresh ephemeral entry — replace the entry VM to get a brand-new IP. The old
#    instance (and its burned IP) is destroyed by the replace.
log "terraform -replace entry VM (new ephemeral IP)"
terraform -chdir="${TF_DIR}" apply -auto-approve -input=false \
  -var "ssh_pubkey=${SSH_PUBKEY}" \
  -replace="module.entry_vm.hcloud_server.this"

NEW_ENTRY_IP="$(terraform -chdir="${TF_DIR}" output -raw entry_ip)"
[ -n "${NEW_ENTRY_IP}" ] || die "no new entry_ip after replace"
[ "${NEW_ENTRY_IP}" != "${OLD_ENTRY_IP}" ] || die "entry IP did not change — rotation failed"
log "new entry IP: ${NEW_ENTRY_IP}"

# 2. Converge the fresh entry, then re-key REALITY on it.
INV="$(mktemp)"; trap 'rm -f "${INV}"' EXIT
printf '[entry]\n%s ansible_host=%s node_id=%s\n' "${NODE}" "${NEW_ENTRY_IP}" "${NODE}" >"${INV}"

# Mimicry must reach the transports role, or the fresh entry hits the empty
# server_names/handshake_server asserts and fails before it can be re-keyed.
REALITY_HANDSHAKE="${REALITY_HANDSHAKE:-${REALITY_DEST%%:*}}"
EXTRA_VARS="$(jq -n \
  --argjson names "$(printf '%s' "$REALITY_SERVER_NAMES" | jq -R 'split(",")|map(select(length>0))')" \
  --arg handshake "$REALITY_HANDSHAKE" \
  '{reality_server_names: $names, reality_handshake_server: $handshake}')"

retry 10 ansible -i "${INV}" entry -m ping >/dev/null
ansible-playbook -i "${INV}" "${ANSIBLE_DIR}/playbooks/node-up.yml"     -e target=entry -e "${EXTRA_VARS}"
ansible-playbook -i "${INV}" "${ANSIBLE_DIR}/playbooks/node-rotate.yml" -e target=entry -e "${EXTRA_VARS}"

# 3. Read the rotation artifact (new REALITY public key + short-id pool).
ART="$(ansible -i "${INV}" entry -b -m slurp -a src=/etc/caspervpn/reality.pub \
        | awk '/"content":/ {gsub(/[",]/,""); print $2}' | base64 -d)"
NEW_PUB="$(printf '%s' "${ART}" | awk -F= '/^public_key/{print $2}')"
NEW_SIDS="$(printf '%s' "${ART}" | awk -F= '/^short_ids/{print $2}')"
[ -n "${NEW_PUB}" ] || die "no public_key artifact from ${NODE}"
log "rekeyed REALITY: pub=${NEW_PUB:0:12}... short_ids=${NEW_SIDS}"

# 4. Update the Node in the control-plane via the shared upsert helper: new entry
#    IP + rotated REALITY material. reality_sync_node GETs the current Node and
#    merges the vless-reality transport in place (bump public_key, UNION the
#    short-id pool — never collapse per-user isolation), keeping every other
#    transport and the frozen-schema validation intact.
if [ -n "${CONTROL_PLANE_URL:-}" ]; then
  # Demote to PROVISIONING while rotating: the re-converged node comes up with
  # reality_users:[] (no subscriber is authenticated yet), so it must not keep
  # serving as active. The reconciler re-pushes the user allow-list and flips it
  # back to active once ready (see docs/FIRST-WORKING-USER.md). Leaving it active
  # here would advertise a node that authenticates nobody.
  NODE_ID="${NODE}" NODE_ROLE="entry" NODE_STATUS="provisioning" \
    PROVIDER="${PROVIDER:-hetzner}" CLOUD="${CLOUD:-hetzner}" REGION="${REGION:-unknown}" \
    ENTRY_IP="${NEW_ENTRY_IP}" EPHEMERAL_ENTRY_IP="true" \
    REALITY_PUBLIC_KEY="${NEW_PUB}" REALITY_SHORT_IDS="${NEW_SIDS}" \
    REALITY_SERVER_NAMES="${REALITY_SERVER_NAMES}" REALITY_DEST="${REALITY_DEST}" \
    reality_sync_node
  log "control-plane updated for ${NODE}: entry_ip=${NEW_ENTRY_IP}, REALITY pub=${NEW_PUB:0:12}... (status=provisioning until user allow-list re-synced)"
else
  log "CONTROL_PLANE_URL unset — skipping control-plane update"
fi

log "rotation done: ${NODE} moved ${OLD_ENTRY_IP:-none} -> ${NEW_ENTRY_IP}"
