#!/usr/bin/env bash
# Pure-shell contract test for personal access convergence. Ansible is stubbed:
# the test inspects the exact vars file and never opens SSH or touches a node.
set -uo pipefail
cd "$(dirname "$0")/../.."

pass=0; fail=0
ok()  { echo "  ok: $1"; pass=$((pass+1)); }
bad() { echo "  FAIL: $1" >&2; fail=$((fail+1)); }

TMP="$(mktemp -d)"
BIN="$TMP/bin"; RUN_DIR="$TMP/runs"; mkdir -p "$BIN" "$RUN_DIR"
CAPTURE="$TMP/vars.json"; CALLS="$TMP/calls"; export CAPTURE CALLS
cat >"$BIN/ansible-playbook" <<'SH'
#!/usr/bin/env bash
for arg in "$@"; do
  case "$arg" in @*) cp "${arg#@}" "$CAPTURE";; esac
done
printf 'playbook %s\n' "$*" >>"$CALLS"
SH
cat >"$BIN/ansible" <<'SH'
#!/usr/bin/env bash
printf 'ansible %s\n' "$*" >>"$CALLS"
SH
chmod +x "$BIN"/*

export PATH="$BIN:$PATH" RUN_DIR RUN_ID=run-1 NODE=cloud:entry
export PAIR_PSK=pair-secret HY2_SNI=hy2.example.test
export REALITY_SERVER_NAMES=reality.example.test REALITY_DEST=reality.example.test:443
jq -n '{run_id:"run-1",tf_workspace:"run-1",
  entry:{cp_id:"cloud:entry",raw_id:"entry-raw",ip:"192.0.2.10",cloud:"cloud",region:"r1"},
  exit:{cp_id:"cloud:exit",raw_id:"exit-raw",ip:"198.51.100.20",cloud:"cloud",region:"r2"},state_backup:""}' \
  >"$RUN_DIR/run-1.json"

SNAP="$TMP/access.json"; export ACCESS_SNAPSHOT_FILE="$SNAP"
jq -n '{revision:"rev-b",valid_until:"2999-01-01T00:00:00Z",users:[
  {uuid:"user-b",short_id:"bb",hysteria2_password:"hy2-b"}]}' >"$SNAP"
if bash infra/scripts/access_sync.sh >/dev/null 2>&1; then
  ok "non-empty leased snapshot converges"
else
  bad "non-empty snapshot failed"
fi
jq -e '
  .reality_reuse_existing_key == true and
  .reality_users == [{"uuid":"user-b","short_id":"bb"}] and
  .hysteria2_users == [{"password":"hy2-b"}] and
  .access_snapshot_revision == "rev-b" and
  .reality_server_names == ["reality.example.test"]
' "$CAPTURE" >/dev/null 2>&1 \
  && ok "revoked user is absent from both transports while user B remains" \
  || bad "personal multi-transport vars are incomplete"
grep -q 'pair-secret' "$CAPTURE" && ok "entry-exit PSK reaches secure vars file" || bad "missing pair PSK"

: >"$CALLS"
jq -n '{revision:"rev-empty",valid_until:"2999-01-01T00:00:00Z",users:[]}' >"$SNAP"
if bash infra/scripts/access_sync.sh >/dev/null 2>&1 \
   && grep -q 'name=sing-box state=stopped' "$CALLS" \
   && ! grep -q '^playbook ' "$CALLS"; then
  ok "empty snapshot stops sing-box without rendering an open inbound"
else
  bad "empty snapshot did not fail closed"
fi

jq -n '{revision:"rev-old",valid_until:"2000-01-01T00:00:00Z",users:[
  {uuid:"user-b",short_id:"bb",hysteria2_password:"hy2-b"}]}' >"$SNAP"
( bash infra/scripts/access_sync.sh ) >/dev/null 2>&1 \
  && bad "expired snapshot was accepted" \
  || ok "expired snapshot is rejected before node mutation"

rm -rf "$TMP"
echo "access-sync-guard: ${pass} ok, ${fail} fail"
[ "$fail" = 0 ]
