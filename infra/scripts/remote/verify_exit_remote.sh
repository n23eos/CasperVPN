#!/usr/bin/env bash
# verify_exit_remote.sh — runs ON THE ENTRY host (delivered over ssh stdin by
# reconcile_live.sh hook_verify_exit). It builds a temporary 0600 SS2022 client to
# the exit, sends one bounded request to the operator echo through it, and prints
# the raw echo body on stdout. reconcile_live.sh parses/decides (egress == exit).
#
# The exit firewall accepts the SS2022 link only from the entry IP, so this check
# can only succeed here — that is the point: it proves the entry->exit path.
#
# Env (from the ssh command): EXIT_IP EXIT_LINK_PORT ECHO_URL SINGBOX PSK_FILE
# The PSK is read from PSK_FILE (staged 0600 by the caller), never from argv.
set -euo pipefail
: "${EXIT_IP:?}" "${EXIT_LINK_PORT:?}" "${ECHO_URL:?}" "${SINGBOX:?}" "${PSK_FILE:?}"

psk="$(cat "$PSK_FILE")"
work="$(mktemp -d)"
cleanup() { rm -rf "$work"; rm -f "$PSK_FILE"; [ -n "${pid:-}" ] && kill "$pid" 2>/dev/null || true; }
trap cleanup EXIT

cfg="${work}/ss2022.json"
( umask 077
  jq -n --arg exip "$EXIT_IP" --argjson port "$EXIT_LINK_PORT" --arg psk "$psk" \
    '{log:{level:"error"},
      inbounds:[{type:"mixed",listen:"127.0.0.1",listen_port:11080}],
      outbounds:[{type:"shadowsocks",server:$exip,server_port:$port,
                  method:"2022-blake3-aes-256-gcm",password:$psk},
                 {type:"direct",tag:"direct"}],
      route:{final:"shadowsocks"}}' >"$cfg"
)

"$SINGBOX" run -c "$cfg" >/dev/null 2>&1 &
pid=$!
sleep 2
curl -fsS --connect-timeout 5 --max-time 15 --socks5-hostname 127.0.0.1:11080 "$ECHO_URL"
