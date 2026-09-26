#!/usr/bin/env bash
# Exercise the rendered node-side lease watchdog with a fake systemctl. This is
# local-only and proves expiry or corruption closes sing-box during CP outage.
set -uo pipefail
cd "$(dirname "$0")/../.."

pass=0; fail=0
ok()  { echo "  ok: $1"; pass=$((pass+1)); }
bad() { echo "  FAIL: $1" >&2; fail=$((fail+1)); }

TMP="$(mktemp -d)"; mkdir -p "$TMP/bin"
LEASE="$TMP/lease.json"; WATCHDOG="$TMP/watchdog.py"; CALLS="$TMP/systemctl"
sed "s|{{ access_lease_file }}|$LEASE|g" \
  infra/ansible/roles/transports/templates/access-lease-watchdog.py.j2 >"$WATCHDOG"
cat >"$TMP/bin/systemctl" <<'SH'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"${WATCHDOG_CALLS:?}"
SH
chmod +x "$TMP/bin/systemctl" "$WATCHDOG"
export PATH="$TMP/bin:$PATH" WATCHDOG_CALLS="$CALLS"

printf '%s' '{"revision":"r1","valid_until":"2999-01-01T00:00:00Z"}' >"$LEASE"
if python3 "$WATCHDOG" >/dev/null 2>&1 && [ ! -s "$CALLS" ]; then
  ok "fresh lease keeps sing-box running"
else
  bad "fresh lease was denied"
fi

printf '%s' '{"revision":"r1","valid_until":"2000-01-01T00:00:00Z"}' >"$LEASE"
( python3 "$WATCHDOG" ) >/dev/null 2>&1 \
  && bad "expired lease was accepted" \
  || ok "expired lease returns failure"
grep -q '^stop sing-box$' "$CALLS" \
  && ok "expired lease stops sing-box fail-closed" \
  || bad "expired lease did not stop sing-box"

: >"$CALLS"; printf '%s' '{broken-json' >"$LEASE"
( python3 "$WATCHDOG" ) >/dev/null 2>&1 \
  && bad "corrupt lease was accepted" \
  || ok "corrupt lease returns failure"
grep -q '^stop sing-box$' "$CALLS" \
  && ok "corrupt lease stops sing-box fail-closed" \
  || bad "corrupt lease did not stop sing-box"

: >"$CALLS"; printf '%s' '{"revision":"r1","valid_until":"2999-01-01T00:00:00"}' >"$LEASE"
( python3 "$WATCHDOG" ) >/dev/null 2>&1 \
  && bad "timezone-naive lease was accepted" \
  || ok "timezone-naive lease returns failure"
grep -q '^stop sing-box$' "$CALLS" \
  && ok "timezone-naive lease stops sing-box fail-closed" \
  || bad "timezone-naive lease did not stop sing-box"

rm -rf "$TMP"
echo "access-lease-watchdog-guard: ${pass} ok, ${fail} fail"
[ "$fail" = 0 ]
