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

require_cmd terraform ansible-playbook ansible jq curl
require_env NODE SSH_PUBKEY
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

retry 10 ansible -i "${INV}" entry -m ping >/dev/null
ansible-playbook -i "${INV}" "${ANSIBLE_DIR}/playbooks/node-up.yml"     -e target=entry
ansible-playbook -i "${INV}" "${ANSIBLE_DIR}/playbooks/node-rotate.yml" -e target=entry

# 3. Read the rotation artifact (new REALITY public key + short-id).
ART="$(ansible -i "${INV}" entry -b -m slurp -a src=/etc/caspervpn/reality.pub \
        | awk '/"content":/ {gsub(/[",]/,""); print $2}')"
NEW_PUB="$(printf '%s' "${ART}" | base64 -d | awk -F= '/^public_key/{print $2}')"
NEW_SID="$(printf '%s' "${ART}" | base64 -d | awk -F= '/^short_id/{print $2}')"
log "rekeyed REALITY: pub=${NEW_PUB:0:12}... short_id=${NEW_SID}"

# 4. Update the Node in the control-plane: new advertised entry IP AND propagate
#    the rotated REALITY material. The frozen contracts.Node has NO node-level
#    field for the REALITY pubkey/short-id — it lives inside
#    transports[].vless_reality (public_key + the short_ids pool). So read the
#    current Node, patch its vless-reality transport in place (bump public_key,
#    ADD the new short-id to the pool — never replace it, that would collapse
#    per-user isolation), and PATCH the whole object back (keeps the Node schema's
#    additionalProperties:false + tagged-union validation happy).
if [ -n "${CONTROL_PLANE_URL:-}" ]; then
  CUR_NODE="$(cp_get_node "${NODE}")"
  PATCHED_NODE="$(printf '%s' "${CUR_NODE}" | jq \
    --arg ip "${NEW_ENTRY_IP}" --arg pub "${NEW_PUB}" --arg sid "${NEW_SID}" '
      .entry_ip = $ip
      | .ephemeral_entry_ip = true
      | .status = "active"
      | .transports = ((.transports // []) | map(
          if .type == "vless-reality" and (.vless_reality // empty) then
            .vless_reality.public_key = $pub
            | .vless_reality.short_ids = ((.vless_reality.short_ids // []) + [$sid] | unique)
          else . end))')"
  # If no vless-reality transport is registered yet, the contract has nowhere to
  # carry the pubkey — fail loud rather than silently dropping the rotation.
  if ! printf '%s' "${PATCHED_NODE}" | jq -e '[.transports[]? | select(.type=="vless-reality")] | length > 0' >/dev/null; then
    log "WARN: ${NODE} has no vless-reality transport in control-plane — new REALITY pubkey ${NEW_PUB:0:12}... NOT propagated (register transports first)"
  fi
  cp_patch_node "${NODE}" "${PATCHED_NODE}"
  log "control-plane updated for ${NODE}: entry_ip=${NEW_ENTRY_IP}, REALITY pub=${NEW_PUB:0:12}... short_id=${NEW_SID}"
else
  log "CONTROL_PLANE_URL unset — skipping control-plane update"
fi

log "rotation done: ${NODE} moved ${OLD_ENTRY_IP:-none} -> ${NEW_ENTRY_IP}"
