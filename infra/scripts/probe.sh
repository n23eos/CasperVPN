#!/usr/bin/env bash
# probe.sh — protocol-aware transport probing for the reconciler. Source, don't
# execute. Each transport is verified INDEPENDENTLY: an authenticated request must
# succeed (real 2xx) AND the reply must confirm egress via the EXIT. A TCP dial
# proves neither, so it is never used.
#
# FAIL-CLOSED: any launch failure, a non-2xx response, an unparsable evidence body,
# or an unknown transport type counts the transport as FAILED — never skipped.

# _probe_result <type> <authenticated_http> <exit_ip_verified> <error>
_probe_result() {
  jq -cn --arg t "$1" --argjson http "$2" --argjson egress "$3" --arg err "$4" \
    '{type:$t, authenticated_http:$http, exit_ip_verified:$egress, error:$err}'
}

# probe_transport <type> <client-config-file> <socks-port> <echo-host> <exit-ip>
#   env: PROBE_IMG PROBE_NET PROBE_CLIENT (docker image / network / container name)
# Starts a single-transport client, makes ONE authenticated HTTP request through
# it to the echo host, and checks the echo's reported peer == the exit IP. Echoes
# the structured result JSON and returns 0 only when BOTH flags are true.
probe_transport() {
  local type="$1" cfg="$2" port="$3" echo_host="$4" exit_ip="$5"
  local img="${PROBE_IMG:?}" net="${PROBE_NET:?}" client="${PROBE_CLIENT:-capvpn-probe-client}"

  case "$type" in
    vless-reality | hysteria2 | shadowsocks-2022 | amneziawg) ;;
    *) _probe_result "$type" false false "unknown transport type"; return 1 ;;
  esac

  docker rm -f "$client" >/dev/null 2>&1 || true
  if ! docker run -d --name "$client" --network "$net" -p "${port}:1080" \
        -v "${cfg}:/c.json:ro" "$img" run -c /c.json >/dev/null 2>&1; then
    _probe_result "$type" false false "client container failed to start"; return 1
  fi
  sleep 3

  local bodyfile code body remote http=false egress=false err=""
  bodyfile="$(mktemp)"
  code="$(curl -sS --max-time 20 -o "$bodyfile" -w '%{http_code}' \
    --proxy "socks5h://127.0.0.1:${port}" "http://${echo_host}/" 2>/dev/null)" || code=000
  body="$(cat "$bodyfile" 2>/dev/null || true)"; rm -f "$bodyfile"
  docker rm -f "$client" >/dev/null 2>&1 || true

  case "$code" in
    2??) http=true ;;
    *)   err="http ${code}" ;;
  esac
  # Evidence: the echo (traefik/whoami) prints "RemoteAddr: <ip>:<port>".
  remote="$(printf '%s' "$body" | awk -F'[ :]' '/^RemoteAddr:/{print $3}')"
  if [ "$http" = true ]; then
    if [ -z "$remote" ]; then
      err="evidence body unparsable (no RemoteAddr)"
    elif [ "$remote" = "$exit_ip" ]; then
      egress=true
    else
      err="egress via ${remote}, not exit ${exit_ip}"
    fi
  fi

  _probe_result "$type" "$http" "$egress" "$err"
  { [ "$http" = true ] && [ "$egress" = true ]; }
}

# transport_gate <results-json-array> -> 0 iff >=2 DISTINCT transport types have
# BOTH authenticated_http and exit_ip_verified true. Two records of the same type
# are not diversity. Anything less (including all-failed) denies activation.
transport_gate() {
  local results="$1" n
  n="$(printf '%s' "$results" | jq '
    [ .[] | select(.authenticated_http == true and .exit_ip_verified == true) | .type ]
    | unique | length' 2>/dev/null || echo 0)"
  [ "${n:-0}" -ge 2 ]
}
