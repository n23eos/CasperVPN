#!/usr/bin/env bash
# transport-probe.sh — protocol-aware transport probe (step 4). Proves that EACH
# client transport counted toward the anti-block >=2 rule can INDEPENDENTLY carry
# an authenticated HTTP request through the entry->exit chain, and that the reply
# confirms egress via the EXIT (not the entry). A TCP dial would prove neither
# (it does not authenticate, and it does not work for UDP hysteria2).
#
# Topology (all on one docker network):
#   client --[vless-reality | hysteria2]--> ENTRY --[shadowsocks-2022]--> EXIT --direct--> echo
# The echo (traefik/whoami) reports RemoteAddr = the peer it saw. Traffic that
# egresses via the EXIT makes the echo report the EXIT's IP; a chain-less entry
# would make it report the ENTRY's IP. So RemoteAddr == EXIT proves exit egress.
#
# OPT-IN: REALITY needs a live TLS 1.3 mimicry target.
#   REALITY_DEST=host:443 REALITY_SERVER_NAME=host test/e2e/transport-probe.sh
# env unset -> SKIP; set but dest not TLS 1.3 -> FAIL.
set -euo pipefail
cd "$(dirname "$0")/../.."

NET=capvpn-probe-net
IMG=ghcr.io/sagernet/sing-box:v1.11.4
ECHO=pb-echo ENTRY=pb-entry EXIT=pb-exit CLIENT=pb-client
SOCKS_PORT=21099
WORK="$(mktemp -d "$(pwd)/test/e2e/.probe.XXXXXX")"

fail() { echo "FAIL: $*" >&2; exit 1; }
skip() { echo "SKIP: $*" >&2; exit 0; }
step() { echo "==> $*"; }
teardown_stack() {
  docker rm -f "$ECHO" "$ENTRY" "$EXIT" "$CLIENT" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
}
cleanup() { teardown_stack; rm -rf "$WORK"; }
trap cleanup EXIT

with_timeout() { local s="$1"; shift; ( "$@" ) & local p=$!; ( sleep "$s"; kill -9 "$p" 2>/dev/null ) & local w=$!; local rc=0; wait "$p" 2>/dev/null || rc=$?; kill "$w" 2>/dev/null; wait "$w" 2>/dev/null || true; return "$rc"; }

for t in docker jq openssl; do command -v "$t" >/dev/null || fail "missing tool: $t"; done
[ -n "${REALITY_DEST:-}" ] && [ -n "${REALITY_SERVER_NAME:-}" ] || skip "REALITY_DEST/REALITY_SERVER_NAME not set"
DEST_HOST="${REALITY_DEST%%:*}"; DEST_PORT="${REALITY_DEST##*:}"; [ "$DEST_PORT" = "$REALITY_DEST" ] && DEST_PORT=443

step "preflight: $REALITY_DEST TLS 1.3"
tls13() { echo | openssl s_client -connect "${DEST_HOST}:${DEST_PORT}" -servername "$REALITY_SERVER_NAME" -tls1_3 2>/dev/null | grep -q TLSv1.3; }
with_timeout 12 tls13 || fail "$REALITY_DEST is not reachable TLS 1.3 (env set => failure)"
nc -z 127.0.0.1 "$SOCKS_PORT" 2>/dev/null && fail "port $SOCKS_PORT busy"

teardown_stack
docker network create "$NET" >/dev/null

# --- credentials ---
UUID="$(docker run --rm "$IMG" generate uuid)"
SID="$(openssl rand -hex 8)"
KP="$(docker run --rm "$IMG" generate reality-keypair)"
RPRIV="$(awk '/PrivateKey/{print $2}' <<<"$KP")"; RPUB="$(awk '/PublicKey/{print $2}' <<<"$KP")"
HY2_PW="$(openssl rand -hex 16)"
PSK="$(openssl rand -base64 32)"     # 32-byte key for 2022-blake3-aes-256-gcm

# --- echo target ---
step "start echo target (reports the peer IP it sees)"
docker run -d --name "$ECHO" --network "$NET" traefik/whoami >/dev/null
sleep 1
EXIT_IP=""  # filled after exit starts

# --- exit: SS2022 inbound -> direct out ---
jq -n --arg psk "$PSK" '{log:{level:"warn"},
  inbounds:[{type:"shadowsocks",tag:"ss-in",listen:"::",listen_port:8388,
    method:"2022-blake3-aes-256-gcm",password:$psk}],
  outbounds:[{type:"direct",tag:"direct"}]}' > "$WORK/exit.json"
step "start EXIT (shadowsocks-2022 in -> direct out)"
docker run -d --name "$EXIT" --network "$NET" -v "$WORK/exit.json:/c.json:ro" "$IMG" run -c /c.json >/dev/null
sleep 1
EXIT_IP="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$EXIT")"
[ -n "$EXIT_IP" ] || fail "no exit IP"

# --- entry: vless + hy2 inbounds -> SS2022 outbound to exit, route.final=to-exit ---
# self-signed hy2 cert (server side)
openssl req -x509 -nodes -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 \
  -keyout "$WORK/hy2.key" -out "$WORK/hy2.crt" -subj "/CN=${REALITY_SERVER_NAME}" -days 3 >/dev/null 2>&1
jq -n --arg uuid "$UUID" --arg priv "$RPRIV" --arg sid "$SID" --arg dh "$DEST_HOST" --argjson dp "$DEST_PORT" \
  --arg sni "$REALITY_SERVER_NAME" --arg hp "$HY2_PW" --arg psk "$PSK" --arg exip "$EXIT_IP" '{
  log:{level:"warn"},
  inbounds:[
    {type:"vless",tag:"vless-in",listen:"::",listen_port:443,users:[{uuid:$uuid,flow:"xtls-rprx-vision"}],
     tls:{enabled:true,server_name:$sni,reality:{enabled:true,handshake:{server:$dh,server_port:$dp},private_key:$priv,short_id:[$sid]}}},
    {type:"hysteria2",tag:"hy2-in",listen:"::",listen_port:8443,users:[{password:$hp}],
     tls:{enabled:true,alpn:["h3"],certificate_path:"/etc/hy2.crt",key_path:"/etc/hy2.key"}}
  ],
  outbounds:[{type:"shadowsocks",tag:"to-exit",server:$exip,server_port:8388,method:"2022-blake3-aes-256-gcm",password:$psk},
             {type:"direct",tag:"direct"}],
  route:{final:"to-exit"}}' > "$WORK/entry.json"
step "start ENTRY (vless+hy2 in -> shadowsocks-2022 out to exit)"
docker run -d --name "$ENTRY" --network "$NET" \
  -v "$WORK/entry.json:/c.json:ro" -v "$WORK/hy2.crt:/etc/hy2.crt:ro" -v "$WORK/hy2.key:/etc/hy2.key:ro" \
  "$IMG" run -c /c.json >/dev/null
sleep 2
ENTRY_IP="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$ENTRY")"

# probe_transport <name> <client-outbound-json> -> 0 if HTTP ok AND egress==EXIT
probe_transport() {
  local name="$1" ob="$2"
  jq -n --argjson ob "$ob" '{log:{level:"warn"},
    inbounds:[{type:"mixed",tag:"in",listen:"0.0.0.0",listen_port:1080}],
    outbounds:[$ob,{type:"direct",tag:"direct"}],
    route:{final:($ob.tag)}}' > "$WORK/client.json"
  docker rm -f "$CLIENT" >/dev/null 2>&1 || true
  docker run -d --name "$CLIENT" --network "$NET" -p "${SOCKS_PORT}:1080" \
    -v "$WORK/client.json:/c.json:ro" "$IMG" run -c /c.json >/dev/null
  sleep 3
  local body remote code
  body="$(curl -sS --max-time 20 --proxy "socks5h://127.0.0.1:${SOCKS_PORT}" "http://${ECHO}/" 2>/dev/null || true)"
  code=$?
  remote="$(printf '%s' "$body" | awk -F'[ :]' '/^RemoteAddr:/{print $3}')"
  docker rm -f "$CLIENT" >/dev/null 2>&1 || true
  if [ -z "$body" ]; then echo "    $name: no HTTP response"; return 1; fi
  if [ "$remote" != "$EXIT_IP" ]; then
    echo "    $name: egress via $remote, expected EXIT $EXIT_IP (not proven through exit)"; return 1
  fi
  echo "    $name: authenticated HTTP ok, egress via EXIT ($remote)"; return 0
}

VLESS_OB="$(jq -n --arg uuid "$UUID" --arg sni "$REALITY_SERVER_NAME" --arg pub "$RPUB" --arg sid "$SID" --arg s "$ENTRY_IP" '{
  type:"vless",tag:"vless-out",server:$s,server_port:443,uuid:$uuid,flow:"xtls-rprx-vision",
  tls:{enabled:true,server_name:$sni,utls:{enabled:true,fingerprint:"chrome"},reality:{enabled:true,public_key:$pub,short_id:$sid}}}')"
HY2_OB="$(jq -n --arg s "$ENTRY_IP" --arg pw "$HY2_PW" --arg sni "$REALITY_SERVER_NAME" '{
  type:"hysteria2",tag:"hy2-out",server:$s,server_port:8443,password:$pw,
  tls:{enabled:true,server_name:$sni,insecure:true,alpn:["h3"]}}')"

step "probe transport #1: vless-reality"
live=0
probe_transport "vless-reality" "$VLESS_OB" && live=$((live+1))
step "probe transport #2: hysteria2"
probe_transport "hysteria2" "$HY2_OB" && live=$((live+1))

step "negative: wrong vless uuid must NOT prove live"
BAD_OB="$(jq --arg u "00000000-0000-0000-0000-0000000000ff" '.uuid=$u|.tag="bad-out"' <<<"$VLESS_OB")"
if probe_transport "vless-wrong-uuid" "$BAD_OB"; then fail "a wrong-credential transport passed the probe"; fi
echo "    wrong-credential transport correctly not live"

step "verdict: need >=2 live client transports (protocol-aware, egress via exit)"
[ "$live" -ge 2 ] || fail "only $live live transport(s), need >=2"

echo
echo "PASS: $live client transports each carry authenticated HTTP through entry->exit (egress via EXIT); wrong creds rejected"
