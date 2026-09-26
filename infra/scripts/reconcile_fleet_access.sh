#!/usr/bin/env bash
# Refresh every manifest-backed entry from the authoritative leased snapshot.
# Intended for the systemd timer in infra/systemd. No manifest means failure:
# a configured scheduler must never report success while managing no fleet.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "${HERE}/lib.sh"
# shellcheck source=control_plane.sh
source "${HERE}/control_plane.sh"
# shellcheck source=run_manifest.sh
source "${HERE}/run_manifest.sh"

require_cmd find jq curl
require_env RUN_DIR CONTROL_PLANE_URL CONTROL_PLANE_TOKEN
[ -d "$RUN_DIR" ] || die "RUN_DIR does not exist: ${RUN_DIR}"

count=0
failed=0
current_snapshot=""
cleanup_fleet_reconcile() { [ -z "$current_snapshot" ] || rm -f "$current_snapshot"; }
trap cleanup_fleet_reconcile EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
while IFS= read -r manifest; do
  [ -n "$manifest" ] || continue
  run_id="$(jq -er '.run_id | select(type == "string" and length > 0)' "$manifest")" \
    || { log "fleet reconcile: invalid manifest ${manifest}"; failed=1; continue; }
  expected="${RUN_DIR}/${run_id}.json"
  [ "$manifest" = "$expected" ] \
    || { log "fleet reconcile: manifest filename does not match run_id: ${manifest}"; failed=1; continue; }
  count=$((count + 1))
  entry="$(manifest_field "$run_id" '.entry.cp_id')"
  [ -n "$entry" ] || { log "fleet reconcile: run ${run_id} has no entry id"; failed=1; continue; }
  snapshot="$(mktemp)"; current_snapshot="$snapshot"
  chmod 600 "$snapshot"
  if ! ( cp_get_access_users "$entry" ) >"$snapshot"; then
    rm -f "$snapshot"; failed=1; continue
  fi
  node_json="$(cp_get_node "$entry")" || { rm -f "$snapshot"; failed=1; continue; }
  reality_names="$(jq -er 'first(.transports[] | select(.enabled == true and .type == "vless-reality") | .vless_reality.server_names) | join(",") | select(length > 0)' <<<"$node_json")" \
    || { log "fleet reconcile: VLESS context missing for ${entry}"; rm -f "$snapshot"; failed=1; continue; }
  reality_dest="$(jq -er 'first(.transports[] | select(.enabled == true and .type == "vless-reality") | .vless_reality.dest) | select(length > 0)' <<<"$node_json")" \
    || { log "fleet reconcile: VLESS destination missing for ${entry}"; rm -f "$snapshot"; failed=1; continue; }
  hy2_sni="$(jq -er 'first(.transports[] | select(.enabled == true and .type == "hysteria2") | .hysteria2.sni) | select(length > 0)' <<<"$node_json")" \
    || { log "fleet reconcile: Hysteria2 context missing for ${entry}"; rm -f "$snapshot"; failed=1; continue; }
  log "fleet reconcile: run=${run_id} entry=${entry}"
  RUN_ID="$run_id" NODE="$entry" ACCESS_SNAPSHOT_FILE="$snapshot" \
    REALITY_SERVER_NAMES="$reality_names" REALITY_DEST="$reality_dest" HY2_SNI="$hy2_sni" \
    "${HERE}/access_sync.sh" || failed=1
  rm -f "$snapshot"
  current_snapshot=""
done < <(find "$RUN_DIR" -maxdepth 1 -type f -name '*.json' ! -name '*.tokens.json' | sort)

[ "$count" -gt 0 ] || die "fleet reconcile: no run manifests in ${RUN_DIR}"
[ "$failed" -eq 0 ] || die "fleet reconcile: one or more runs failed"
log "fleet reconcile: refreshed ${count} manifest-backed run(s)"
