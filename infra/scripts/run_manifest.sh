#!/usr/bin/env bash
# run_manifest.sh — live-run identity + durable teardown artifact. Source, don't
# execute. Depends on lib.sh (log/die/require_env/repo_root) being sourced first.
#
# It solves two live-lifecycle problems:
#
#   1. Globally-unique control-plane node IDs. Raw provider instance ids from
#      different clouds share no namespace (a Hetzner id could collide with a Vultr
#      id), so the CP node id is a NAMESPACED logical id "<cloud>:<raw>". The raw
#      provider ids live only in the manifest, for teardown.
#
#   2. Teardown after destroy. Once `terraform destroy` runs, `terraform output` is
#      gone — node_down can no longer rediscover what to retire. The manifest,
#      written 0600 right after apply, is the durable record: workspace, logical CP
#      ids, raw provider ids, IPs, regions, clouds, and the state-backup path. It
#      carries NO secrets.
set -euo pipefail

# logical_id <cloud> <raw-instance-id> -> "<cloud>:<raw>" namespaced CP node id.
logical_id() {
  local cloud="$1" raw="$2"
  [ -n "$cloud" ] || die "logical_id: empty cloud"
  [ -n "$raw" ]   || die "logical_id: empty raw instance id"
  printf '%s:%s' "$cloud" "$raw"
}

# run_dir -> directory holding run manifests (override RUN_DIR; default <repo>/.runs).
run_dir() { printf '%s' "${RUN_DIR:-$(repo_root)/.runs}"; }

# manifest_path <run-id> -> absolute path to the run's manifest JSON.
manifest_path() { printf '%s/%s.json' "$(run_dir)" "$1"; }

# require_live_run -> RUN_ID + TF_WORKSPACE are mandatory for a live lifecycle, and
# TF_WORKSPACE must NOT be 'default': an isolated per-run state is required so a
# mistake can never mutate a shared/global state.
require_live_run() {
  require_env RUN_ID TF_WORKSPACE
  [ "$TF_WORKSPACE" != "default" ] \
    || die "TF_WORKSPACE must not be 'default' for a live run (use an isolated per-run workspace)"
  # RUN_ID / TF_WORKSPACE are env inputs (validated by require_env above), not typos.
  # shellcheck disable=SC2153
  case "$RUN_ID" in
    *[!A-Za-z0-9._-]*) die "RUN_ID must match [A-Za-z0-9._-]" ;;
  esac
}

# tf_select_workspace <tf-dir> -> select (or create) the run's isolated workspace,
# so every apply/output/destroy for this run targets the same isolated state.
tf_select_workspace() {
  local dir="$1"
  terraform -chdir="$dir" workspace select "$TF_WORKSPACE" 2>/dev/null \
    || terraform -chdir="$dir" workspace new "$TF_WORKSPACE"
}

# backup_state <tf-dir> -> copy this workspace's tfstate to a 0600 backup under
# run_dir; echo the backup path (empty if no state file yet). The state is the
# critical teardown artifact, so it is never left world-readable.
backup_state() {
  local dir="$1" dest src
  dest="$(run_dir)/${RUN_ID}.tfstate.backup"
  src="${dir}/terraform.tfstate.d/${TF_WORKSPACE}/terraform.tfstate"
  [ -f "$src" ] || src="${dir}/terraform.tfstate"
  if [ -f "$src" ]; then
    mkdir -p "$(run_dir)"
    ( umask 077; cp "$src" "$dest" )
    printf '%s' "$dest"
  fi
}

# write_stub_manifest -> write a MINIMAL 0600 manifest (run_id + workspace, empty
# node fields) BEFORE apply. A multi-provider apply can bill one VM and then fail on
# the other; set -e would abort before write_manifest, leaving a running, billed VM
# with no teardown key. The stub guarantees node_down can always select the
# workspace and destroy — even after a partial apply. write_manifest overwrites it
# with the full record on success.
write_stub_manifest() {
  require_env RUN_ID TF_WORKSPACE
  local path; path="$(manifest_path "$RUN_ID")"
  mkdir -p "$(dirname "$path")"
  ( umask 077
    jq -n --arg run_id "$RUN_ID" --arg ws "$TF_WORKSPACE" \
      '{run_id:$run_id, tf_workspace:$ws,
        entry:{cp_id:"", raw_id:"", ip:"", cloud:"", region:""},
        exit:{cp_id:"", raw_id:"", ip:"", cloud:"", region:""},
        state_backup:""}' >"$path"
  )
  chmod 600 "$path"
  printf '%s' "$path"
}

# write_manifest -> emit the 0600 run manifest JSON from env; echo its path. NO
# secrets. Required env: RUN_ID TF_WORKSPACE ENTRY_LOGICAL_ID EXIT_LOGICAL_ID
#   ENTRY_RAW_ID EXIT_RAW_ID ENTRY_CLOUD EXIT_CLOUD ENTRY_REGION EXIT_REGION
# Optional: ENTRY_IP EXIT_IP STATE_BACKUP
write_manifest() {
  require_env RUN_ID TF_WORKSPACE ENTRY_LOGICAL_ID EXIT_LOGICAL_ID \
              ENTRY_RAW_ID EXIT_RAW_ID ENTRY_CLOUD EXIT_CLOUD ENTRY_REGION EXIT_REGION
  local path; path="$(manifest_path "$RUN_ID")"
  mkdir -p "$(dirname "$path")"
  ( umask 077
    jq -n \
      --arg run_id "$RUN_ID" --arg ws "$TF_WORKSPACE" \
      --arg eid "$ENTRY_LOGICAL_ID" --arg xid "$EXIT_LOGICAL_ID" \
      --arg eraw "$ENTRY_RAW_ID" --arg xraw "$EXIT_RAW_ID" \
      --arg eip "${ENTRY_IP:-}" --arg xip "${EXIT_IP:-}" \
      --arg ecloud "$ENTRY_CLOUD" --arg xcloud "$EXIT_CLOUD" \
      --arg ereg "$ENTRY_REGION" --arg xreg "$EXIT_REGION" \
      --arg sb "${STATE_BACKUP:-}" \
      '{run_id:$run_id, tf_workspace:$ws,
        entry:{cp_id:$eid, raw_id:$eraw, ip:$eip, cloud:$ecloud, region:$ereg},
        exit:{cp_id:$xid, raw_id:$xraw, ip:$xip, cloud:$xcloud, region:$xreg},
        state_backup:$sb}' >"$path"
  )
  chmod 600 "$path"
  printf '%s' "$path"
}

# manifest_field <run-id> <jq-filter> -> read one value from the manifest. Dies if
# the manifest is missing (teardown cannot proceed safely without it).
manifest_field() {
  local run_id="$1" filter="$2" path
  path="$(manifest_path "$run_id")"
  [ -f "$path" ] || die "run manifest not found: $path (needed for teardown)"
  jq -r "$filter" "$path"
}
