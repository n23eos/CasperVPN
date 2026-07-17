#!/usr/bin/env bash
# Shared helpers for the node lifecycle scripts. Source, don't execute.
set -euo pipefail

log()  { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }
die()  { log "ERROR: $*"; exit 1; }

require_cmd() {
  local c
  for c in "$@"; do
    command -v "$c" >/dev/null 2>&1 || die "required command not found: $c"
  done
}

require_env() {
  local v
  for v in "$@"; do
    [ -n "${!v:-}" ] || die "required env var not set: $v"
  done
}

# retry <max> <cmd...>  — exponential-ish backoff.
retry() {
  local max="$1"; shift
  local n=0
  until "$@"; do
    n=$((n + 1))
    [ "$n" -ge "$max" ] && return 1
    log "retry $n/$max: $*"
    sleep $((n * 2))
  done
}

# Repo root = two levels up from infra/scripts/.
repo_root() {
  cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd
}

# Consumed by node_up.sh / node_rotate.sh / node_down.sh which source this file.
# shellcheck disable=SC2034
TF_ENV_DIR_DEFAULT="infra/terraform/envs/example"
# shellcheck disable=SC2034
ANSIBLE_DIR_DEFAULT="infra/ansible"
