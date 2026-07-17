#!/usr/bin/env bash
# hy2-rotation-preserve.sh — regression for the Hysteria2 password surviving a VM
# REPLACEMENT (terraform -replace wipes the on-VM state file).
#
# The fix makes the control-plane the durable source: node_rotate.sh reads the
# current hysteria2 password back from the control-plane and feeds it forward, so
# a fresh VM serves the SAME password the control-plane still advertises. This
# test drives that at the CP level (no VM/ansible): register a node with password
# P1, then simulate node_rotate reading P1 from the control-plane and re-syncing
# with a fresh REALITY key — and asserts the control-plane still holds P1.
set -euo pipefail
cd "$(dirname "$0")/../.."

PROJECT=caspervpn-hy2rot
if docker compose version >/dev/null 2>&1; then COMPOSE_BIN=(docker compose); else COMPOSE_BIN=(docker-compose); fi
COMPOSE=("${COMPOSE_BIN[@]}" -p "$PROJECT" -f docker-compose.dev.yml -f test/e2e/docker-compose.hy2rot.yml)
CP=http://localhost:48081
ADMIN_TOKEN=dev-admin-token
CURL=(curl -sS --max-time 10)

fail() { echo "FAIL: $*" >&2; exit 1; }
step() { echo "==> $*"; }
cleanup() { "${COMPOSE[@]}" down -v --remove-orphans >/dev/null 2>&1 || true; }
trap cleanup EXIT

for t in docker jq; do command -v "$t" >/dev/null || fail "missing tool: $t"; done
nc -z 127.0.0.1 48081 2>/dev/null && fail "port 48081 busy"

step "clean start (postgres + control-plane)"
cleanup
"${COMPOSE[@]}" build -q control-plane
"${COMPOSE[@]}" up -d postgres
for i in $(seq 1 30); do "${COMPOSE[@]}" exec -T postgres pg_isready -U caspervpn >/dev/null 2>&1 && break; [ "$i" = 30 ] && fail "pg timeout"; sleep 2; done
"${COMPOSE[@]}" up -d control-plane
for i in $(seq 1 30); do "${CURL[@]}" -o /dev/null "$CP/healthz" 2>/dev/null && break; [ "$i" = 30 ] && fail "cp unhealthy"; sleep 2; done

# shellcheck source=/dev/null
source infra/scripts/lib.sh; source infra/scripts/control_plane.sh; source infra/scripts/reality_sync.sh
export CONTROL_PLANE_URL="$CP" CONTROL_PLANE_TOKEN="$ADMIN_TOKEN"

NID=nd-hy2rot
P1=hy2-durable-password-P1

step "register node with hysteria2 password P1"
NODE_ID="$NID" NODE_ROLE=entry NODE_STATUS=provisioning PROVIDER=local CLOUD=local REGION=RU-MOW \
  ENTRY_IP=1.1.1.1 EPHEMERAL_ENTRY_IP=false \
  REALITY_PUBLIC_KEY=PUB1 REALITY_SHORT_IDS=1111111111111111 \
  REALITY_SERVER_NAMES=www.example.test REALITY_DEST=www.example.test:443 \
  HY2_PASSWORD="$P1" HY2_SNI=cdn.example.test HY2_INSECURE=false \
  reality_sync_node

step "simulate node_rotate: read the durable hysteria2 password back from CP"
CUR="$(cp_get_node "$NID")"
DURABLE_PW="$(printf '%s' "$CUR" | jq -r 'first(.transports[]? | select(.type=="hysteria2")).hysteria2.password // empty')"
DURABLE_SNI="$(printf '%s' "$CUR" | jq -r 'first(.transports[]? | select(.type=="hysteria2")).hysteria2.sni // empty')"
[ "$DURABLE_PW" = "$P1" ] || fail "rotation could not read the durable password from CP (got '$DURABLE_PW')"

step "rotation re-sync: fresh REALITY key, hysteria2 password fed forward from CP"
NODE_ID="$NID" NODE_ROLE=entry NODE_STATUS=provisioning PROVIDER=local CLOUD=local REGION=RU-MOW \
  ENTRY_IP=2.2.2.2 EPHEMERAL_ENTRY_IP=true \
  REALITY_PUBLIC_KEY=PUB2 REALITY_SHORT_IDS=2222222222222222 \
  REALITY_SERVER_NAMES=www.example.test REALITY_DEST=www.example.test:443 \
  HY2_PASSWORD="$DURABLE_PW" HY2_SNI="$DURABLE_SNI" HY2_INSECURE=false \
  reality_sync_node

step "assert the control-plane still holds P1 (not lost, not changed)"
N="$(cp_get_node "$NID")"
check() { jq -e "$1" <<<"$N" >/dev/null || fail "$2 -- node: $(jq -c . <<<"$N")"; }
check '[.transports[]|select(.type=="hysteria2")][0].hysteria2.password == "'"$P1"'"' "hysteria2 password lost/changed after rotation"
check '[.transports[]|select(.type=="vless-reality")][0].vless_reality.public_key == "PUB2"' "REALITY key was not rotated"
check '([.transports[]|select(.enabled and (.type=="vless-reality" or .type=="hysteria2"))]|length) >= 2' "fewer than 2 client transports after rotation"

echo
echo "PASS: hysteria2 password survives a VM replacement (control-plane is the durable source)"
