#!/usr/bin/env bash
# node_up.sh — bring up an entry+exit pair: terraform apply -> ansible node-up ->
# register both Nodes in the control-plane. Idempotent (terraform + ansible
# converge; register tolerates 409). Target < 5 min from nothing.
#
# Usage:  REGION=<entry-region> CLOUD=<entry-cloud> infra/scripts/node_up.sh
# Env:    HCLOUD_TOKEN / VULTR_API_KEY (per env providers), SSH_PUBKEY,
#         CONTROL_PLANE_URL, CONTROL_PLANE_TOKEN
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
# REALITY mimicry is DATA, never hardcoded — the operator supplies it (see
# docs/mimicry-domains.md). REALITY_DEST is the handshake upstream host:port.
require_env REGION CLOUD SSH_PUBKEY REALITY_SERVER_NAMES REALITY_DEST
ROOT="$(repo_root)"
TF_DIR="${ROOT}/${TF_ENV_DIR_DEFAULT}"
ANSIBLE_DIR="${ROOT}/${ANSIBLE_DIR_DEFAULT}"

log "terraform apply (entry region=${REGION}, entry cloud=${CLOUD})"
terraform -chdir="${TF_DIR}" init -input=false >/dev/null
terraform -chdir="${TF_DIR}" apply -auto-approve -input=false \
  -var "ssh_pubkey=${SSH_PUBKEY}" \
  -var "entry_region=${REGION}"

ENTRY_IP="$(terraform -chdir="${TF_DIR}" output -raw entry_ip)"
EXIT_IP="$(terraform -chdir="${TF_DIR}" output -raw exit_ip)"
ENTRY_ID="$(terraform -chdir="${TF_DIR}" output -raw entry_id)"
EXIT_ID="$(terraform -chdir="${TF_DIR}" output -raw exit_id)"
[ -n "${ENTRY_IP}" ] || die "no entry_ip from terraform"

log "building inventory (entry=${ENTRY_IP}, exit=${EXIT_IP})"
INV="$(mktemp)"
trap 'rm -f "${INV}"' EXIT
# node_role is REQUIRED per host: without it the exit inherits the entry default
# and both provision as entries. server_names[0] handshake if none given.
cat >"${INV}" <<EOF
[entry]
${ENTRY_ID} ansible_host=${ENTRY_IP} node_id=${ENTRY_ID} node_role=entry health_region=${REGION}

[exit]
${EXIT_ID} ansible_host=${EXIT_IP} node_id=${EXIT_ID} node_role=exit allowed_entry_ip=${ENTRY_IP}

[nodes:children]
entry
exit
EOF

# Mimicry passed as extra-vars (data, not code). NOTE: entry->exit SS chaining is
# NOT wired here — it needs a matching Shadowsocks inbound (enabled, password,
# port 8388) on the exit and credentials shared with the entry's outbound. That
# is a separate, unproven task (see docs/FIRST-WORKING-USER.md); half-wiring a
# passwordless exit_endpoint on port 443 would only produce a broken chain. For
# now the entry terminates traffic directly (entry==exit egress).
REALITY_HANDSHAKE="${REALITY_HANDSHAKE:-${REALITY_DEST%%:*}}"
# hy2_sni enables + configures hysteria2 in the role (entry only; the exit gate is
# in node-up.yml). Password is empty on the first node-up so keygen generates it;
# a rotation supplies the durable one. Trusted TLS cert/key come from the operator
# secret manager (HY2_TLS_CERT/HY2_TLS_KEY) — a private key is never in the CP.
EXTRA_VARS="$(jq -n \
  --argjson names "$(printf '%s' "$REALITY_SERVER_NAMES" | jq -R 'split(",")|map(select(length>0))')" \
  --arg handshake "$REALITY_HANDSHAKE" \
  '{reality_server_names: $names, reality_handshake_server: $handshake}')"
if [ -n "${HY2_SNI:-}" ]; then
  EXTRA_VARS="$(jq -s '.[0] * .[1]' \
    <(printf '%s' "$EXTRA_VARS") \
    <(hy2_extra_vars "$HY2_SNI" "" "${HY2_TLS_CERT:-}" "${HY2_TLS_KEY:-}"))"
fi

# Pass extra-vars through a 0600 file (-e @file), never on argv: hy2_tls_key/
# hy2_password would otherwise be visible in the process list.
VARS_FILE="$(write_secure_vars_file "$EXTRA_VARS")"
trap 'rm -f "${INV}" "${VARS_FILE}"' EXIT

log "ansible node-up (wait for cloud-init, then converge)"
retry 10 ansible -i "${INV}" nodes -m ping >/dev/null
ansible-playbook -i "${INV}" "${ANSIBLE_DIR}/playbooks/node-up.yml" -e "@${VARS_FILE}"

if [ -n "${CONTROL_PLANE_URL:-}" ]; then
  log "registering nodes in control-plane"
  # Clients connect ONLY to the entry, so only the entry carries a client-facing
  # REALITY transport (real public key from its artifact). The exit is registered
  # as inventory metadata with NO client transport — otherwise subscription, which
  # renders every active node's transports, would emit an outbound with an empty
  # server (the exit has no client entry_ip).
  ENTRY_BLOB="$(ansible -i "${INV}" "${ENTRY_ID}" -b -m slurp -a src="${REALITY_PUB_ARTIFACT:-/etc/caspervpn/reality.pub}" \
                 | awk '/"content":/ {gsub(/[",]/,""); print $2}' | base64 -d)"
  ENTRY_PUB="$(printf '%s' "$ENTRY_BLOB" | awk -F= '/^public_key/{print $2}')"
  ENTRY_SIDS="$(printf '%s' "$ENTRY_BLOB" | awk -F= '/^short_ids/{print $2}')"
  [ -n "$ENTRY_PUB" ] || die "no public_key artifact from entry ${ENTRY_ID}"

  # Optional 2nd client transport. When the operator enabled hysteria2 (HY2_SNI
  # set), slurp its generated password so the node registers with vless-reality +
  # hysteria2 (the >=2 distinct types the activation gate requires). Production
  # runs a trusted cert => insecure=false; the self-signed/insecure path is e2e
  # only (real-node.sh), never node-up.
  HY2_PASSWORD=""; HY2_INSECURE="false"
  if [ -n "${HY2_SNI:-}" ]; then
    HY2_PASSWORD="$(ansible -i "${INV}" "${ENTRY_ID}" -b -m slurp -a src="${HY2_CRED_STATE_FILE:-/etc/caspervpn/hysteria2.cred}" \
                     | awk '/"content":/ {gsub(/[",]/,""); print $2}' | base64 -d | awk -F= '/^password/{print $2}')"
    [ -n "$HY2_PASSWORD" ] || die "HY2_SNI set but no hysteria2 credential on entry ${ENTRY_ID}"
  fi

  # Register as PROVISIONING, not active. Subscription serves only status='active'
  # nodes (control-plane ListActive: WHERE status='active'), so a fresh node stays
  # out of every client bundle until a reconciler has (1) pushed each subscriber's
  # UUID/short-id into the node's reality_users allow-list — the node converges
  # with reality_users:[] and would authenticate nobody — and (2) ensured at least
  # two client transports are up (anti-block: never a monoculture). Flipping to
  # active is that reconciler's job (see docs/FIRST-WORKING-USER.md), NOT node-up's.
  NODE_ID="${ENTRY_ID}" NODE_ROLE="entry" NODE_STATUS="provisioning" \
    PROVIDER="${CLOUD}" CLOUD="${CLOUD}" REGION="${REGION}" \
    ENTRY_IP="${ENTRY_IP}" EPHEMERAL_ENTRY_IP="true" \
    REALITY_PUBLIC_KEY="$ENTRY_PUB" REALITY_SHORT_IDS="$ENTRY_SIDS" \
    REALITY_SERVER_NAMES="$REALITY_SERVER_NAMES" REALITY_DEST="$REALITY_DEST" \
    HY2_PASSWORD="$HY2_PASSWORD" HY2_SNI="${HY2_SNI:-}" HY2_INSECURE="$HY2_INSECURE" \
    reality_sync_node

  # Exit: plain inventory node, no client transport (see comment above), also
  # provisioning until the reconciler blesses the pair.
  NODE_ID="${EXIT_ID}" NODE_ROLE="exit" NODE_STATUS="provisioning" \
    PROVIDER="${CLOUD}" CLOUD="${CLOUD}" REGION="${REGION}" ENTRY_NODE_ID="${ENTRY_ID}" \
    cp_register_node "$(NODE_ID="${EXIT_ID}" NODE_ROLE="exit" NODE_STATUS="provisioning" \
      PROVIDER="${CLOUD}" CLOUD="${CLOUD}" REGION="${REGION}" ENTRY_NODE_ID="${ENTRY_ID}" \
      build_node_json)"
else
  log "CONTROL_PLANE_URL unset — skipping registration"
fi

log "node up: entry=${ENTRY_IP} (id=${ENTRY_ID}) exit=${EXIT_IP} (id=${EXIT_ID})"
