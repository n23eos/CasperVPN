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

require_cmd terraform ansible-playbook jq curl
require_env REGION CLOUD SSH_PUBKEY
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
cat >"${INV}" <<EOF
[entry]
${ENTRY_ID} ansible_host=${ENTRY_IP} node_id=${ENTRY_ID} health_region=${REGION}

[exit]
${EXIT_ID} ansible_host=${EXIT_IP} node_id=${EXIT_ID} allowed_entry_ip=${ENTRY_IP}

[nodes:children]
entry
exit
EOF

log "ansible node-up (wait for cloud-init, then converge)"
retry 10 ansible -i "${INV}" nodes -m ping >/dev/null
ansible-playbook -i "${INV}" "${ANSIBLE_DIR}/playbooks/node-up.yml"

if [ -n "${CONTROL_PLANE_URL:-}" ]; then
  log "registering nodes in control-plane"
  NODE_ID="${ENTRY_ID}" NODE_ROLE="entry" NODE_STATUS="active" \
    PROVIDER="${CLOUD}" CLOUD="${CLOUD}" REGION="${REGION}" \
    ENTRY_IP="${ENTRY_IP}" EPHEMERAL_ENTRY_IP="true" \
    cp_register_node "$(NODE_ID="${ENTRY_ID}" NODE_ROLE="entry" NODE_STATUS="active" \
      PROVIDER="${CLOUD}" CLOUD="${CLOUD}" REGION="${REGION}" \
      ENTRY_IP="${ENTRY_IP}" EPHEMERAL_ENTRY_IP="true" build_node_json)"

  NODE_ID="${EXIT_ID}" NODE_ROLE="exit" NODE_STATUS="active" \
    PROVIDER="${CLOUD}" CLOUD="${CLOUD}" REGION="${REGION}" ENTRY_NODE_ID="${ENTRY_ID}" \
    cp_register_node "$(NODE_ID="${EXIT_ID}" NODE_ROLE="exit" NODE_STATUS="active" \
      PROVIDER="${CLOUD}" CLOUD="${CLOUD}" REGION="${REGION}" ENTRY_NODE_ID="${ENTRY_ID}" \
      build_node_json)"
else
  log "CONTROL_PLANE_URL unset — skipping registration"
fi

log "node up: entry=${ENTRY_IP} (id=${ENTRY_ID}) exit=${EXIT_IP} (id=${EXIT_ID})"
