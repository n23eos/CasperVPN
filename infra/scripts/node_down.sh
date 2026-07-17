#!/usr/bin/env bash
# node_down.sh — graceful drain then teardown of an entry+exit PAIR, cost-safe and
# idempotent. Reads the durable 0600 run manifest (NOT `terraform output`, which is
# gone after a destroy) to learn both nodes' logical CP ids, IPs, and the isolated
# workspace, so a repeat run after the state is already destroyed still knows what
# to retire.
#
# Cost-safety: drain / demote / CP-retire failures are AGGREGATED and reported but
# NEVER block the `terraform destroy` — leaving billable VMs alive because the
# control-plane is unreachable is the one outcome we must avoid. Only a destroy
# failure is a fatal (non-zero) exit, with an explicit emergency manual command.
#
# Usage:  RUN_ID=<run-id> infra/scripts/node_down.sh
# Env:    provider tokens, CONTROL_PLANE_URL, CONTROL_PLANE_TOKEN, SSH_PUBKEY
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "${HERE}/lib.sh"
# shellcheck source=control_plane.sh
source "${HERE}/control_plane.sh"
# shellcheck source=run_manifest.sh
source "${HERE}/run_manifest.sh"

require_cmd terraform ansible-playbook jq curl
require_env RUN_ID
ROOT="$(repo_root)"
TF_DIR="${ROOT}/${TF_ENV_DIR_DEFAULT}"
ANSIBLE_DIR="${ROOT}/${ANSIBLE_DIR_DEFAULT}"

# Everything the teardown needs comes from the durable manifest.
TF_WORKSPACE="$(manifest_field "$RUN_ID" '.tf_workspace')"
[ "$TF_WORKSPACE" != "default" ] || die "manifest workspace is 'default' — refusing (isolated state required)"
export TF_WORKSPACE
ENTRY_CP="$(manifest_field "$RUN_ID" '.entry.cp_id')"
EXIT_CP="$(manifest_field "$RUN_ID" '.exit.cp_id')"
ENTRY_RAW="$(manifest_field "$RUN_ID" '.entry.raw_id')"
EXIT_RAW="$(manifest_field "$RUN_ID" '.exit.raw_id')"
ENTRY_IP="$(manifest_field "$RUN_ID" '.entry.ip')"
EXIT_IP="$(manifest_field "$RUN_ID" '.exit.ip')"

terraform -chdir="${TF_DIR}" init -input=false >/dev/null
tf_select_workspace "${TF_DIR}"

# Aggregate soft failures (drain/retire) but keep going to the destroy.
WARN=()

# 1. Graceful software drain (best effort — hosts may already be gone).
if [ -n "${ENTRY_IP}${EXIT_IP}" ]; then
  INV="$(mktemp)"; trap 'rm -f "${INV}"' EXIT
  {
    printf '[entry]\n'; [ -n "${ENTRY_IP}" ] && printf '%s ansible_host=%s\n' "${ENTRY_RAW}" "${ENTRY_IP}"
    printf '[exit]\n';  [ -n "${EXIT_IP}" ]  && printf '%s ansible_host=%s\n' "${EXIT_RAW}"  "${EXIT_IP}"
    printf '[nodes:children]\nentry\nexit\n'
  } >"${INV}"
  log "draining run ${RUN_ID}"
  ( ansible-playbook -i "${INV}" "${ANSIBLE_DIR}/playbooks/node-down.yml" -e target=nodes ) \
    || WARN+=("drain (hosts may be gone)")
fi

# 2. Retire BOTH Node records (best effort; a subshell isolates cp_retire_node's
#    die so an unreachable CP never blocks the destroy). Empty ids come from a stub
#    manifest after a partial apply — there is nothing to retire, only VMs to reap.
if [ -n "${CONTROL_PLANE_URL:-}" ]; then
  [ -n "${ENTRY_CP}" ] && { ( cp_retire_node "${ENTRY_CP}" ) || WARN+=("retire entry ${ENTRY_CP}"); }
  [ -n "${EXIT_CP}" ]  && { ( cp_retire_node "${EXIT_CP}" )  || WARN+=("retire exit ${EXIT_CP}"); }
  [ -z "${ENTRY_CP}${EXIT_CP}" ] && log "no CP ids in manifest (partial apply) — destroying VMs only"
else
  log "CONTROL_PLANE_URL unset — skipping retire"
fi

# 3. Tear down the infra — ALWAYS attempted. A destroy failure is the only fatal.
log "terraform destroy (workspace=${TF_WORKSPACE})"
if ! terraform -chdir="${TF_DIR}" destroy -auto-approve -input=false \
      -var "ssh_pubkey=${SSH_PUBKEY:-unused}"; then
  log "ERROR: terraform destroy FAILED — billable VMs may still exist"
  log "EMERGENCY: run manually —"
  log "  terraform -chdir=${TF_DIR} workspace select ${TF_WORKSPACE} && \\"
  log "  terraform -chdir=${TF_DIR} destroy -auto-approve -var ssh_pubkey=unused"
  exit 1
fi

if [ "${#WARN[@]}" -gt 0 ]; then
  log "run ${RUN_ID} down — infra destroyed; NON-FATAL warnings: ${WARN[*]}"
else
  log "run ${RUN_ID} down — infra destroyed, both nodes retired"
fi
