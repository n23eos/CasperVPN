#!/usr/bin/env bash
# E2E happy path: the first working user, with real inter-service HTTP calls.
#
#   create user -> mock invoice -> HMAC "settled" webhook -> billing activates
#   subscription via control-plane -> rotate-token -> GET /sub/{token} ->
#   valid personalized sing-box config
#
# The only mock is the payment gateway inside billing (real HMAC verification).
# Runs in an isolated compose project (caspervpn-e2e) on alternative host ports;
# never touches the dev stack or its volumes. DB starts clean every run.
#
# Usage: test/e2e/first-user.sh   (or: make e2e-first-user)
set -euo pipefail

cd "$(dirname "$0")/../.."

PROJECT=caspervpn-e2e
# Prefer the compose plugin; fall back to the standalone docker-compose binary.
if docker compose version >/dev/null 2>&1; then
  COMPOSE_BIN=(docker compose)
elif command -v docker-compose >/dev/null 2>&1; then
  COMPOSE_BIN=(docker-compose)
else
  echo "FAIL: neither 'docker compose' nor 'docker-compose' available" >&2; exit 1
fi
COMPOSE=("${COMPOSE_BIN[@]}" -p "$PROJECT" -f docker-compose.dev.yml -f test/e2e/docker-compose.e2e.yml)
SERVICES=(postgres control-plane subscription billing)

PG_PORT=15432 CP_PORT=18081 SUB_PORT=18082 BILL_PORT=18084
CP="http://localhost:${CP_PORT}"
SUB="http://localhost:${SUB_PORT}"
BILL="http://localhost:${BILL_PORT}"
ADMIN_TOKEN=dev-admin-token
MOCK_SECRET=dev-mock-webhook-secret
CURL=(curl -sS --max-time 10)

fail() { echo "FAIL: $*" >&2; exit 1; }
step() { echo "==> $*"; }

cleanup() { "${COMPOSE[@]}" down -v --remove-orphans >/dev/null 2>&1 || true; }
trap cleanup EXIT

# --- 0. Preconditions -------------------------------------------------------
for tool in docker jq openssl nc; do
  command -v "$tool" >/dev/null || fail "required tool missing: $tool"
done
for p in $PG_PORT $CP_PORT $SUB_PORT $BILL_PORT; do
  if nc -z 127.0.0.1 "$p" 2>/dev/null; then
    fail "port $p is busy — stop whatever holds it (this script never kills other containers)"
  fi
done

# --- 1. Clean stack ---------------------------------------------------------
step "clean start: removing any previous $PROJECT state"
cleanup

step "building images (${SERVICES[*]})"
"${COMPOSE[@]}" build -q "${SERVICES[@]}"

step "starting postgres"
"${COMPOSE[@]}" up -d postgres
for i in $(seq 1 30); do
  "${COMPOSE[@]}" exec -T postgres pg_isready -U caspervpn >/dev/null 2>&1 && break
  [ "$i" -eq 30 ] && fail "postgres not ready after 60s"
  sleep 2
done

# billing + subscription schemas are applied out-of-band by design (no
# migrate-on-boot); control-plane migrates itself on startup.
step "applying billing + subscription schemas"
"${COMPOSE[@]}" exec -T postgres psql -q -v ON_ERROR_STOP=1 -U caspervpn -d caspervpn \
  < services/billing/internal/store/schema.sql
"${COMPOSE[@]}" exec -T postgres psql -q -v ON_ERROR_STOP=1 -U caspervpn -d caspervpn \
  < services/subscription/internal/controlplane/schema.sql

step "starting control-plane, subscription, billing"
"${COMPOSE[@]}" up -d "${SERVICES[@]}"
for url in "$CP/healthz" "$SUB/healthz" "$BILL/healthz"; do
  for i in $(seq 1 30); do
    "${CURL[@]}" -o /dev/null "$url" 2>/dev/null && break
    [ "$i" -eq 30 ] && { "${COMPOSE[@]}" logs --tail 50; fail "$url not healthy after 60s"; }
    sleep 2
  done
done

# --- 2. The user's journey --------------------------------------------------
step "create user (POST $CP/v1/users)"
USER_JSON=$("${CURL[@]}" -X POST "$CP/v1/users" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"telegram_id": 900001}')
USER_ID=$(jq -re '.id' <<<"$USER_JSON") || fail "no user id in: $USER_JSON"
USER_UUID=$(jq -re '.uuid' <<<"$USER_JSON") || fail "no user uuid in: $USER_JSON"
echo "    user: $USER_ID (uuid $USER_UUID)"

step "create mock invoice (POST $BILL/v1/invoices)"
INV_JSON=$("${CURL[@]}" -X POST "$BILL/v1/invoices" -H 'Content-Type: application/json' \
  -d "{\"anon_user_id\":\"$USER_ID\",\"plan\":\"basic\",\"currency\":\"BTC\"}")
INV_ID=$(jq -re '.invoice_id' <<<"$INV_JSON") || fail "no invoice_id in: $INV_JSON"
INV_AMOUNT=$(jq -re '.amount' <<<"$INV_JSON")
INV_CURRENCY=$(jq -re '.currency' <<<"$INV_JSON")
echo "    invoice: $INV_ID ($INV_AMOUNT $INV_CURRENCY)"

step "deliver signed 'settled' webhook (POST $BILL/v1/webhooks/mock)"
TS=$(date -u +%Y-%m-%dT%H:%M:%SZ)
WEBHOOK_BODY=$(jq -cn --arg ext "e2e-$INV_ID" --arg inv "$INV_ID" \
  --arg amt "$INV_AMOUNT" --arg cur "$INV_CURRENCY" --arg ts "$TS" \
  '{external_id:$ext, invoice_id:$inv, status:"settled", amount:$amt, currency:$cur, confirmations:6, ts:$ts}')
SIG=$(printf '%s' "$WEBHOOK_BODY" | openssl dgst -sha256 -hmac "$MOCK_SECRET" -hex | awk '{print $NF}')
HTTP=$("${CURL[@]}" -o /dev/null -w '%{http_code}' -X POST "$BILL/v1/webhooks/mock" \
  -H "X-Signature: $SIG" -H 'Content-Type: application/json' --data-binary "$WEBHOOK_BODY")
[ "$HTTP" = "200" ] || fail "webhook returned HTTP $HTTP"

step "subscription activated in control-plane"
SUB_ID=""
for i in $(seq 1 10); do
  SUB_ID=$("${CURL[@]}" -H "Authorization: Bearer $ADMIN_TOKEN" "$CP/v1/users/$USER_ID" \
    | jq -r '.subscription_id // empty')
  [ -n "$SUB_ID" ] && break
  sleep 1
done
[ -n "$SUB_ID" ] || fail "user has no subscription_id after settled webhook"
SUB_STATE=$("${CURL[@]}" -H "Authorization: Bearer $ADMIN_TOKEN" "$CP/v1/subscriptions/$SUB_ID")
jq -e '.status == "active"' <<<"$SUB_STATE" >/dev/null || fail "subscription not active: $SUB_STATE"
EXPIRES_1=$(jq -re '.expires_at' <<<"$SUB_STATE")
echo "    subscription: $SUB_ID active until $EXPIRES_1"

step "replay the same webhook — must be idempotent (no double period)"
HTTP=$("${CURL[@]}" -o /dev/null -w '%{http_code}' -X POST "$BILL/v1/webhooks/mock" \
  -H "X-Signature: $SIG" -H 'Content-Type: application/json' --data-binary "$WEBHOOK_BODY")
[ "$HTTP" = "200" ] || fail "webhook replay returned HTTP $HTTP"
EXPIRES_2=$("${CURL[@]}" -H "Authorization: Bearer $ADMIN_TOKEN" "$CP/v1/subscriptions/$SUB_ID" \
  | jq -re '.expires_at')
[ "$EXPIRES_1" = "$EXPIRES_2" ] || fail "replayed webhook extended the period: $EXPIRES_1 -> $EXPIRES_2"

# Temporary bridge for the e2e: rotate-token returns the plaintext token once.
# NOT the product path for handing links to users — every rotation revokes the
# previous link (see docs/FIRST-WORKING-USER.md).
step "obtain subscription token (POST rotate-token)"
TOKEN=$("${CURL[@]}" -X POST -H "Authorization: Bearer $ADMIN_TOKEN" \
  "$CP/v1/subscriptions/$SUB_ID/rotate-token" | jq -re '.token') || fail "rotate-token returned no token"

step "fetch personalized config (GET $SUB/sub/{token}?format=singbox)"
CONFIG=$("${CURL[@]}" "$SUB/sub/$TOKEN?format=singbox")
jq -e . >/dev/null <<<"$CONFIG" || fail "not JSON: $(head -c 300 <<<"$CONFIG")"

step "validate sing-box config"
# Real proxy outbounds only — the renderer always adds direct/urltest/selector
# service outbounds, which must not count toward transport diversity.
PROXIES=$(jq '[.outbounds[] | select(.type as $t
  | ["direct","block","dns","urltest","selector"] | index($t) | not)]' <<<"$CONFIG")
COUNT=$(jq 'length' <<<"$PROXIES")
[ "$COUNT" -ge 2 ] || fail "need >=2 real proxy outbounds, got $COUNT"
jq -e 'any(.[]; .type == "vless")' <<<"$PROXIES" >/dev/null || fail "no vless outbound"
jq -e 'any(.[]; .type == "hysteria2")' <<<"$PROXIES" >/dev/null || fail "no hysteria2 outbound"
jq -e --arg uuid "$USER_UUID" 'any(.[]; .type == "vless" and .uuid == $uuid)' <<<"$PROXIES" >/dev/null \
  || fail "vless outbound does not carry the user's personal uuid"
echo "    $COUNT proxy outbounds, vless+hysteria2 present, personal uuid confirmed"

echo
echo "PASS: first working user — full chain green (user -> invoice -> webhook -> activation -> /sub -> valid config)"
