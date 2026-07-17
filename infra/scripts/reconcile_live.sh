#!/usr/bin/env bash
# reconcile_live.sh — production driver for reconcile_node.sh against a REAL
# entry+exit pair. It supplies live implementations of the three reconcile hooks
# and runs reconcile_pair with the pair's logical CP ids from the run manifest.
#
# The DECISION logic (echo contract, egress==exit, >=2-transport gate) lives here
# and is unit-tested by test/infra/reconcile-live-guard.sh with the network
# primitives stubbed. The primitives that actually touch the pair (SSH to the
# entry, running a client, converging via Ansible) are thin and overridable, so
# the guard proves wiring + fail-closed WITHOUT a VPS. Live data-plane proof is
# only meaningful behind GATE-PAID.
#
# Hooks:
#   RECONCILE_VERIFY_EXIT — proves the exit data plane FROM THE ENTRY HOST. The
#     exit firewall accepts the SS2022 link only from the entry IP, so the check
#     MUST originate on the entry; a probe from the operator host is, and must be,
#     dropped. The PAIR_PSK reaches the entry over stdin (a remote 0600 temp),
#     never argv/log.
#   RECONCILE_APPLY_CMD — full-syncs the allow-list onto the entry via Ansible,
#     REUSING the node's existing REALITY/hy2 state (no key/password rotation on a
#     reconcile). Secret vars go through a 0600 vars file, removed after.
#   RECONCILE_PROBE_CMD — runs the VLESS-REALITY and hysteria2 client probes from
#     the operator host against the PUBLIC entry, each proving auth + exit egress.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "${HERE}/lib.sh"
# shellcheck source=run_manifest.sh
source "${HERE}/run_manifest.sh"
# reconcile_node.sh pulls in probe.sh (transport_gate) + control_plane.sh (_cp).
export RECONCILE_LIB_ONLY=true
# shellcheck source=reconcile_node.sh
source "${HERE}/reconcile_node.sh"

# --- overridable network primitives (stubbed in the guard test) --------------

# rl_ssh_entry <cmd...> — run a command ON the entry host; stdin is forwarded, so
# secrets travel over stdin, never argv. SSH_USER default root; SSH_OPTS operator.
rl_ssh_entry() {
  # SC2086: SSH_OPTS is intentionally word-split. SC2029: args (e.g. EXIT_IP=..)
  # are meant to expand locally into the remote command line.
  # shellcheck disable=SC2086,SC2029
  ssh ${SSH_OPTS:-} "${SSH_USER:-root}@${ENTRY_IP}" "$@"
}

# rl_run_client_probe <type> <client-config-file> — run a single-transport client
# with the given config, send one bounded request to EGRESS_ECHO_URL through it,
# and echo a JSON verdict {type, authenticated_http, exit_ip_verified}. The live
# implementation runs a docker sing-box client; it is a thin, replaceable seam.
rl_run_client_probe() {
  local type="$1" cfg="$2" img="ghcr.io/sagernet/sing-box:v${SINGBOX_VERSION:?SINGBOX_VERSION required}"
  local name="rl-probe-$$-${type}" body observed auth=false exitok=false
  require_cmd docker
  docker rm -f "$name" >/dev/null 2>&1 || true
  docker run -d --name "$name" -p 127.0.0.1:0:1080 -v "${cfg}:/c.json:ro" "$img" run -c /c.json >/dev/null || {
    _rl_probe_verdict "$type" false false; return 0; }
  local hostport; hostport="$(docker port "$name" 1080/tcp 2>/dev/null | head -n1)"
  sleep 2
  if [ -n "$hostport" ]; then
    body="$(curl -fsS --connect-timeout 5 --max-time 15 --socks5-hostname "$hostport" "$EGRESS_ECHO_URL" 2>/dev/null)" && auth=true
  fi
  docker rm -f "$name" >/dev/null 2>&1 || true
  if [ "$auth" = true ]; then
    observed="$(echo_observed_ip "$body" || true)"
    [ -n "$observed" ] && [ "$observed" = "$EXIT_IP" ] && exitok=true
  fi
  _rl_probe_verdict "$type" "$auth" "$exitok"
}

_rl_probe_verdict() {
  jq -nc --arg t "$1" --argjson a "$2" --argjson e "$3" \
    '{type:$t, authenticated_http:$a, exit_ip_verified:$e}'
}

# rl_converge <vars-file> — run the Ansible converge on the entry with the given
# 0600 vars file (reality_users + reuse of on-node state). Overridden in tests.
rl_converge() {
  local vars="$1" inv
  require_env ENTRY_IP EXIT_IP
  inv="$(mktemp)"
  {
    printf '[entry]\n%s ansible_host=%s node_role=entry\n' "$ENTRY_RAW_ID" "$ENTRY_IP"
  } >"$inv"
  ansible-playbook -i "$inv" "$(repo_root)/${ANSIBLE_DIR_DEFAULT}/playbooks/node-up.yml" -e "@${vars}"
  local rc=$?
  rm -f "$inv"
  return "$rc"
}

# --- production echo contract -------------------------------------------------

# echo_observed_ip <body> — strict, fail-closed parse of the operator-provided
# egress echo. The echo MUST return JSON {"ip":"<client-ip>"}; anything else
# (HTML, a bare string, an error page) is rejected. No echo URL/domain is baked in
# — EGRESS_ECHO_URL is operator-supplied.
echo_observed_ip() {
  local body="$1" ip
  ip="$(jq -er '.ip' <<<"$body" 2>/dev/null)" || return 1
  [ -n "$ip" ] && [ "$ip" != "null" ] || return 1
  printf '%s' "$ip"
}

# --- the three live hooks -----------------------------------------------------

hook_verify_exit() {
  require_env ENTRY_IP EXIT_IP PAIR_PSK EGRESS_ECHO_URL
  local body observed remote_psk="/tmp/caspervpn-verify-psk.$$"
  # Deliver the PSK to the entry over stdin into a 0600 file (never argv/log), then
  # run the remote verify script which reads the PSK from that file and removes it.
  printf '%s' "$PAIR_PSK" | rl_ssh_entry "umask 077; cat > '${remote_psk}'" \
    || { log "reconcile-live: could not stage PSK on entry"; return 1; }
  local rc=0
  body="$(rl_ssh_entry \
            EXIT_IP="$EXIT_IP" EXIT_LINK_PORT="${EXIT_LINK_PORT:-8388}" \
            ECHO_URL="$EGRESS_ECHO_URL" SINGBOX="${REMOTE_SINGBOX:-sing-box}" \
            PSK_FILE="$remote_psk" \
            bash -s < "${RL_REMOTE_VERIFY:-$HERE/remote/verify_exit_remote.sh}")" || rc=$?
  # Belt-and-suspenders: the remote script removes PSK_FILE on its own exit, but if
  # the run call died before that trap, clean the staged 0600 PSK from the operator side.
  rl_ssh_entry "rm -f '${remote_psk}'" >/dev/null 2>&1 || true
  [ "$rc" -eq 0 ] || { log "reconcile-live: entry->exit verify command failed"; return 1; }
  observed="$(echo_observed_ip "$body")" \
    || { log "reconcile-live: echo body did not match the {\"ip\":..} contract — fail closed"; return 1; }
  if [ "$observed" != "$EXIT_IP" ]; then
    log "reconcile-live: egress ${observed} != exit ${EXIT_IP} — exit NOT verified"; return 1
  fi
  log "reconcile-live: exit verified from entry host (egress == exit ${EXIT_IP})"
}

hook_apply() {
  [ -n "${RECONCILE_USERS:-}" ] || { log "reconcile-live: no RECONCILE_USERS"; return 1; }
  require_env PAIR_PSK
  # Reuse existing on-node REALITY/hy2 state: pass ONLY the allow-list + the pair
  # link PSK, never a keygen/rotate flag, so a reconcile never rotates secrets.
  local vars; vars="$(mktemp)"
  ( umask 077
    jq -n --argjson users "$RECONCILE_USERS" --arg psk "$PAIR_PSK" \
       --arg exip "$EXIT_IP" --argjson port "${EXIT_LINK_PORT:-8388}" \
       '{reality_users:$users, reality_reuse_existing_key:true,
         exit_endpoint:{server:$exip, server_port:$port, psk:$psk}}' >"$vars"
  )
  local rc=0
  rl_converge "$vars" || rc=$?
  rm -f "$vars"
  [ "$rc" -eq 0 ] || { log "reconcile-live: converge failed"; return 1; }
}

hook_probe() {
  require_env ENTRY_IP EXIT_IP EGRESS_ECHO_URL
  local vcfg hcfg a b
  vcfg="$(_rl_vless_client_config)" || return 1
  a="$(rl_run_client_probe vless-reality "$vcfg")"; rm -f "$vcfg"
  hcfg="$(_rl_hy2_client_config)" || { log "reconcile-live: hy2 client not configured"; printf '[%s]' "$a"; return 0; }
  b="$(rl_run_client_probe hysteria2 "$hcfg")"; rm -f "$hcfg"
  jq -sc '.' <<<"$a"$'\n'"$b"
}

# _rl_vless_client_config / _rl_hy2_client_config — build a 0600 single-transport
# client config against the PUBLIC entry from the operator-supplied client params
# (public key, short id, uuid, sni, hy2 password). No mimicry value is baked in.
_rl_vless_client_config() {
  require_env RL_VLESS_UUID RL_VLESS_SHORT_ID RL_VLESS_PUBKEY RL_REALITY_SNI
  local f; f="$(mktemp)"
  ( umask 077
    jq -n --arg s "$ENTRY_IP" --arg u "$RL_VLESS_UUID" --arg sid "$RL_VLESS_SHORT_ID" \
       --arg pub "$RL_VLESS_PUBKEY" --arg sni "$RL_REALITY_SNI" \
       '{log:{level:"warn"},inbounds:[{type:"mixed",listen:"127.0.0.1",listen_port:1080}],
         outbounds:[{type:"vless",server:$s,server_port:443,uuid:$u,flow:"xtls-rprx-vision",
           tls:{enabled:true,server_name:$sni,utls:{enabled:true,fingerprint:"chrome"},
           reality:{enabled:true,public_key:$pub,short_id:$sid}}},{type:"direct",tag:"direct"}],
         route:{final:"vless"}}' >"$f"
  ) || { rm -f "$f"; return 1; }
  printf '%s' "$f"
}

_rl_hy2_client_config() {
  [ -n "${RL_HY2_PASSWORD:-}" ] && [ -n "${RL_HY2_SNI:-}" ] || return 1
  local f; f="$(mktemp)"
  ( umask 077
    jq -n --arg s "$ENTRY_IP" --arg pw "$RL_HY2_PASSWORD" --arg sni "$RL_HY2_SNI" \
       '{log:{level:"warn"},inbounds:[{type:"mixed",listen:"127.0.0.1",listen_port:1080}],
         outbounds:[{type:"hysteria2",server:$s,server_port:8443,password:$pw,
           tls:{enabled:true,server_name:$sni,alpn:["h3"]}},{type:"direct",tag:"direct"}],
         route:{final:"hysteria2"}}' >"$f"
  ) || { rm -f "$f"; return 1; }
  printf '%s' "$f"
}

reconcile_live_main() {
  require_cmd jq curl ssh
  require_env RUN_ID EGRESS_ECHO_URL CONTROL_PLANE_URL CONTROL_PLANE_TOKEN
  # Operator-supplied values flow into a remote ssh command line; validate their
  # shape so a stray metacharacter can't execute on the entry.
  case "$EGRESS_ECHO_URL" in
    https://* | http://*) ;;
    *) die "EGRESS_ECHO_URL must be an http(s) URL" ;;
  esac
  # shellcheck disable=SC2016  # the '$' is a literal in this grep character class
  if printf '%s' "$EGRESS_ECHO_URL" | grep -qE '[[:space:];`$(){}|&<>]'; then
    die "EGRESS_ECHO_URL contains shell metacharacters"
  fi
  case "${EXIT_LINK_PORT:-8388}" in
    *[!0-9]*) die "EXIT_LINK_PORT must be numeric" ;;
  esac
  # Logical CP ids + IPs come from the durable run manifest (globally-unique ids).
  ENTRY="$(manifest_field "$RUN_ID" '.entry.cp_id')"
  EXIT="$(manifest_field "$RUN_ID" '.exit.cp_id')"
  ENTRY_RAW_ID="$(manifest_field "$RUN_ID" '.entry.raw_id')"
  ENTRY_IP="$(manifest_field "$RUN_ID" '.entry.ip')"
  EXIT_IP="$(manifest_field "$RUN_ID" '.exit.ip')"
  export ENTRY EXIT ENTRY_RAW_ID ENTRY_IP EXIT_IP
  export RECONCILE_VERIFY_EXIT=hook_verify_exit
  export RECONCILE_APPLY_CMD=hook_apply
  export RECONCILE_PROBE_CMD=hook_probe
  log "reconcile-live: entry=${ENTRY} (${ENTRY_IP}) exit=${EXIT} (${EXIT_IP})"
  reconcile_pair "$ENTRY" "$EXIT"
}

if [ "${RECONCILE_LIVE_LIB_ONLY:-false}" != "true" ]; then
  reconcile_live_main
fi
