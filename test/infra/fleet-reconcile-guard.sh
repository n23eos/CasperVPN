#!/usr/bin/env bash
# The scheduler must enumerate manifests, preserve their run identity, and exit
# nonzero if any guarded reconcile fails. The reconcile command is stubbed.
set -uo pipefail
cd "$(dirname "$0")/../.."

pass=0; fail=0
ok()  { echo "  ok: $1"; pass=$((pass+1)); }
bad() { echo "  FAIL: $1" >&2; fail=$((fail+1)); }

TMP="$(mktemp -d)"; RUN_DIR="$TMP/runs"; mkdir -p "$RUN_DIR" "$TMP/scripts" "$TMP/bin"
cp infra/scripts/reconcile_fleet_access.sh infra/scripts/lib.sh \
  infra/scripts/control_plane.sh infra/scripts/run_manifest.sh "$TMP/scripts/"
cat >"$TMP/scripts/access_sync.sh" <<'SH'
#!/usr/bin/env bash
printf '%s %s %s\n' "$RUN_ID" "$NODE" "$(jq -r '.users | length' "$ACCESS_SNAPSHOT_FILE")" >>"${FLEET_CALLS:?}"
[ "$RUN_ID" != "run-fail" ]
SH
chmod +x "$TMP/scripts/"*.sh
cat >"$TMP/bin/curl" <<'SH'
#!/usr/bin/env bash
case "$*" in
  */access-users*) printf '%s' '{"revision":"r1","valid_until":"2999-01-01T00:00:00Z","users":[]}' ;;
  *) printf '%s' '{"transports":[
    {"enabled":true,"type":"vless-reality","vless_reality":{"server_names":["front.test"],"dest":"front.test:443"}},
    {"enabled":true,"type":"hysteria2","hysteria2":{"sni":"hy2.test"}}]}' ;;
esac
SH
chmod +x "$TMP/bin/curl"
export PATH="$TMP/bin:$PATH" RUN_DIR CONTROL_PLANE_URL=http://cp.invalid CONTROL_PLANE_TOKEN=test FLEET_CALLS="$TMP/calls"

jq -n '{run_id:"run-a",entry:{cp_id:"cloud:entry-a"}}' >"$RUN_DIR/run-a.json"
jq -n '{run_id:"run-b",entry:{cp_id:"cloud:entry-b"}}' >"$RUN_DIR/run-b.json"
if bash "$TMP/scripts/reconcile_fleet_access.sh" >/dev/null 2>&1 \
   && [ "$(wc -l <"$FLEET_CALLS" | tr -d ' ')" = 2 ]; then
  ok "scheduler reconciles every manifest-backed run"
else
  bad "scheduler did not enumerate the manifest fleet"
fi

: >"$FLEET_CALLS"; jq -n '{run_id:"run-fail",entry:{cp_id:"cloud:entry-fail"}}' >"$RUN_DIR/run-fail.json"
( bash "$TMP/scripts/reconcile_fleet_access.sh" ) >/dev/null 2>&1 \
  && bad "scheduler hid a run failure" \
  || ok "scheduler exits nonzero when any run fails"
[ "$(wc -l <"$FLEET_CALLS" | tr -d ' ')" = 3 ] \
  && ok "scheduler attempts all runs before reporting failure" \
  || bad "scheduler stopped before refreshing the whole fleet"
grep -q '^run-a cloud:entry-a 0$' "$FLEET_CALLS" \
  && ok "scheduler passes exact manifest node and empty snapshot to access sync" \
  || bad "scheduler lost manifest or snapshot identity"

rm -rf "$RUN_DIR"; mkdir -p "$RUN_DIR"; : >"$FLEET_CALLS"
( bash "$TMP/scripts/reconcile_fleet_access.sh" ) >/dev/null 2>&1 \
  && bad "scheduler accepted an empty fleet" \
  || ok "scheduler rejects an empty manifest fleet"

grep -q '^ExecStart=/opt/caspervpn/infra/scripts/reconcile_fleet_access.sh$' infra/systemd/caspervpn-access-reconcile.service \
  && grep -q '^EnvironmentFile=/etc/caspervpn/access-reconcile.env$' infra/systemd/caspervpn-access-reconcile.service \
  && ok "systemd service runs the manifest reconciler with a dedicated env file" \
  || bad "systemd service is not wired to the fleet reconciler"
grep -q '^OnUnitActiveSec=2min$' infra/systemd/caspervpn-access-reconcile.timer \
  && ok "timer refreshes inside the five-minute lease bound" \
  || bad "timer interval is outside the tested lease policy"

rm -rf "$TMP"
echo "fleet-reconcile-guard: ${pass} ok, ${fail} fail"
[ "$fail" = 0 ]
