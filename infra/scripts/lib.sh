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

# acquire_lock <path> — portable NON-BLOCKING mutex. Uses flock(1) when available
# (Linux), else an atomic mkdir lock (macOS has no flock). Returns non-zero if the
# lock is already held. It does NOT install a trap — the caller owns a single
# cleanup trap (installing one here would clobber the caller's). Release with
# release_lock. Set LOCK_FORCE_MKDIR=true to force the portable path (tests).
acquire_lock() {
  local path="$1"
  if [ "${LOCK_FORCE_MKDIR:-false}" != "true" ] && command -v flock >/dev/null 2>&1; then
    exec 9>"${path}.flock" || return 1
    flock -n 9 || return 1
    return 0
  fi
  # mkdir is atomic: the directory's existence IS the lock.
  mkdir "${path}.d" 2>/dev/null || return 1
  return 0
}

# release_lock <path> — release the mkdir lock (flock releases on process exit;
# the rmdir is then a harmless no-op).
release_lock() {
  rmdir "${1}.d" 2>/dev/null || true
}
