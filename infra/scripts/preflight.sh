#!/usr/bin/env bash
# preflight.sh — fail-fast validation that MUST pass BEFORE any paid/destructive
# terraform apply. Source, don't execute (depends on lib.sh). Every check dies
# (non-zero, clear message) on failure; node_up runs preflight_all before apply so
# a missing/invalid secret or credential is caught before a single VM is billed.
#
# Checks are individual functions so a test can stub the network-touching ones and
# assert that a failure aborts before the provisioning command runs.
set -euo pipefail

# pf_require_present — every mandatory input must be set (presence only).
pf_require_present() {
  require_env HCLOUD_TOKEN VULTR_API_KEY SSH_PUBKEY CONTROL_PLANE_URL \
              CONTROL_PLANE_TOKEN PAIR_PSK REALITY_SERVER_NAMES REALITY_DEST
}

# pf_binaries — required tools plus the pinned sing-box version (single source of
# truth from infra/versions.env, which node_up must have sourced).
pf_binaries() {
  require_cmd terraform ansible ansible-playbook jq curl openssl ssh-keygen base64
  [ -n "${SINGBOX_VERSION:-}" ] \
    || die "preflight: SINGBOX_VERSION unset (source infra/versions.env)"
}

# pf_ssh_pubkey — SSH_PUBKEY must actually parse as an SSH public key.
pf_ssh_pubkey() {
  printf '%s\n' "$SSH_PUBKEY" | ssh-keygen -l -f /dev/stdin >/dev/null 2>&1 \
    || die "preflight: SSH_PUBKEY is not a valid public key"
}

# pf_pair_psk — 2022-blake3-aes-256-gcm requires a 32-byte base64 key.
pf_pair_psk() {
  local n
  n="$(printf '%s' "$PAIR_PSK" | base64 -d 2>/dev/null | wc -c | tr -d ' ')" \
    || die "preflight: PAIR_PSK is not valid base64"
  [ "$n" = "32" ] \
    || die "preflight: PAIR_PSK must decode to 32 bytes for 2022-blake3-aes-256-gcm (got ${n})"
}

# pf_reality_tls13 — the REALITY handshake host must complete a TLS 1.3 handshake,
# else the mimicry target is unusable and would fingerprint on the node.
pf_reality_tls13() {
  local host="${REALITY_DEST%%:*}" port="${REALITY_DEST##*:}"
  [ "$port" != "$REALITY_DEST" ] || port=443
  printf 'Q' | timeout 12 openssl s_client -tls1_3 -connect "${host}:${port}" -servername "$host" \
    >/dev/null 2>&1 \
    || die "preflight: REALITY_DEST ${REALITY_DEST} did not complete a TLS 1.3 handshake"
}

# pf_hy2_tls — only when hysteria2 is enabled: cert/key are a matching pair, the
# cert is not expired, and its SAN covers HY2_SNI.
pf_hy2_tls() {
  [ -n "${HY2_SNI:-}" ] || return 0
  require_env HY2_TLS_CERT HY2_TLS_KEY
  local cpk kpk
  cpk="$(openssl x509 -in "$HY2_TLS_CERT" -noout -pubkey 2>/dev/null | openssl md5)" \
    || die "preflight: HY2_TLS_CERT unreadable"
  kpk="$(openssl pkey -in "$HY2_TLS_KEY" -pubout 2>/dev/null | openssl md5)" \
    || die "preflight: HY2_TLS_KEY unreadable"
  [ "$cpk" = "$kpk" ] \
    || die "preflight: HY2_TLS_CERT and HY2_TLS_KEY are not a matching pair"
  openssl x509 -in "$HY2_TLS_CERT" -noout -checkend 0 >/dev/null 2>&1 \
    || die "preflight: HY2_TLS_CERT is expired"
  # Extract each DNS SAN and match HY2_SNI EXACTLY (whole line, fixed string) — a
  # substring/boundary match would wrongly accept e.g. SAN hy2.example.com for SNI
  # hy2.example, and an unescaped SNI in a regex would let '.' match any char.
  openssl x509 -in "$HY2_TLS_CERT" -noout -ext subjectAltName 2>/dev/null \
    | tr ',' '\n' | sed -n 's/.*DNS:\([^ ,]*\).*/\1/p' \
    | grep -qxiF "$HY2_SNI" \
    || die "preflight: HY2_TLS_CERT SAN does not cover HY2_SNI ${HY2_SNI}"
}

# pf_cp_reachable — the control-plane health endpoint must answer before we
# provision (registration happens post-apply; catch an unreachable CP early).
pf_cp_reachable() {
  curl -fsS --connect-timeout 5 --max-time 15 "${CONTROL_PLANE_URL%/}/healthz" >/dev/null 2>&1 \
    || die "preflight: control-plane ${CONTROL_PLANE_URL%/}/healthz not reachable"
}

# pf_cloud_auth <tf-dir> — read-only credential/config check via terraform plan (no
# apply). Validates provider init + configuration with the operator's tokens in the
# environment. Full API auth is exercised at apply; this catches missing/invalid
# config and credentials before any billable resource is created.
pf_cloud_auth() {
  local dir="$1"
  terraform -chdir="$dir" init -input=false >/dev/null 2>&1 \
    || die "preflight: terraform init failed"
  terraform -chdir="$dir" plan -input=false -var "ssh_pubkey=${SSH_PUBKEY}" >/dev/null 2>&1 \
    || die "preflight: terraform plan failed (cloud credentials / config invalid)"
}

# preflight_all <tf-dir> — run every check; the FIRST failure aborts before apply.
preflight_all() {
  local dir="$1"
  pf_require_present
  pf_binaries
  pf_ssh_pubkey
  pf_pair_psk
  pf_hy2_tls
  pf_reality_tls13
  pf_cp_reachable
  pf_cloud_auth "$dir"
  log "preflight: all checks passed"
}
