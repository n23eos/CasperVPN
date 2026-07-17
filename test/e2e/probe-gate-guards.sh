#!/usr/bin/env bash
# probe-gate-guards.sh — pure-shell regression for the reconciler's transport
# gate + probe-result contract (no docker). Proves activation is allowed ONLY for
# >=2 DISTINCT transport types with both flags true, and that the gate is
# fail-closed for same-type / half-verified / errored / empty inputs.
set -uo pipefail
cd "$(dirname "$0")/../.."
# shellcheck source=/dev/null
source infra/scripts/lib.sh
# shellcheck source=/dev/null
source infra/scripts/probe.sh

pass=0; fail=0
ok()  { echo "  ok: $1"; pass=$((pass+1)); }
bad() { echo "  FAIL: $1" >&2; fail=$((fail+1)); }

R() { _probe_result "$1" "$2" "$3" "${4:-}"; }  # type http egress [err]

# helper: assert transport_gate(results) has expected rc (0=allow,1=deny)
gate() {
  local want="$1"; shift
  local arr; arr="$(printf '%s\n' "$@" | jq -sc '.')"
  if transport_gate "$arr"; then [ "$want" = allow ] && return 0 || return 1
  else [ "$want" = deny ] && return 0 || return 1; fi
}

gate allow "$(R vless-reality true true)" "$(R hysteria2 true true)"      && ok "2 distinct both-verified -> allow" || bad "should allow"
gate deny  "$(R vless-reality true true)"                                  && ok "1 transport -> deny" || bad "single should deny"
gate deny  "$(R vless-reality true true)" "$(R vless-reality true true)"   && ok "2 records same type -> deny (not diversity)" || bad "same type should deny"
gate deny  "$(R vless-reality true true)" "$(R hysteria2 true false 'egress not exit')" && ok "one exit_ip_verified=false -> deny" || bad "half-verified should deny"
gate deny  "$(R vless-reality true true)" "$(R hysteria2 false false 'http 000')"       && ok "one authenticated_http=false -> deny" || bad "unauthenticated should deny"
gate deny                                                                                 && ok "empty results -> deny (fail-closed)" || bad "empty should deny"

# _probe_result shape + fail-closed for unknown type (via the case in probe_transport
# is docker-bound; here assert the result JSON contract directly).
res="$(R amneziawg false false 'launch error')"
[ "$(jq -r .type <<<"$res")" = amneziawg ] && [ "$(jq -r .authenticated_http <<<"$res")" = false ] \
  && [ "$(jq -r .exit_ip_verified <<<"$res")" = false ] && [ "$(jq -r .error <<<"$res")" = "launch error" ] \
  && ok "probe result carries type/flags/error" || bad "probe result contract wrong: $res"

echo
if [ "$fail" -eq 0 ]; then echo "PASS: probe gate guards ($pass checks)"; else echo "FAIL: $fail failed" >&2; exit 1; fi
