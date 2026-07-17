#!/usr/bin/env bash
# reality-sync-merge.sh — regression for reality_sync_node's upsert MERGE path.
#
# Proves that re-running the helper against an existing Node (the rotation case):
#   - updates the mutable fields (entry_ip, ephemeral_entry_ip, public_key),
#   - UNIONs the short-id pool (never collapses per-user isolation),
#   - PRESERVES other transports already on the node.
#
# Only postgres + control-plane are needed (no sing-box, no external network), so
# this runs fast and is not opt-in.
set -euo pipefail
cd "$(dirname "$0")/../.."

PROJECT=caspervpn-merge
if docker compose version >/dev/null 2>&1; then COMPOSE_BIN=(docker compose); else COMPOSE_BIN=(docker-compose); fi
COMPOSE=("${COMPOSE_BIN[@]}" -p "$PROJECT" -f docker-compose.dev.yml -f test/e2e/docker-compose.merge.yml)
CP=http://localhost:38081
ADMIN_TOKEN=dev-admin-token
CURL=(curl -sS --max-time 10)

fail() { echo "FAIL: $*" >&2; exit 1; }
step() { echo "==> $*"; }
cleanup() { "${COMPOSE[@]}" down -v --remove-orphans >/dev/null 2>&1 || true; }
trap cleanup EXIT

for t in docker jq; do command -v "$t" >/dev/null || fail "missing tool: $t"; done
nc -z 127.0.0.1 38081 2>/dev/null && fail "port 38081 busy"

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

NID=nd-merge-1
step "create node: entry_ip=1.1.1.1, pub=PUB_OLD, short-id S_OLD"
NODE_ID="$NID" NODE_ROLE=entry NODE_STATUS=active PROVIDER=local CLOUD=local REGION=RU-MOW \
  ENTRY_IP=1.1.1.1 EPHEMERAL_ENTRY_IP=false \
  REALITY_PUBLIC_KEY=PUB_OLD REALITY_SHORT_IDS=1111111111111111 \
  REALITY_SERVER_NAMES=www.example.test REALITY_DEST=www.example.test:443 \
  reality_sync_node

step "add a second transport (shadowsocks-2022) to simulate other transports"
CUR="$(cp_get_node "$NID")"
WITH_SS="$(jq '.transports += [{tag:"ss-in", type:"shadowsocks-2022", version:"v1", port:8388,
  enabled:true, priority:1, shadowsocks_2022:{method:"2022-blake3-aes-256-gcm", psk:"dGVzdHBzaw=="}}]' <<<"$CUR")"
cp_patch_node "$NID" "$WITH_SS"

step "merge (rotation): entry_ip=2.2.2.2, pub=PUB_NEW, short-id S_NEW"
NODE_ID="$NID" NODE_ROLE=entry NODE_STATUS=active PROVIDER=local CLOUD=local REGION=RU-MOW \
  ENTRY_IP=2.2.2.2 EPHEMERAL_ENTRY_IP=true \
  REALITY_PUBLIC_KEY=PUB_NEW REALITY_SHORT_IDS=2222222222222222 \
  REALITY_SERVER_NAMES=www.example.test REALITY_DEST=www.example.test:443 \
  reality_sync_node

step "assert the merge result"
N="$(cp_get_node "$NID")"
check() { jq -e "$1" <<<"$N" >/dev/null || fail "$2 -- node: $(jq -c . <<<"$N")"; }
check '.entry_ip == "2.2.2.2"'                                                   "entry_ip not updated to new IP"
check '.ephemeral_entry_ip == true'                                             "ephemeral_entry_ip not updated"
check '[.transports[]|select(.type=="vless-reality")][0].vless_reality.public_key == "PUB_NEW"' "public_key not rotated"
check '[.transports[]|select(.type=="vless-reality")][0].vless_reality.short_ids | (index("1111111111111111") and index("2222222222222222"))' "short-id pool not unioned (old+new)"
check 'any(.transports[]; .type=="shadowsocks-2022")'                            "other transport (shadowsocks-2022) was dropped"
check '([.transports[]|select(.type=="vless-reality")] | length) == 1'          "vless-reality duplicated instead of merged in place"

echo
echo "PASS: reality_sync merge updates entry_ip/pub, unions short-ids, preserves other transports"
