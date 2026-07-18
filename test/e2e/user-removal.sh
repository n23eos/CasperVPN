#!/usr/bin/env bash
# user-removal.sh — proves ban/removal after a REAL converge+restart (step 6), not
# just a UUID disappearing from a rendered file. Per-user isolation is enforced by
# VLESS-REALITY (uuid + short_id); hysteria2/ss2022 carry a shared node credential
# (documented isolation boundary in CLAUDE.md), so removal cuts a user's REALITY
# access while both transports stay available to the remaining user.
#
# Topology: client(user) --[vless|hy2]--> ENTRY --[ss2022]--> EXIT --> echo.
# The ENTRY's REALITY allow-list is (re)rendered and the container restarted to
# simulate a converge. Asserts:
#   - both users tunnel initially;
#   - user2 has BOTH transports (vless + hy2);
#   - after banning user1 (remove from reality_users) + restart:
#       user1 vless FAILS, user2 vless still tunnels, user2 hy2 still tunnels;
#   - a second identical full-sync is idempotent (same result).
#
# OPT-IN: REALITY_DEST + REALITY_SERVER_NAME (SKIP without; FAIL on non-TLS-1.3).
set -euo pipefail
cd "$(dirname "$0")/../.."
# shellcheck source=/dev/null
source infra/scripts/lib.sh
# shellcheck source=/dev/null
source infra/scripts/probe.sh

NET=capvpn-rm-net
# shellcheck source=../../infra/versions.sh
source infra/versions.sh
IMG="ghcr.io/sagernet/sing-box:v${SINGBOX_VERSION}"
ECHO=rm-echo ENTRY=rm-entry EXIT=rm-exit CLIENT=rm-client
SOCKS_PORT=21098
WORK="$(mktemp -d "$(pwd)/test/e2e/.rm.XXXXXX")"
export PROBE_IMG="$IMG" PROBE_NET="$NET" PROBE_CLIENT="$CLIENT"

skip() { echo "SKIP: $*" >&2; exit 0; }
step() { echo "==> $*"; }
teardown() { docker rm -f "$ECHO" "$ENTRY" "$EXIT" "$CLIENT" >/dev/null 2>&1 || true; docker network rm "$NET" >/dev/null 2>&1 || true; }
cleanup() { teardown; rm -rf "$WORK"; }
trap cleanup EXIT
with_timeout() { local s="$1"; shift; ( "$@" ) & local p=$!; ( sleep "$s"; kill -9 "$p" 2>/dev/null ) & local w=$!; local rc=0; wait "$p" 2>/dev/null || rc=$?; kill "$w" 2>/dev/null; wait "$w" 2>/dev/null || true; return "$rc"; }

for t in docker jq openssl; do command -v "$t" >/dev/null || die "missing tool: $t"; done
[ -n "${REALITY_DEST:-}" ] && [ -n "${REALITY_SERVER_NAME:-}" ] || skip "REALITY_DEST/REALITY_SERVER_NAME not set"
DEST_HOST="${REALITY_DEST%%:*}"; DEST_PORT="${REALITY_DEST##*:}"; [ "$DEST_PORT" = "$REALITY_DEST" ] && DEST_PORT=443

step "preflight: $REALITY_DEST TLS 1.3"
tls13() { echo | openssl s_client -connect "${DEST_HOST}:${DEST_PORT}" -servername "$REALITY_SERVER_NAME" -tls1_3 2>/dev/null | grep -q TLSv1.3; }
with_timeout 20 tls13 || die "$REALITY_DEST not TLS 1.3 (env set => failure)"

teardown; docker network create "$NET" >/dev/null

# credentials
U1="$(docker run --rm "$IMG" generate uuid)"; S1="$(openssl rand -hex 8)"
U2="$(docker run --rm "$IMG" generate uuid)"; S2="$(openssl rand -hex 8)"
KP="$(docker run --rm "$IMG" generate reality-keypair)"
RPRIV="$(awk '/PrivateKey/{print $2}' <<<"$KP")"; RPUB="$(awk '/PublicKey/{print $2}' <<<"$KP")"
HY2_PW="$(openssl rand -hex 16)"; PSK="$(openssl rand -base64 32)"

# echo + exit
docker run -d --name "$ECHO" --network "$NET" traefik/whoami >/dev/null; sleep 1
jq -n --arg psk "$PSK" '{log:{level:"warn"},inbounds:[{type:"shadowsocks",tag:"ss-in",listen:"::",listen_port:8388,method:"2022-blake3-aes-256-gcm",password:$psk}],outbounds:[{type:"direct",tag:"direct"}]}' > "$WORK/exit.json"
docker run -d --name "$EXIT" --network "$NET" -v "$WORK/exit.json:/c.json:ro" "$IMG" run -c /c.json >/dev/null; sleep 1
EXIT_IP="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$EXIT")"
openssl req -x509 -nodes -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -keyout "$WORK/hy2.key" -out "$WORK/hy2.crt" -subj "/CN=${REALITY_SERVER_NAME}" -days 3 >/dev/null 2>&1

# render_entry <uuids-json-array> <sids-json-array> — the REALITY allow-list.
render_entry() {
  local uuids="$1" sids="$2"
  jq -n --argjson uuids "$uuids" --argjson sids "$sids" --arg priv "$RPRIV" --arg dh "$DEST_HOST" --argjson dp "$DEST_PORT" \
    --arg sni "$REALITY_SERVER_NAME" --arg hp "$HY2_PW" --arg psk "$PSK" --arg exip "$EXIT_IP" '{
    log:{level:"warn"},
    inbounds:[
      {type:"vless",tag:"vless-in",listen:"::",listen_port:443,
       users:($uuids|map({uuid:.,flow:"xtls-rprx-vision"})),
       tls:{enabled:true,server_name:$sni,reality:{enabled:true,handshake:{server:$dh,server_port:$dp},private_key:$priv,short_id:$sids}}},
      {type:"hysteria2",tag:"hy2-in",listen:"::",listen_port:8443,users:[{password:$hp}],
       tls:{enabled:true,alpn:["h3"],certificate_path:"/etc/hy2.crt",key_path:"/etc/hy2.key"}}
    ],
    outbounds:[{type:"shadowsocks",tag:"to-exit",server:$exip,server_port:8388,method:"2022-blake3-aes-256-gcm",password:$psk},{type:"direct",tag:"direct"}],
    route:{final:"to-exit"}}' > "$WORK/entry.json"
}
converge_entry() {  # (re)start the entry from the rendered config — simulates converge+restart
  docker rm -f "$ENTRY" >/dev/null 2>&1 || true
  docker run -d --name "$ENTRY" --network "$NET" \
    -v "$WORK/entry.json:/c.json:ro" -v "$WORK/hy2.crt:/etc/hy2.crt:ro" -v "$WORK/hy2.key:/etc/hy2.key:ro" \
    "$IMG" run -c /c.json >/dev/null
  sleep 2
  ENTRY_IP="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$ENTRY")"
}

# vless_probe <uuid> <sid> -> structured probe result via the shared helper.
vless_probe() {
  local uuid="$1" sid="$2"
  jq -n --arg u "$uuid" --arg sni "$REALITY_SERVER_NAME" --arg pub "$RPUB" --arg sid "$sid" --arg s "$ENTRY_IP" '{log:{level:"warn"},
    inbounds:[{type:"mixed",tag:"in",listen:"0.0.0.0",listen_port:1080}],
    outbounds:[{type:"vless",tag:"vless-out",server:$s,server_port:443,uuid:$u,flow:"xtls-rprx-vision",
      tls:{enabled:true,server_name:$sni,utls:{enabled:true,fingerprint:"chrome"},reality:{enabled:true,public_key:$pub,short_id:$sid}}},{type:"direct",tag:"direct"}],
    route:{final:"vless-out"}}' > "$WORK/client.json"
  probe_transport vless-reality "$WORK/client.json" "$SOCKS_PORT" "$ECHO" "$EXIT_IP"
}
hy2_probe() {
  jq -n --arg s "$ENTRY_IP" --arg pw "$HY2_PW" --arg sni "$REALITY_SERVER_NAME" '{log:{level:"warn"},
    inbounds:[{type:"mixed",tag:"in",listen:"0.0.0.0",listen_port:1080}],
    outbounds:[{type:"hysteria2",tag:"hy2-out",server:$s,server_port:8443,password:$pw,tls:{enabled:true,server_name:$sni,insecure:true,alpn:["h3"]}},{type:"direct",tag:"direct"}],
    route:{final:"hy2-out"}}' > "$WORK/client.json"
  probe_transport hysteria2 "$WORK/client.json" "$SOCKS_PORT" "$ECHO" "$EXIT_IP"
}
verified() { jq -e '.authenticated_http==true and .exit_ip_verified==true' >/dev/null; }

# --- initial: both users allow-listed ----------------------------------------
step "converge entry with BOTH users allow-listed"
render_entry "$(jq -cn --arg a "$U1" --arg b "$U2" '[$a,$b]')" "$(jq -cn --arg a "$S1" --arg b "$S2" '[$a,$b]')"
converge_entry
vless_probe "$U1" "$S1" | verified || die "user1 vless should tunnel initially"
vless_probe "$U2" "$S2" | verified || die "user2 vless should tunnel initially"
hy2_probe   | verified || die "user2 hy2 should tunnel initially"
echo "    both users tunnel; user2 has vless + hysteria2"

# --- ban user1: remove from the allow-list, re-converge ----------------------
step "ban user1 -> re-render allow-list (user2 only) + restart"
render_entry "$(jq -cn --arg b "$U2" '[$b]')" "$(jq -cn --arg b "$S2" '[$b]')"
converge_entry
vless_probe "$U1" "$S1" | verified && die "BANNED user1 still tunnels vless" || echo "    user1 vless is dead (removed from reality_users)"
vless_probe "$U2" "$S2" | verified || die "user2 vless must still tunnel"
hy2_probe   | verified || die "user2 hy2 must still tunnel (both transports remain)"
echo "    user2 still tunnels on BOTH transports"

# --- idempotent re-sync -------------------------------------------------------
step "re-run the same full-sync (idempotent)"
render_entry "$(jq -cn --arg b "$U2" '[$b]')" "$(jq -cn --arg b "$S2" '[$b]')"
converge_entry
vless_probe "$U1" "$S1" | verified && die "user1 came back after re-sync" || true
vless_probe "$U2" "$S2" | verified || die "user2 broke after re-sync"
echo "    re-sync stable: user1 dead, user2 alive"

echo
echo "PASS: ban removes user1's REALITY access after converge+restart; user2 keeps both transports; re-sync idempotent"
