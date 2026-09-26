#!/usr/bin/env bash
# user-removal.sh — proves ban/removal after a REAL converge+restart (step 6), not
# just a UUID disappearing from a rendered file. VLESS-REALITY and Hysteria2 both
# use personal credentials, so one revoked user loses both transports while the
# other keeps both.
#
# Topology: client(user) --[vless|hy2]--> ENTRY --[ss2022]--> EXIT --> echo.
# The ENTRY's REALITY allow-list is (re)rendered and the container restarted to
# simulate a converge. Asserts:
#   - both users tunnel initially;
#   - user2 has BOTH transports (vless + hy2);
#   - after banning user1 (remove from snapshot) + restart:
#       user1 fails both transports, user2 keeps both;
#   - a second identical full-sync is idempotent (same result).
#
# The REALITY camouflage origin is a local Caddy TLS 1.3 fixture in the private
# test network. The test does not need a production domain or external network.
set -euo pipefail
cd "$(dirname "$0")/../.."
# shellcheck source=/dev/null
source infra/scripts/lib.sh
# shellcheck source=/dev/null
source infra/scripts/probe.sh

RUN_TAG="cvrm-$$-$(openssl rand -hex 3)"
NET="${RUN_TAG}-net"
# shellcheck source=../../infra/versions.sh
source infra/versions.sh
IMG="ghcr.io/sagernet/sing-box:v${SINGBOX_VERSION}"
ECHO="${RUN_TAG}-echo" ENTRY="${RUN_TAG}-entry" EXIT="${RUN_TAG}-exit"
CLIENT="${RUN_TAG}-client" MIMICRY="${RUN_TAG}-mimicry"
SOCKS_PORT="$(python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
)"
WORK="$(mktemp -d "$(pwd)/test/e2e/.rm.XXXXXX")"
export PROBE_IMG="$IMG" PROBE_NET="$NET" PROBE_CLIENT="$CLIENT"

step() { echo "==> $*"; }
teardown() { docker rm -f "$ECHO" "$ENTRY" "$EXIT" "$CLIENT" "$MIMICRY" >/dev/null 2>&1 || true; docker network rm "$NET" >/dev/null 2>&1 || true; }
cleanup() { teardown; rm -rf "$WORK"; }
trap cleanup EXIT
with_timeout() { local s="$1"; shift; ( "$@" ) & local p=$!; ( sleep "$s"; kill -9 "$p" 2>/dev/null ) & local w=$!; local rc=0; wait "$p" 2>/dev/null || rc=$?; kill "$w" 2>/dev/null; wait "$w" 2>/dev/null || true; return "$rc"; }

for t in docker jq openssl python3; do command -v "$t" >/dev/null || die "missing tool: $t"; done
for image in "$IMG" caddy:2.11.4-alpine traefik/whoami:latest; do
  docker image inspect "$image" >/dev/null 2>&1 || die "required local Docker image missing: $image"
done
REALITY_SERVER_NAME="mimicry.test"
DEST_HOST="$MIMICRY"; DEST_PORT=443

teardown; docker network create "$NET" >/dev/null
openssl req -x509 -nodes -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 \
  -keyout "$WORK/mimicry.key" -out "$WORK/mimicry.crt" \
  -subj "/CN=${REALITY_SERVER_NAME}" -addext "subjectAltName=DNS:${REALITY_SERVER_NAME}" \
  -days 1 >/dev/null 2>&1
cat >"$WORK/Caddyfile" <<'EOF'
:443 {
  tls /fixtures/mimicry.crt /fixtures/mimicry.key
  respond "local TLS origin"
}
EOF
docker run -d --name "$MIMICRY" --network "$NET" --user 0:0 -p 127.0.0.1::443 \
  -v "$WORK/Caddyfile:/etc/caddy/Caddyfile:ro" \
  -v "$WORK/mimicry.crt:/fixtures/mimicry.crt:ro" \
  -v "$WORK/mimicry.key:/fixtures/mimicry.key:ro" \
  caddy:2.11.4-alpine >/dev/null
MIMICRY_PORT="$(docker port "$MIMICRY" 443/tcp | awk -F: 'NR==1 {print $NF}')"
step "preflight: local mimicry fixture speaks TLS 1.3"
tls13() { echo | openssl s_client -connect "127.0.0.1:${MIMICRY_PORT}" -servername "$REALITY_SERVER_NAME" -tls1_3 2>/dev/null | grep -q TLSv1.3; }
fixture_ready=false
for _ in $(seq 1 20); do
  if with_timeout 2 tls13; then fixture_ready=true; break; fi
  sleep 0.2
done
if [ "$fixture_ready" != true ]; then
  docker logs "$MIMICRY" >&2 || true
  die "local mimicry fixture did not negotiate TLS 1.3"
fi


# credentials
U1="$(docker run --rm "$IMG" generate uuid)"; S1="$(openssl rand -hex 8)"
U2="$(docker run --rm "$IMG" generate uuid)"; S2="$(openssl rand -hex 8)"
KP="$(docker run --rm "$IMG" generate reality-keypair)"
RPRIV="$(awk '/PrivateKey/{print $2}' <<<"$KP")"; RPUB="$(awk '/PublicKey/{print $2}' <<<"$KP")"
H1="$(openssl rand -hex 16)"; H2="$(openssl rand -hex 16)"; PSK="$(openssl rand -base64 32)"

# echo + exit
docker run -d --name "$ECHO" --network "$NET" traefik/whoami >/dev/null; sleep 1
jq -n --arg psk "$PSK" '{log:{level:"warn"},inbounds:[{type:"shadowsocks",tag:"ss-in",listen:"::",listen_port:8388,method:"2022-blake3-aes-256-gcm",password:$psk}],outbounds:[{type:"direct",tag:"direct"}]}' > "$WORK/exit.json"
docker run -d --name "$EXIT" --network "$NET" -v "$WORK/exit.json:/c.json:ro" "$IMG" run -c /c.json >/dev/null; sleep 1
EXIT_IP="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$EXIT")"
openssl req -x509 -nodes -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -keyout "$WORK/hy2.key" -out "$WORK/hy2.crt" -subj "/CN=${REALITY_SERVER_NAME}" -days 3 >/dev/null 2>&1

# render_entry <users-json-array> - personal credentials for both transports.
render_entry() {
  local users="$1"
  jq -n --argjson users "$users" --arg priv "$RPRIV" --arg dh "$DEST_HOST" --argjson dp "$DEST_PORT" \
    --arg sni "$REALITY_SERVER_NAME" --arg psk "$PSK" --arg exip "$EXIT_IP" '{
    log:{level:"warn"},
    inbounds:[
      {type:"vless",tag:"vless-in",listen:"::",listen_port:443,
       users:($users|map({uuid:.uuid,flow:"xtls-rprx-vision"})),
       tls:{enabled:true,server_name:$sni,reality:{enabled:true,handshake:{server:$dh,server_port:$dp},private_key:$priv,short_id:($users|map(.short_id))}}},
      {type:"hysteria2",tag:"hy2-in",listen:"::",listen_port:8443,users:($users|map({password:.hysteria2_password})),
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
  local password="$1"
  jq -n --arg s "$ENTRY_IP" --arg pw "$password" --arg sni "$REALITY_SERVER_NAME" '{log:{level:"warn"},
    inbounds:[{type:"mixed",tag:"in",listen:"0.0.0.0",listen_port:1080}],
    outbounds:[{type:"hysteria2",tag:"hy2-out",server:$s,server_port:8443,password:$pw,tls:{enabled:true,server_name:$sni,insecure:true,alpn:["h3"]}},{type:"direct",tag:"direct"}],
    route:{final:"hy2-out"}}' > "$WORK/client.json"
  probe_transport hysteria2 "$WORK/client.json" "$SOCKS_PORT" "$ECHO" "$EXIT_IP"
}
verified() { jq -e '.authenticated_http==true and .exit_ip_verified==true' >/dev/null; }

# --- initial: both users allow-listed ----------------------------------------
step "converge entry with BOTH users allow-listed"
render_entry "$(jq -cn --arg u1 "$U1" --arg s1 "$S1" --arg h1 "$H1" --arg u2 "$U2" --arg s2 "$S2" --arg h2 "$H2" '[{uuid:$u1,short_id:$s1,hysteria2_password:$h1},{uuid:$u2,short_id:$s2,hysteria2_password:$h2}]')"
converge_entry
vless_probe "$U1" "$S1" | verified || die "user1 vless should tunnel initially"
vless_probe "$U2" "$S2" | verified || die "user2 vless should tunnel initially"
hy2_probe "$H1" | verified || die "user1 hy2 should tunnel initially"
hy2_probe "$H2" | verified || die "user2 hy2 should tunnel initially"
echo "    both users tunnel; user2 has vless + hysteria2"

# --- ban user1: remove from the allow-list, re-converge ----------------------
step "ban user1 -> re-render allow-list (user2 only) + restart"
render_entry "$(jq -cn --arg u "$U2" --arg s "$S2" --arg h "$H2" '[{uuid:$u,short_id:$s,hysteria2_password:$h}]')"
converge_entry
vless_probe "$U1" "$S1" | verified && die "BANNED user1 still tunnels vless" || echo "    user1 vless is dead (removed from access snapshot)"
hy2_probe "$H1" | verified && die "BANNED user1 still tunnels hysteria2" || echo "    user1 hysteria2 is dead"
vless_probe "$U2" "$S2" | verified || die "user2 vless must still tunnel"
hy2_probe "$H2" | verified || die "user2 hy2 must still tunnel (both transports remain)"
echo "    user2 still tunnels on BOTH transports"

# --- idempotent re-sync -------------------------------------------------------
step "re-run the same full-sync (idempotent)"
render_entry "$(jq -cn --arg u "$U2" --arg s "$S2" --arg h "$H2" '[{uuid:$u,short_id:$s,hysteria2_password:$h}]')"
converge_entry
vless_probe "$U1" "$S1" | verified && die "user1 came back after re-sync" || true
vless_probe "$U2" "$S2" | verified || die "user2 broke after re-sync"
hy2_probe "$H2" | verified || die "user2 hysteria2 broke after re-sync"
echo "    re-sync stable: user1 dead, user2 alive"

echo
echo "PASS: ban removes user1 from VLESS and Hysteria2 after converge+restart; user2 keeps both transports; re-sync idempotent"
