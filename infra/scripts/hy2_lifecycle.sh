#!/usr/bin/env bash
# hy2_lifecycle.sh — helpers for the hysteria2 credential/TLS lifecycle across
# node provisioning and rotation. Source, don't execute. Depends on lib.sh (die)
# and control_plane.sh (cp_get_node) being sourced by the caller.

# write_secure_vars_file <json> -> prints the path of a fresh 0600 temp file
# holding the JSON. Ansible extra-vars carrying a secret must be passed via
# `-e @file`, NEVER `-e "<json>"` on the command line, where a password is visible
# in the process list and CI diagnostics. The caller traps removal of the file.
write_secure_vars_file() {
  local f
  f="$(mktemp)"
  chmod 600 "$f"
  printf '%s' "$1" >"$f"
  printf '%s' "$f"
}

# hy2_desired_from_cp <node-id> -> echoes "password<TAB>sni" for the node's
# hysteria2 transport, or nothing if the node carries no hysteria2. The
# control-plane is the durable source for the hysteria2 PASSWORD (a client secret
# it already advertises) across a VM replacement.
#
# Fail-closed: cp_get_node itself dies if the control-plane is unreachable — this
# helper must NEVER be wrapped in `|| echo '{}'`, which would silently rotate
# without the durable state and desync the fresh server from the control-plane.
# A hysteria2 transport present but missing password OR sni is a hard error.
hy2_desired_from_cp() {
  local id="$1" node pw sni
  # Check the substitution's exit explicitly: cp_get_node's die runs inside the
  # $() subshell and would otherwise leave this function running with empty node.
  node="$(cp_get_node "$id")" || die "hy2: control-plane read failed for ${id} (refusing to rotate blind)"
  pw="$(printf '%s' "$node" | jq -r 'first(.transports[]? | select(.type=="hysteria2")).hysteria2.password // empty')"
  sni="$(printf '%s' "$node" | jq -r 'first(.transports[]? | select(.type=="hysteria2")).hysteria2.sni // empty')"
  if [ -z "$pw" ] && [ -z "$sni" ]; then
    return 0
  fi
  [ -n "$pw" ] && [ -n "$sni" ] || die "hy2: partial hysteria2 state in control-plane for ${id} (need both password and sni)"
  printf '%s\t%s' "$pw" "$sni"
}

# --- entry<->exit link (Shadowsocks-2022) --------------------------------------
# The PAIR PSK is an INTERNAL secret of the entry<->exit data plane: it is never
# stored in the control-plane and never rendered into a client config. Both sides
# of ONE pair share it (exit SS2022 inbound psk == entry exit_endpoint password).
# It is a pair-level (not per-node) secret spanning two VMs, so it cannot live in
# a single on-VM state file — it comes from the operator secret manager (PAIR_PSK)
# and is re-supplied to a replacement VM from there (never from the control-plane,
# which does not hold it, and never from the destroyed VM).

# resolve_pair_psk -> echoes the entry<->exit PAIR PSK. It comes from the operator
# secret manager (PAIR_PSK). node_up does NOT persist it anywhere, so a generated
# one could never be recovered for a future rotation — production therefore
# REQUIRES PAIR_PSK. A throwaway is minted ONLY in explicit e2e mode
# (EXIT_LINK_E2E=true), never silently in production.
resolve_pair_psk() {
  if [ -n "${PAIR_PSK:-}" ]; then
    printf '%s' "$PAIR_PSK"
    return 0
  fi
  if [ "${EXIT_LINK_E2E:-false}" = "true" ]; then
    openssl rand -base64 32
    return 0
  fi
  die "PAIR_PSK is required (entry<->exit pair secret from the secret manager). node_up does not persist it, so a generated one could never be recovered for a rotation. Mint it once with an operator command, store it in the secret manager, and pass it as PAIR_PSK. For a throwaway e2e set EXIT_LINK_E2E=true."
}

# resolve_exit_topology <exit_ip_read_rc> <exit_ip> -> prints "true"/"false" for
# EXIT_CONFIGURED. Topology is EXPLICIT, never inferred from a command failure: a
# terraform/state read error or an empty exit_ip HARD-FAILS (so a fresh entry is
# never brought up chain-less by accident, egressing its own IP). A genuine
# single-node deployment must opt out on purpose with NO_EXIT_LINK=true.
resolve_exit_topology() {
  local read_rc="$1" exit_ip="$2"
  if [ "${NO_EXIT_LINK:-false}" = "true" ]; then
    printf 'false'
    return 0
  fi
  [ "$read_rc" = "0" ] \
    || die "cannot read exit_ip (terraform/state error) — refusing to rotate; set NO_EXIT_LINK=true ONLY for an intentional single-node (entry==exit) deployment"
  [ -n "$exit_ip" ] \
    || die "exit_ip is empty — refusing to rotate blind (would egress the entry's own IP, breaking entry != exit); set NO_EXIT_LINK=true if that is intended"
  printf 'true'
}

# require_pair_psk_for_rotation <exit-configured?> — hard-fail if the entry->exit
# chain is configured but PAIR_PSK is absent, so a rotation cannot silently drop
# the link (which would send the fresh entry's traffic out its own IP, breaking
# entry != exit).
require_pair_psk_for_rotation() {
  local exit_configured="$1"
  [ "$exit_configured" = "true" ] || return 0
  [ -n "${PAIR_PSK:-}" ] \
    || die "entry->exit chaining is configured but PAIR_PSK (secret manager) is not set — a replacement entry needs the pair PSK or the chain breaks (traffic would egress the entry, violating entry != exit)"
}

# exit_link_extra_vars <server> <port> <psk> -> JSON for the entry's exit_endpoint
# (outbound to the exit) and, on the exit host, the SS2022 inbound psk. Consumed
# by node-up.yml pre_tasks (gated by node_role). psk is secret => vars via file.
exit_link_extra_vars() {
  local server="$1" port="$2" psk="$3"
  jq -n --arg server "$server" --argjson port "${port:-8388}" --arg psk "$psk" \
    '{pair_psk: $psk, exit_link_server: $server, exit_link_port: $port}'
}

# hy2_extra_vars <sni> <password> <tls_cert> <tls_key> -> JSON object of the
# hysteria2 desired state for ansible. TLS cert/key are SERVER secrets (never in
# the control-plane) and must come from the operator's secret manager on both
# node-up and rotation, so a replacement VM can present the same trusted cert.
hy2_extra_vars() {
  local sni="$1" pw="$2" cert="$3" key="$4"
  jq -n --arg sni "$sni" --arg pw "$pw" --arg cert "$cert" --arg key "$key" '
    {hy2_sni: $sni, hy2_password: $pw}
    + (if $cert != "" then {hy2_tls_cert: $cert} else {} end)
    + (if $key  != "" then {hy2_tls_key:  $key}  else {} end)'
}
