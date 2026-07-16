#!/usr/bin/env bash
# reconcile-e2e.sh — the capstone: closes the control-plane -> data-plane loop.
# It drives the REAL reconcile_node.sh state machine (sourced, real _cp, real
# hooks) against a live control-plane stack AND a real entry->exit->echo container
# chain. Proves: a real ban/cancel through the CP changes the eligibility revision;
# the reconciler resyncs the node's REALITY allow-list; the banned user's VLESS
# dies while the other keeps both transports; the run is idempotent; and the
# R1/R2 race and a broken transport both leave the pair provisioning.
#
# OPT-IN: REALITY_DEST + REALITY_SERVER_NAME (SKIP without; FAIL on non-TLS-1.3).
set -uo pipefail
cd "$(dirname "$0")/../.."
export RECONCILE_LIB_ONLY=true
# shellcheck source=/dev/null
source infra/scripts/reconcile_node.sh   # brings lib + control_plane + probe + _cp + reconcile_pair
set +e

PROJECT=caspervpn-recon
if docker compose version >/dev/null 2>&1; then COMPOSE_BIN=(docker compose); else COMPOSE_BIN=(docker-compose); fi
COMPOSE=("${COMPOSE_BIN[@]}" -p "$PROJECT" -f docker-compose.dev.yml -f test/e2e/docker-compose.recon.yml)
CORE=(postgres control-plane subscription billing)
CP=http://localhost:58081; SUB=http://localhost:58082; BILL=http://localhost:58084
ADMIN=dev-admin-token; MOCK_SECRET=dev-mock-webhook-secret
export CONTROL_PLANE_URL="$CP" CONTROL_PLANE_TOKEN="$ADMIN"
IMG=ghcr.io/sagernet/sing-box:v1.11.4
NET="${PROJECT}_default"
ECHO=re-echo EXIT=re-exit ENTRY=re-entry CLIENT=re-client VERIFY=re-verify
SOCKS_PORT=21097
export PROBE_IMG="$IMG" PROBE_NET="$NET" PROBE_CLIENT="$CLIENT"
WORK="$(mktemp -d "$(pwd)/test/e2e/.recon.XXXXXX")"
CURL=(curl -sS --max-time 10)

skip(){ echo "SKIP: $*" >&2; exit 0; }
step(){ echo "==> $*"; }
teardown(){ docker rm -f "$ECHO" "$EXIT" "$ENTRY" "$CLIENT" "$VERIFY" >/dev/null 2>&1 || true; "${COMPOSE[@]}" down -v --remove-orphans >/dev/null 2>&1 || true; }
cleanup(){ teardown; rm -rf "$WORK"; }
trap cleanup EXIT
wto(){ local s="$1"; shift; ( "$@" ) & local p=$!; ( sleep "$s"; kill -9 "$p" 2>/dev/null ) & local w=$!; local rc=0; wait "$p" 2>/dev/null || rc=$?; kill "$w" 2>/dev/null; wait "$w" 2>/dev/null||true; return "$rc"; }

for t in docker jq openssl nc; do command -v "$t" >/dev/null || die "missing $t"; done
[ -n "${REALITY_DEST:-}" ] && [ -n "${REALITY_SERVER_NAME:-}" ] || skip "REALITY_DEST/REALITY_SERVER_NAME not set"
DEST_HOST="${REALITY_DEST%%:*}"; DEST_PORT="${REALITY_DEST##*:}"; [ "$DEST_PORT" = "$REALITY_DEST" ] && DEST_PORT=443
step "preflight $REALITY_DEST TLS 1.3"
tls13(){ echo | openssl s_client -connect "${DEST_HOST}:${DEST_PORT}" -servername "$REALITY_SERVER_NAME" -tls1_3 2>/dev/null | grep -q TLSv1.3; }
wto 20 tls13 || die "$REALITY_DEST not TLS 1.3"

# ---------------------------------------------------------------------------
step "bring up control-plane stack (SEED off)"
teardown
"${COMPOSE[@]}" build -q "${CORE[@]}"
"${COMPOSE[@]}" up -d postgres
for i in $(seq 1 30); do "${COMPOSE[@]}" exec -T postgres pg_isready -U caspervpn >/dev/null 2>&1 && break; [ "$i" = 30 ] && die "pg timeout"; sleep 2; done
"${COMPOSE[@]}" exec -T postgres psql -q -v ON_ERROR_STOP=1 -U caspervpn -d caspervpn < services/billing/internal/store/schema.sql
"${COMPOSE[@]}" exec -T postgres psql -q -v ON_ERROR_STOP=1 -U caspervpn -d caspervpn < services/subscription/internal/controlplane/schema.sql
"${COMPOSE[@]}" up -d "${CORE[@]}"
for u in "$CP/healthz" "$SUB/healthz" "$BILL/healthz"; do for i in $(seq 1 30); do "${CURL[@]}" -o /dev/null "$u" 2>/dev/null && break; [ "$i" = 30 ] && die "$u unhealthy"; sleep 2; done; done

# mkuser -> creates+pays a user; echoes "id uuid short_id sub_id"
mkuser(){
  local uj; uj="$("${CURL[@]}" -X POST "$CP/v1/users" -H "Authorization: Bearer $ADMIN" -H 'Content-Type: application/json' -d "{\"telegram_id\":$1}")"
  local id uuid sid; id="$(jq -re .id <<<"$uj")"; uuid="$(jq -re .uuid <<<"$uj")"; sid="$(jq -re .reality_short_id <<<"$uj")"
  local inv; inv="$("${CURL[@]}" -X POST "$BILL/v1/invoices" -H 'Content-Type: application/json' -d "{\"anon_user_id\":\"$id\",\"plan\":\"basic\",\"currency\":\"BTC\"}")"
  local iid amt cur; iid="$(jq -re .invoice_id <<<"$inv")"; amt="$(jq -re .amount <<<"$inv")"; cur="$(jq -re .currency <<<"$inv")"
  local ts body sig; ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  body="$(jq -cn --arg e "re-$iid" --arg i "$iid" --arg a "$amt" --arg c "$cur" --arg t "$ts" '{external_id:$e,invoice_id:$i,status:"settled",amount:$a,currency:$c,confirmations:6,ts:$t}')"
  sig="$(printf '%s' "$body" | openssl dgst -sha256 -hmac "$MOCK_SECRET" -hex | awk '{print $NF}')"
  "${CURL[@]}" -o /dev/null -X POST "$BILL/v1/webhooks/mock" -H "X-Signature: $sig" -H 'Content-Type: application/json' --data-binary "$body"
  local subid; subid="$("${CURL[@]}" -H "Authorization: Bearer $ADMIN" "$CP/v1/users/$id" | jq -r .subscription_id)"
  echo "$id $uuid $sid $subid"
}
step "create + pay two eligible users"
read -r U1_ID U1_UUID U1_SID U1_SUB < <(mkuser 800001)
read -r U2_ID U2_UUID U2_SID U2_SUB < <(mkuser 800002)
[ -n "$U1_UUID" ] && [ -n "$U2_UUID" ] || die "user creation failed"
echo "    user1=$U1_UUID/$U1_SID  user2=$U2_UUID/$U2_SID"

# ---------------------------------------------------------------------------
step "credentials + echo/exit containers"
KP="$(docker run --rm "$IMG" generate reality-keypair)"; RPRIV="$(awk '/PrivateKey/{print $2}' <<<"$KP")"; RPUB="$(awk '/PublicKey/{print $2}' <<<"$KP")"
HY2_PW="$(openssl rand -hex 16)"; PSK="$(openssl rand -base64 32)"
docker run -d --name "$ECHO" --network "$NET" traefik/whoami >/dev/null; sleep 1
jq -n --arg psk "$PSK" '{log:{level:"warn"},inbounds:[{type:"shadowsocks",tag:"ss-in",listen:"::",listen_port:8388,method:"2022-blake3-aes-256-gcm",password:$psk}],outbounds:[{type:"direct",tag:"direct"}]}' >"$WORK/exit.json"
docker run -d --name "$EXIT" --network "$NET" -v "$WORK/exit.json:/c.json:ro" "$IMG" run -c /c.json >/dev/null; sleep 1
EXIT_IP="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$EXIT")"
openssl req -x509 -nodes -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -keyout "$WORK/hy2.key" -out "$WORK/hy2.crt" -subj "/CN=${REALITY_SERVER_NAME}" -days 3 >/dev/null 2>&1

step "register entry (vless+hy2) + exit nodes in CP (provisioning)"
# shellcheck source=/dev/null
source infra/scripts/reality_sync.sh
ENTRY_ID=re-entry-1; EXIT_ID=re-exit-1
NODE_ID="$ENTRY_ID" NODE_ROLE=entry NODE_STATUS=provisioning PROVIDER=local CLOUD=local REGION=RU-MOW \
  ENTRY_IP="$ENTRY" EPHEMERAL_ENTRY_IP=false \
  REALITY_PUBLIC_KEY="$RPUB" REALITY_SERVER_NAMES="$REALITY_SERVER_NAME" REALITY_DEST="$REALITY_DEST" \
  HY2_PASSWORD="$HY2_PW" HY2_SNI="$REALITY_SERVER_NAME" HY2_INSECURE=true reality_sync_node
"${CURL[@]}" -X POST "$CP/v1/nodes" -H "Authorization: Bearer $ADMIN" -H 'Content-Type: application/json' \
  -d "{\"id\":\"$EXIT_ID\",\"role\":\"exit\",\"status\":\"provisioning\",\"entry_node_id\":\"$ENTRY_ID\",\"provider\":\"local\",\"cloud\":\"local\",\"region\":\"RU-MOW\",\"ephemeral_entry_ip\":false,\"transports\":[]}" >/dev/null

# ---------------------------------------------------------------------------
# hooks (called by reconcile_pair via RECONCILE_* env pointing at these funcs)
render_entry(){ # $1 = users json array [{uuid,short_id}]
  local users="$1"
  jq -n --argjson users "$users" --arg priv "$RPRIV" --arg dh "$DEST_HOST" --argjson dp "$DEST_PORT" \
    --arg sni "$REALITY_SERVER_NAME" --arg hp "$HY2_PW" --arg psk "$PSK" --arg exip "$EXIT_IP" '{
    log:{level:"warn"},
    inbounds:[
      {type:"vless",tag:"vless-in",listen:"::",listen_port:443,users:($users|map({uuid:.uuid,flow:"xtls-rprx-vision"})),
       tls:{enabled:true,server_name:$sni,reality:{enabled:true,handshake:{server:$dh,server_port:$dp},private_key:$priv,short_id:($users|map(.short_id))}}},
      {type:"hysteria2",tag:"hy2-in",listen:"::",listen_port:8443,users:[{password:$hp}],
       tls:{enabled:true,alpn:["h3"],certificate_path:"/etc/hy2.crt",key_path:"/etc/hy2.key"}}
    ],
    outbounds:[{type:"shadowsocks",tag:"to-exit",server:$exip,server_port:8388,method:"2022-blake3-aes-256-gcm",password:$psk},{type:"direct",tag:"direct"}],
    route:{final:"to-exit"}}' >"$WORK/entry.json"
}
start_entry(){ docker rm -f "$ENTRY" >/dev/null 2>&1||true; docker run -d --name "$ENTRY" --network "$NET" -v "$WORK/entry.json:/c.json:ro" -v "$WORK/hy2.crt:/etc/hy2.crt:ro" -v "$WORK/hy2.key:/etc/hy2.key:ro" "$IMG" run -c /c.json >/dev/null; sleep 2; }

hook_verify_exit(){  # authenticated SS2022 client -> exit -> echo -> egress==exit
  jq -n --arg exip "$EXIT_IP" --arg psk "$PSK" '{log:{level:"warn"},inbounds:[{type:"mixed",tag:"in",listen:"0.0.0.0",listen_port:1080}],
    outbounds:[{type:"shadowsocks",tag:"o",server:$exip,server_port:8388,method:"2022-blake3-aes-256-gcm",password:$psk},{type:"direct",tag:"direct"}],route:{final:"o"}}' >"$WORK/vexit.json"
  local r; r="$(probe_transport shadowsocks-2022 "$WORK/vexit.json" "$SOCKS_PORT" "$ECHO" "$EXIT_IP")"; echo "$r" | jq -e '.authenticated_http and .exit_ip_verified' >/dev/null
}
hook_apply(){  # receives RECONCILE_USERS; renders + restarts entry
  [ -n "${RECONCILE_USERS:-}" ] || return 1
  render_entry "$RECONCILE_USERS"; start_entry
}
vless_probe(){ local uuid="$1" sid="$2"; jq -n --arg u "$uuid" --arg sni "$REALITY_SERVER_NAME" --arg pub "$RPUB" --arg sid "$sid" --arg s "$ENTRY" '{log:{level:"warn"},inbounds:[{type:"mixed",tag:"in",listen:"0.0.0.0",listen_port:1080}],outbounds:[{type:"vless",tag:"vless-out",server:$s,server_port:443,uuid:$u,flow:"xtls-rprx-vision",tls:{enabled:true,server_name:$sni,utls:{enabled:true,fingerprint:"chrome"},reality:{enabled:true,public_key:$pub,short_id:$sid}}},{type:"direct",tag:"direct"}],route:{final:"vless-out"}}' >"$WORK/client.json"; probe_transport vless-reality "$WORK/client.json" "$SOCKS_PORT" "$ECHO" "$EXIT_IP"; }
hy2_probe(){ jq -n --arg s "$ENTRY" --arg pw "$HY2_PW" --arg sni "$REALITY_SERVER_NAME" '{log:{level:"warn"},inbounds:[{type:"mixed",tag:"in",listen:"0.0.0.0",listen_port:1080}],outbounds:[{type:"hysteria2",tag:"hy2-out",server:$s,server_port:8443,password:$pw,tls:{enabled:true,server_name:$sni,insecure:true,alpn:["h3"]}},{type:"direct",tag:"direct"}],route:{final:"hy2-out"}}' >"$WORK/client.json"; probe_transport hysteria2 "$WORK/client.json" "$SOCKS_PORT" "$ECHO" "$EXIT_IP"; }
hook_probe(){  # the reconciler's >=2 evidence: vless(user2) + hy2
  local a b; a="$(vless_probe "$U2_UUID" "$U2_SID")"; b="$(hy2_probe)"
  jq -sc '.' <<<"$a"$'\n'"$b"
}
export RECONCILE_VERIFY_EXIT=hook_verify_exit RECONCILE_APPLY_CMD=hook_apply RECONCILE_PROBE_CMD=hook_probe
verified(){ jq -e '.authenticated_http==true and .exit_ip_verified==true' >/dev/null; }
status(){ "${CURL[@]}" -H "Authorization: Bearer $ADMIN" "$CP/v1/nodes/$1" | jq -r .status; }

# ---------------------------------------------------------------------------
step "R1 before ban includes both users"
REV1="$("${CURL[@]}" -H "Authorization: Bearer $ADMIN" "$CP/v1/nodes/$ENTRY_ID/reality-users" | jq -r .revision)"
"${CURL[@]}" -H "Authorization: Bearer $ADMIN" "$CP/v1/nodes/$ENTRY_ID/reality-users" | jq -e --arg u "$U1_UUID" 'any(.users[];.uuid==$u)' >/dev/null || die "R1 missing user1"

step "reconcile #1: demote pair -> activate exit -> activate entry"
reconcile_pair "$ENTRY_ID" "$EXIT_ID" || die "reconcile #1 failed"
[ "$(status "$EXIT_ID")" = active ] && [ "$(status "$ENTRY_ID")" = active ] || die "pair not both active after reconcile #1 (exit=$(status "$EXIT_ID") entry=$(status "$ENTRY_ID"))"
vless_probe "$U1_UUID" "$U1_SID" | verified || die "user1 vless should tunnel after reconcile #1"
vless_probe "$U2_UUID" "$U2_SID" | verified || die "user2 vless should tunnel"
hy2_probe | verified || die "user2 hy2 should tunnel"
echo "    both active; both users tunnel; user2 has vless+hy2"

# ---------------------------------------------------------------------------
step "BAN user1 through the real CP (cancel subscription)"
"${CURL[@]}" -X POST "$CP/v1/subscriptions/$U1_SUB/cancel" -H "Authorization: Bearer $ADMIN" >/dev/null
REV2="$("${CURL[@]}" -H "Authorization: Bearer $ADMIN" "$CP/v1/nodes/$ENTRY_ID/reality-users" | jq -r .revision)"
[ "$REV2" != "$REV1" ] || die "revision did not change after ban"
"${CURL[@]}" -H "Authorization: Bearer $ADMIN" "$CP/v1/nodes/$ENTRY_ID/reality-users" | jq -e --arg u "$U1_UUID" 'any(.users[];.uuid==$u)|not' >/dev/null || die "banned user1 still in R1"
echo "    revision changed; user1 excluded from R1"

step "reconcile #2: after ban"
reconcile_pair "$ENTRY_ID" "$EXIT_ID" || die "reconcile #2 failed"
vless_probe "$U1_UUID" "$U1_SID" | verified && die "BANNED user1 still tunnels vless" || echo "    user1 vless dead"
vless_probe "$U2_UUID" "$U2_SID" | verified || die "user2 vless must still tunnel"
hy2_probe | verified || die "user2 hy2 must still tunnel"
echo "    user1 removed; user2 keeps both transports"

step "reconcile #3: idempotent"
reconcile_pair "$ENTRY_ID" "$EXIT_ID" || die "reconcile #3 (idempotent) failed"
[ "$(status "$ENTRY_ID")" = active ] || die "entry not active after idempotent reconcile"
vless_probe "$U2_UUID" "$U2_SID" | verified || die "user2 broke after idempotent reconcile"
echo "    idempotent: still active, user2 tunnels, user1 dead"

# ---------------------------------------------------------------------------
step "negative: broken transport leaves the pair provisioning"
_bad_probe(){ jq -cn '[{type:"vless-reality",authenticated_http:true,exit_ip_verified:true}]'; }  # only 1 distinct
RECONCILE_PROBE_CMD=_bad_probe reconcile_pair "$ENTRY_ID" "$EXIT_ID" && die "reconcile passed with <2 transports"
[ "$(status "$ENTRY_ID")" = provisioning ] && [ "$(status "$EXIT_ID")" = provisioning ] || die "pair not provisioning after transport failure (entry=$(status "$ENTRY_ID") exit=$(status "$EXIT_ID"))"
echo "    both provisioning on transport failure"

step "negative: R1/R2 race leaves the pair provisioning"
# apply hook bans user2 mid-converge so R2 != R1
_racing_apply(){ hook_apply || return 1; "${CURL[@]}" -X POST "$CP/v1/subscriptions/$U2_SUB/cancel" -H "Authorization: Bearer $ADMIN" >/dev/null; }
RECONCILE_APPLY_CMD=_racing_apply reconcile_pair "$ENTRY_ID" "$EXIT_ID" && die "reconcile activated despite R1/R2 race"
[ "$(status "$ENTRY_ID")" = provisioning ] && [ "$(status "$EXIT_ID")" = provisioning ] || die "pair not provisioning after R1/R2 race"
echo "    both provisioning on R1/R2 race"

echo
echo "PASS: control-plane ban -> revision change -> reconcile resyncs the node; user1 removed, user2 keeps both transports; idempotent; failures fail-closed to provisioning"
