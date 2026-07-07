#!/usr/bin/env bash
# node_down.sh — graceful drain then teardown: ansible node-down -> retire Node
# in control-plane -> terraform destroy. Idempotent (retire tolerates 404;
# destroy of already-gone infra is a no-op).
#
# Usage:  NODE=<node-id> infra/scripts/node_down.sh
# Env:    provider tokens, CONTROL_PLANE_URL, CONTROL_PLANE_TOKEN
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "${HERE}/lib.sh"
# shellcheck source=control_plane.sh
source "${HERE}/control_plane.sh"

require_cmd terraform ansible-playbook jq curl
require_env NODE
ROOT="$(repo_root)"
TF_DIR="${ROOT}/${TF_ENV_DIR_DEFAULT}"
ANSIBLE_DIR="${ROOT}/${ANSIBLE_DIR_DEFAULT}"

ENTRY_IP="$(terraform -chdir="${TF_DIR}" output -raw entry_ip 2>/dev/null || echo '')"
EXIT_IP="$(terraform -chdir="${TF_DIR}" output -raw exit_ip 2>/dev/null || echo '')"

# 1. Graceful software drain (best effort — host may already be gone).
if [ -n "${ENTRY_IP}${EXIT_IP}" ]; then
  INV="$(mktemp)"; trap 'rm -f "${INV}"' EXIT
  {
    printf '[entry]\n'
    [ -n "${ENTRY_IP}" ] && printf '%s ansible_host=%s\n' "${NODE}-entry" "${ENTRY_IP}"
    printf '[exit]\n'
    [ -n "${EXIT_IP}" ] && printf '%s ansible_host=%s\n' "${NODE}-exit" "${EXIT_IP}"
    printf '[nodes:children]\nentry\nexit\n'
  } >"${INV}"
  log "draining node ${NODE}"
  ansible-playbook -i "${INV}" "${ANSIBLE_DIR}/playbooks/node-down.yml" -e target=nodes || \
    log "drain reported errors (host may be gone) — continuing"
fi

# 2. Retire the Node record.
if [ -n "${CONTROL_PLANE_URL:-}" ]; then
  cp_retire_node "${NODE}"
else
  log "CONTROL_PLANE_URL unset — skipping retire"
fi

# 3. Tear down the infra.
log "terraform destroy"
terraform -chdir="${TF_DIR}" destroy -auto-approve -input=false \
  -var "ssh_pubkey=${SSH_PUBKEY:-unused}"

log "node ${NODE} down"
