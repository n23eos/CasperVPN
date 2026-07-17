#!/usr/bin/env bash
# real-node.sh — the honest proof of a REAL REALITY tunnel, end to end, locally.
#
#   CP+billing+subscription -> register a node with its REALITY public key ->
#   create+pay a user -> sync that user's UUID/short-id into the node allow-list
#   -> render+start the node (real x25519 keypair, generated on the node) ->
#   fetch the user's /sub -> start a client sing-box -> curl through the REALITY
#   tunnel and get a real HTTP response. Then re-key (rotation) and prove the
#   fresh public key tunnels while the old one is gone.
#
# The REALITY private key is generated for the node; this test proves the client
# never sees it — it is absent from the client config, /sub, the control-plane API
# responses, and every container log. (It IS handled by this orchestrating script,
# just as a real deploy's Ansible controller handles it: the control host is part
# of the trusted secret boundary. The guarantee is that it never reaches a client
# or an outward-facing surface.) Only the public half flows to CP and clients.
#
# OPT-IN: REALITY needs a live TLS 1.3 mimicry target, which is real external
# network. The mimicry host is NOT baked into the repo (docs/mimicry-domains.md
# intentionally lists none) — you must supply it:
#   REALITY_DEST=host:443 REALITY_SERVER_NAME=host test/e2e/real-node.sh
#
# Semantics: env unset -> SKIP (exit 0). env set but the dest is unreachable or
# not TLS 1.3 -> FAIL (exit 1) — a broken dest must not masquerade as green.
set -euo pipefail

cd "$(dirname "$0")/../.."

PROJECT=caspervpn-realnode
NET="${PROJECT}_default"
if docker compose version >/dev/null 2>&1; then COMPOSE_BIN=(docker compose); else COMPOSE_BIN=(docker-compose); fi
COMPOSE=("${COMPOSE_BIN[@]}" -p "$PROJECT" -f docker-compose.dev.yml -f test/e2e/docker-compose.realnode.yml)
CORE=(postgres control-plane subscription billing)

CP=http://localhost:28081
SUB=http://localhost:28082
BILL=http://localhost:28084
ADMIN_TOKEN=dev-admin-token
MOCK_SECRET=dev-mock-webhook-secret
# sing-box pin: single source of truth (infra/versions.sh), kept equal to the
# ansible role default by test/infra/versions-pin-guard.sh.
# shellcheck source=../../infra/versions.sh
source infra/versions.sh
SINGBOX_IMG="ghcr.io/sagernet/sing-box:v${SINGBOX_VERSION}"
NODE_CT=rn-node
CLIENT_CT=rn-client
CLIENT_SOCKS_PORT=21080
CURL=(curl -sS --max-time 10)

# WORK must live under the repo (Docker Desktop/Colima only bind-mounts $HOME;
# /tmp and /var/folders are outside the VM's virtiofs share and silently mount
# as empty dirs).
WORK="$(mktemp -d "$(pwd)/test/e2e/.realnode.XXXXXX")"
fail() { echo "FAIL: $*" >&2; exit 1; }

# Portable timeout (no coreutils `timeout`/`gtimeout` on stock macOS): run the
# command in the background and kill it if it outlives the deadline.
with_timeout() {
  local secs="$1"; shift
  ( "$@" ) & local pid=$!
  ( sleep "$secs"; kill -9 "$pid" 2>/dev/null ) & local wd=$!
  local rc=0; wait "$pid" 2>/dev/null || rc=$?
  kill "$wd" 2>/dev/null; wait "$wd" 2>/dev/null || true
  return "$rc"
}
skip() { echo "SKIP: $*" >&2; exit 0; }
step() { echo "==> $*"; }

teardown_stack() {
  docker rm -f "$NODE_CT" "$CLIENT_CT" >/dev/null 2>&1 || true
  "${COMPOSE[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
}
cleanup() { teardown_stack; rm -rf "$WORK"; }
trap cleanup EXIT

# --- 0. Preconditions -------------------------------------------------------
for t in docker jq openssl nc base64; do command -v "$t" >/dev/null || fail "missing tool: $t"; done
[ -n "${REALITY_DEST:-}" ] && [ -n "${REALITY_SERVER_NAME:-}" ] \
  || skip "REALITY_DEST and REALITY_SERVER_NAME not set — supply a live TLS 1.3 mimicry target to run"
DEST_HOST="${REALITY_DEST%%:*}"; DEST_PORT="${REALITY_DEST##*:}"; [ "$DEST_PORT" = "$REALITY_DEST" ] && DEST_PORT=443

step "preflight: $REALITY_DEST must speak TLS 1.3"
# Bounded so a non-TLS or unreachable dest FAILs in seconds instead of hanging on
# openssl's default TCP/handshake timeout.
tls13_ok() {
  echo | openssl s_client -connect "${DEST_HOST}:${DEST_PORT}" -servername "$REALITY_SERVER_NAME" -tls1_3 2>/dev/null \
    | grep -q "TLSv1.3"
}
with_timeout 12 tls13_ok \
  || fail "$REALITY_DEST did not negotiate TLS 1.3 within 12s (env is set — failure, not skip)"

for p in 25432 28081 28082 28084 "$CLIENT_SOCKS_PORT"; do
  nc -z 127.0.0.1 "$p" 2>/dev/null && fail "port $p busy"
done

# --- 1. Core services -------------------------------------------------------
step "clean start"
teardown_stack
"${COMPOSE[@]}" build -q "${CORE[@]}"
"${COMPOSE[@]}" up -d postgres
for i in $(seq 1 30); do "${COMPOSE[@]}" exec -T postgres pg_isready -U caspervpn >/dev/null 2>&1 && break; [ "$i" = 30 ] && fail "pg timeout"; sleep 2; done
"${COMPOSE[@]}" exec -T postgres psql -q -v ON_ERROR_STOP=1 -U caspervpn -d caspervpn < services/billing/internal/store/schema.sql
"${COMPOSE[@]}" exec -T postgres psql -q -v ON_ERROR_STOP=1 -U caspervpn -d caspervpn < services/subscription/internal/controlplane/schema.sql
"${COMPOSE[@]}" up -d "${CORE[@]}"
for url in "$CP/healthz" "$SUB/healthz" "$BILL/healthz"; do
  for i in $(seq 1 30); do "${CURL[@]}" -o /dev/null "$url" 2>/dev/null && break; [ "$i" = 30 ] && fail "$url unhealthy"; sleep 2; done
done

# --- 2. Node REALITY keypair (generated in the node's own image) ------------
step "generate node REALITY keypair (private key stays local to the node)"
KP="$(docker run --rm "$SINGBOX_IMG" generate reality-keypair)"
NODE_PRIV="$(awk '/PrivateKey/{print $2}' <<<"$KP")"
NODE_PUB="$(awk '/PublicKey/{print $2}' <<<"$KP")"
[ -n "$NODE_PRIV" ] && [ -n "$NODE_PUB" ] || fail "keypair parse failed"
umask 077; printf 'private_key=%s\npublic_key=%s\n' "$NODE_PRIV" "$NODE_PUB" > "$WORK/reality.key"

# --- 3. Register the node with its REAL public key (shared upsert helper) ----
step "register node in control-plane via reality_sync.sh (real pubkey)"
# shellcheck source=/dev/null
source infra/scripts/lib.sh; source infra/scripts/control_plane.sh; source infra/scripts/reality_sync.sh
export CONTROL_PLANE_URL="$CP" CONTROL_PLANE_TOKEN="$ADMIN_TOKEN"
NODE_ID=rn-node-1 NODE_ROLE=entry NODE_STATUS=active PROVIDER=local CLOUD=local REGION=RU-MOW \
  ENTRY_IP="$NODE_CT" EPHEMERAL_ENTRY_IP=false \
  REALITY_PUBLIC_KEY="$NODE_PUB" REALITY_SERVER_NAMES="$REALITY_SERVER_NAME" REALITY_DEST="$REALITY_DEST" \
  REALITY_TAG=vless-reality-in REALITY_PORT=443 \
  reality_sync_node

# --- 4. Create + pay a user, read their personal UUID / short-id -------------
step "create + pay a user"
USER_JSON=$("${CURL[@]}" -X POST "$CP/v1/users" -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' -d '{"telegram_id": 700700}')
USER_ID=$(jq -re .id <<<"$USER_JSON"); USER_UUID=$(jq -re .uuid <<<"$USER_JSON")
USER_SID=$(jq -re .reality_short_id <<<"$USER_JSON")
INV=$("${CURL[@]}" -X POST "$BILL/v1/invoices" -H 'Content-Type: application/json' \
  -d "{\"anon_user_id\":\"$USER_ID\",\"plan\":\"basic\",\"currency\":\"BTC\"}")
INV_ID=$(jq -re .invoice_id <<<"$INV"); INV_AMT=$(jq -re .amount <<<"$INV"); INV_CUR=$(jq -re .currency <<<"$INV")
TS=$(date -u +%Y-%m-%dT%H:%M:%SZ)
BODY=$(jq -cn --arg e "rn-$INV_ID" --arg i "$INV_ID" --arg a "$INV_AMT" --arg c "$INV_CUR" --arg t "$TS" \
  '{external_id:$e,invoice_id:$i,status:"settled",amount:$a,currency:$c,confirmations:6,ts:$t}')
SIG=$(printf '%s' "$BODY" | openssl dgst -sha256 -hmac "$MOCK_SECRET" -hex | awk '{print $NF}')
[ "$("${CURL[@]}" -o /dev/null -w '%{http_code}' -X POST "$BILL/v1/webhooks/mock" -H "X-Signature: $SIG" \
  -H 'Content-Type: application/json' --data-binary "$BODY")" = 200 ] || fail "webhook not 200"
echo "    user $USER_ID uuid=$USER_UUID short_id=$USER_SID"

# --- 5a. Sync the user's allow-list onto the node ---------------------------
# Two flows: user short-id into the CP node pool (keeps CP truthful) AND into the
# running node's server config (what actually admits the handshake).
step "sync user allow-list -> control-plane node pool + node server config"
NODE_ID=rn-node-1 NODE_ROLE=entry NODE_STATUS=active PROVIDER=local CLOUD=local REGION=RU-MOW \
  ENTRY_IP="$NODE_CT" EPHEMERAL_ENTRY_IP=false \
  REALITY_PUBLIC_KEY="$NODE_PUB" REALITY_SHORT_IDS="$USER_SID" \
  REALITY_SERVER_NAMES="$REALITY_SERVER_NAME" REALITY_DEST="$REALITY_DEST" \
  REALITY_TAG=vless-reality-in REALITY_PORT=443 \
  reality_sync_node

render_node_config() {  # $1=private_key  -> writes $WORK/node.json
  jq -n --arg uuid "$USER_UUID" --arg sni "$REALITY_SERVER_NAME" --arg dh "$DEST_HOST" \
    --argjson dp "$DEST_PORT" --arg priv "$1" --arg sid "$USER_SID" '{
      log: {level:"warn"},
      inbounds: [{type:"vless", tag:"vless-in", listen:"::", listen_port:443,
        users:[{uuid:$uuid, flow:"xtls-rprx-vision"}],
        tls:{enabled:true, server_name:$sni,
          reality:{enabled:true, handshake:{server:$dh, server_port:$dp},
            private_key:$priv, short_id:[$sid]}}}],
      outbounds: [{type:"direct", tag:"direct"}]
    }' > "$WORK/node.json"
}

start_node() {  # (re)start the node container from $WORK/node.json
  docker rm -f "$NODE_CT" >/dev/null 2>&1 || true
  docker run -d --name "$NODE_CT" --network "$NET" \
    -v "$WORK/node.json:/etc/sing-box/config.json:ro" "$SINGBOX_IMG" run -c /etc/sing-box/config.json >/dev/null
  for i in $(seq 1 15); do docker exec "$NODE_CT" true 2>/dev/null && break; sleep 1; done
}

render_node_config "$NODE_PRIV"; start_node
step "node up (uuid in allow-list, short-id in pool)"

# --- 5. Fetch /sub, build a client, tunnel through REALITY -------------------
tunnel_check() {  # $1=expected pubkey ; returns 0 on HTTP success through tunnel
  local want_pub="$1"
  local token sub tag
  token=$("${CURL[@]}" -X POST -H "Authorization: Bearer $ADMIN_TOKEN" "$CP/v1/subscriptions/$SUB_ID/rotate-token" | jq -re .token)
  sub=$("${CURL[@]}" "$SUB/sub/$token?format=singbox")
  printf '%s' "$sub" > "$WORK/sub.json"
  # The bundle may carry other nodes (dev seed). Select OUR node's outbound by its
  # public key, and build a client that uses only that one — this proves the real
  # keyed node tunnels, not some seed entry.
  jq -e --arg pub "$want_pub" 'any(.outbounds[]?; .type=="vless" and .tls.reality.public_key==$pub)' <<<"$sub" >/dev/null \
    || { echo "our pubkey ${want_pub:0:12}... not present in /sub" >&2; return 2; }
  tag=$(jq -r --arg pub "$want_pub" 'first(.outbounds[]|select(.type=="vless" and .tls.reality.public_key==$pub)).tag' <<<"$sub")
  jq --arg final "$tag" --arg pub "$want_pub" '{
      log:{level:"warn"},
      inbounds:[{type:"mixed", tag:"in", listen:"0.0.0.0", listen_port:1080}],
      outbounds: ([.outbounds[]|select(.type=="vless" and .tls.reality.public_key==$pub)] + [{type:"direct",tag:"direct"}]),
      route:{final:$final}
    }' <<<"$sub" > "$WORK/client.json"
  docker rm -f "$CLIENT_CT" >/dev/null 2>&1 || true
  docker run -d --name "$CLIENT_CT" --network "$NET" -p "${CLIENT_SOCKS_PORT}:1080" \
    -v "$WORK/client.json:/etc/sing-box/config.json:ro" "$SINGBOX_IMG" run -c /etc/sing-box/config.json >/dev/null
  sleep 4
  local code
  code=$(curl -sS --max-time 25 -o /dev/null -w '%{http_code}' \
    --proxy "socks5h://127.0.0.1:${CLIENT_SOCKS_PORT}" "https://${REALITY_SERVER_NAME}/" || echo 000)
  echo "    tunnel HTTP $code (via pbk ${want_pub:0:12}...)"
  case "$code" in 2??|3??) return 0;; *) return 1;; esac
}

SUB_ID=$("${CURL[@]}" -H "Authorization: Bearer $ADMIN_TOKEN" "$CP/v1/users/$USER_ID" | jq -re .subscription_id)
step "tunnel #1 through the freshly keyed node"
tunnel_check "$NODE_PUB" || fail "REALITY tunnel did not carry HTTP (initial key)"

# --- 7. Secret hygiene: private key must appear NOWHERE outward --------------
step "secret hygiene: node private key must not leak"
LEAK=0
grep -q "$NODE_PRIV" "$WORK/client.json" "$WORK/sub.json" && { echo "  LEAK in client/sub config" >&2; LEAK=1; }
for path in "/v1/users/$USER_ID" "/v1/nodes/rn-node-1" "/v1/users/$USER_ID/subscription-set"; do
  "${CURL[@]}" -H "Authorization: Bearer $ADMIN_TOKEN" "$CP$path" | grep -q "$NODE_PRIV" && { echo "  LEAK in $path" >&2; LEAK=1; }
done
{ "${COMPOSE[@]}" logs --no-color 2>&1; docker logs "$NODE_CT" 2>&1; docker logs "$CLIENT_CT" 2>&1; } | grep -q "$NODE_PRIV" \
  && { echo "  LEAK in container logs" >&2; LEAK=1; }
[ "$LEAK" = 0 ] || fail "private key leaked outward"
# sanity: the PUBLIC key IS present in the client config (proves grep works)
grep -q "$NODE_PUB" "$WORK/sub.json" || fail "public key missing from client config (hygiene check is inert)"
echo "    private key absent from every outward surface; public key present"

# --- 8. Rotation: new key tunnels, old key is gone --------------------------
step "rotate node key; new pubkey must tunnel, old must be absent"
OLD_PUB="$NODE_PUB"
KP2="$(docker run --rm "$SINGBOX_IMG" generate reality-keypair)"
NODE_PRIV="$(awk '/PrivateKey/{print $2}' <<<"$KP2")"; NODE_PUB="$(awk '/PublicKey/{print $2}' <<<"$KP2")"
printf 'private_key=%s\npublic_key=%s\n' "$NODE_PRIV" "$NODE_PUB" > "$WORK/reality.key"
NODE_ID=rn-node-1 NODE_ROLE=entry NODE_STATUS=active PROVIDER=local CLOUD=local REGION=RU-MOW \
  ENTRY_IP="$NODE_CT" EPHEMERAL_ENTRY_IP=false \
  REALITY_PUBLIC_KEY="$NODE_PUB" REALITY_SHORT_IDS="$USER_SID" \
  REALITY_SERVER_NAMES="$REALITY_SERVER_NAME" REALITY_DEST="$REALITY_DEST" \
  REALITY_TAG=vless-reality-in REALITY_PORT=443 \
  reality_sync_node
render_node_config "$NODE_PRIV"; start_node
tunnel_check "$NODE_PUB" || fail "REALITY tunnel did not carry HTTP after rotation"
grep -q "$OLD_PUB" "$WORK/sub.json" && fail "old public key still present in /sub after rotation"
echo "    rotation OK: fresh pubkey tunnels, old pubkey gone"

echo
echo "PASS: real REALITY tunnel — user paid -> allow-listed -> tunneled HTTP; rotation coherent; private key never reached a client, /sub, the API, or any log"
