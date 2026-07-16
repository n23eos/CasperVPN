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
# shellcheck source=hy2_lifecycle.sh
source "${HERE}/hy2_lifecycle.sh"

require_cmd terraform ansible-playbook ansible jq curl
require_env NODE SSH_PUBKEY REALITY_SERVER_NAMES REALITY_DEST
ROOT="$(repo_root)"
TF_DIR="${ROOT}/${TF_ENV_DIR_DEFAULT}"
ANSIBLE_DIR="${ROOT}/${ANSIBLE_DIR_DEFAULT}"

# ===========================================================================
# PREFLIGHT — read all state and validate EVERY secret BEFORE touching infra.
# terraform -replace destroys the working entry, so a missing secret must fail
# HERE, while the node is still up. Nothing below mutates infrastructure.
# ===========================================================================
OLD_ENTRY_IP="$(terraform -chdir="${TF_DIR}" output -raw entry_ip)" \
  || die "cannot read current entry_ip from terraform (state error?) — refusing to rotate"
[ -n "${OLD_ENTRY_IP}" ] || die "current entry_ip is empty — nothing to rotate"
# NODE is a required env input (require_env NODE above), not a typo of a local 'node'.
# shellcheck disable=SC2153
log "rotating ${NODE}; old entry IP: ${OLD_ENTRY_IP} — preflighting secrets"

# hysteria2: durable password from the control-plane; TLS from the secret manager.
# hy2_desired_from_cp HARD-FAILS if the CP is unreachable or the state is partial.
HY2_PASSWORD=""; HY2_SNI_CUR=""
if [ -n "${CONTROL_PLANE_URL:-}" ]; then
  HY2_DESIRED="$(hy2_desired_from_cp "${NODE}")"
  if [ -n "$HY2_DESIRED" ]; then
    HY2_PASSWORD="${HY2_DESIRED%%$'\t'*}"
    HY2_SNI_CUR="${HY2_DESIRED##*$'\t'}"
    [ -n "${HY2_TLS_CERT:-}" ] && [ -n "${HY2_TLS_KEY:-}" ] \
      || die "hysteria2 is configured on ${NODE} but HY2_TLS_CERT/HY2_TLS_KEY (secret manager) are not set — a replacement VM needs the trusted cert+key or it fails the fail-closed TLS assert"
  fi
fi

# entry->exit chain topology is EXPLICIT, never inferred from a command failure:
# a terraform read error / empty exit_ip hard-fails (resolve_exit_topology), so a
# fresh entry is never brought up chain-less by accident. NO_EXIT_LINK=true is a
# deliberate test/recovery opt-out only (a chainless entry still won't activate).
# The internal PAIR PSK then comes from the secret manager (never CP, never VM).
set +e
EXIT_LINK_IP="$(terraform -chdir="${TF_DIR}" output -raw exit_ip 2>/dev/null)"; EXIT_IP_RC=$?
set -e
EXIT_CONFIGURED="$(resolve_exit_topology "$EXIT_IP_RC" "$EXIT_LINK_IP")"
require_pair_psk_for_rotation "$EXIT_CONFIGURED"

# Build the desired extra-vars and stage the 0600 secret file — still no mutation.
REALITY_HANDSHAKE="${REALITY_HANDSHAKE:-${REALITY_DEST%%:*}}"
EXTRA_VARS="$(jq -n \
  --argjson names "$(printf '%s' "$REALITY_SERVER_NAMES" | jq -R 'split(",")|map(select(length>0))')" \
  --arg handshake "$REALITY_HANDSHAKE" \
  '{reality_server_names: $names, reality_handshake_server: $handshake}')"
if [ -n "$HY2_SNI_CUR" ]; then
  EXTRA_VARS="$(jq -s '.[0] * .[1]' \
    <(printf '%s' "$EXTRA_VARS") \
    <(hy2_extra_vars "$HY2_SNI_CUR" "$HY2_PASSWORD" "${HY2_TLS_CERT:-}" "${HY2_TLS_KEY:-}"))"
fi
if [ "$EXIT_CONFIGURED" = true ]; then
  EXTRA_VARS="$(jq -s '.[0] * .[1]' \
    <(printf '%s' "$EXTRA_VARS") \
    <(exit_link_extra_vars "$EXIT_LINK_IP" "${EXIT_LINK_PORT:-8388}" "$PAIR_PSK"))"
fi
INV="$(mktemp)"
VARS_FILE="$(write_secure_vars_file "$EXTRA_VARS")"
trap 'rm -f "${INV}" "${VARS_FILE}"' EXIT
log "preflight ok — all secrets/state validated; entry still up"

# ===========================================================================
# MUTATE — only now replace the entry VM. Everything it needs is already staged.
# ===========================================================================
log "terraform -replace entry VM (new ephemeral IP)"
terraform -chdir="${TF_DIR}" apply -auto-approve -input=false \
  -var "ssh_pubkey=${SSH_PUBKEY}" \
  -replace="module.entry_vm.hcloud_server.this"

NEW_ENTRY_IP="$(terraform -chdir="${TF_DIR}" output -raw entry_ip)"
[ -n "${NEW_ENTRY_IP}" ] || die "no new entry_ip after replace"
[ "${NEW_ENTRY_IP}" != "${OLD_ENTRY_IP}" ] || die "entry IP did not change — rotation failed"
log "new entry IP: ${NEW_ENTRY_IP}"

printf '[entry]\n%s ansible_host=%s node_id=%s\n' "${NODE}" "${NEW_ENTRY_IP}" "${NODE}" >"${INV}"
retry 10 ansible -i "${INV}" entry -m ping >/dev/null
ansible-playbook -i "${INV}" "${ANSIBLE_DIR}/playbooks/node-up.yml"     -e target=entry -e "@${VARS_FILE}"
ansible-playbook -i "${INV}" "${ANSIBLE_DIR}/playbooks/node-rotate.yml" -e target=entry -e "@${VARS_FILE}"

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
    HY2_PASSWORD="${HY2_PASSWORD}" HY2_SNI="${HY2_SNI_CUR}" HY2_INSECURE=false \
    reality_sync_node
  log "control-plane updated for ${NODE}: entry_ip=${NEW_ENTRY_IP}, REALITY pub=${NEW_PUB:0:12}... (status=provisioning until user allow-list re-synced)"
else
  log "CONTROL_PLANE_URL unset — skipping control-plane update"
fi

log "rotation done: ${NODE} moved ${OLD_ENTRY_IP:-none} -> ${NEW_ENTRY_IP}"
