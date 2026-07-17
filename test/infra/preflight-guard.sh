#!/usr/bin/env bash
# preflight-guard.sh — B4: preflight validates VALUES (not just presence) and
# aborts BEFORE apply. Pure shell (uses local openssl/base64; no cloud).
set -uo pipefail
cd "$(dirname "$0")/../.."
# shellcheck source=../../infra/scripts/lib.sh
source infra/scripts/lib.sh
# shellcheck source=../../infra/scripts/preflight.sh
source infra/scripts/preflight.sh
set +e

pass=0; fail=0
ok()  { echo "  ok: $1"; pass=$((pass+1)); }
bad() { echo "  FAIL: $1" >&2; fail=$((fail+1)); }

# --- pf_pair_psk: 2022-blake3-aes-256-gcm needs a 32-byte base64 key ---
PAIR_PSK="$(head -c 32 /dev/zero | base64)"; ( pf_pair_psk ) 2>/dev/null && ok "accepts a 32-byte PSK" || bad "rejected a valid PSK"
PAIR_PSK="$(head -c 16 /dev/zero | base64)"; ( pf_pair_psk ) 2>/dev/null && bad "accepted a 16-byte PSK" || ok "rejects a wrong-length PSK"
PAIR_PSK="not base64 %%%"; ( pf_pair_psk ) 2>/dev/null && bad "accepted non-base64 PSK" || ok "rejects non-base64 PSK"

# --- pf_ssh_pubkey: must parse as an SSH public key ---
KD="$(mktemp -d)"; ssh-keygen -t ed25519 -N '' -f "$KD/id" >/dev/null 2>&1
SSH_PUBKEY="$(cat "$KD/id.pub")"; ( pf_ssh_pubkey ) 2>/dev/null && ok "accepts a valid pubkey" || bad "rejected a valid pubkey"
SSH_PUBKEY="not-a-key"; ( pf_ssh_pubkey ) 2>/dev/null && bad "accepted garbage pubkey" || ok "rejects an invalid pubkey"
rm -rf "$KD"

# --- pf_hy2_tls: skip when disabled; match cert/key + SAN when enabled ---
unset HY2_SNI; ( pf_hy2_tls ) 2>/dev/null && ok "skips hy2 tls when HY2_SNI unset" || bad "hy2 skip failed"
TMP="$(mktemp -d)"
openssl req -x509 -newkey rsa:2048 -nodes -keyout "$TMP/k.pem" -out "$TMP/c.pem" -days 2 \
  -subj "/CN=hy2.example" -addext "subjectAltName=DNS:hy2.example" >/dev/null 2>&1
openssl req -x509 -newkey rsa:2048 -nodes -keyout "$TMP/k2.pem" -out "$TMP/c2.pem" -days 2 \
  -subj "/CN=other" -addext "subjectAltName=DNS:other.example" >/dev/null 2>&1
export HY2_SNI=hy2.example HY2_TLS_CERT="$TMP/c.pem" HY2_TLS_KEY="$TMP/k.pem"
( pf_hy2_tls ) 2>/dev/null && ok "accepts a matching cert/key with covering SAN" || bad "rejected a valid hy2 cert/key"
HY2_TLS_KEY="$TMP/k2.pem"; ( pf_hy2_tls ) 2>/dev/null && bad "accepted a mismatched cert/key pair" || ok "rejects mismatched cert/key"
HY2_TLS_KEY="$TMP/k.pem"; HY2_SNI=notcovered.example; ( pf_hy2_tls ) 2>/dev/null && bad "accepted a SAN that doesn't cover HY2_SNI" || ok "rejects SAN not covering HY2_SNI"
unset HY2_SNI HY2_TLS_CERT HY2_TLS_KEY; rm -rf "$TMP"

# --- preflight_all aborts on the FIRST failure (before cloud auth / apply) ---
ORDER="$(mktemp)"
pf_require_present() { echo present >>"$ORDER"; }
pf_binaries()        { echo binaries >>"$ORDER"; }
pf_ssh_pubkey()      { echo ssh >>"$ORDER"; die "injected ssh failure"; }   # real checks die(exit) on failure
pf_pair_psk()        { echo psk >>"$ORDER"; }
pf_hy2_tls()         { echo hy2 >>"$ORDER"; }
pf_reality_tls13()   { echo reality >>"$ORDER"; }
pf_cp_reachable()    { echo cp >>"$ORDER"; }
pf_cloud_auth()      { echo cloud_auth >>"$ORDER"; }      # must NOT run after an earlier abort
( preflight_all /tmp/x ) 2>/dev/null && bad "preflight_all passed despite a failing check" || ok "preflight_all aborts on first failure"
grep -q cloud_auth "$ORDER" && bad "cloud_auth ran after an earlier abort (no fail-fast)" || ok "aborts before cloud auth / apply"
rm -f "$ORDER"

# --- structural: node_up runs preflight_all BEFORE terraform apply ---
pfl="$(grep -n 'preflight_all' infra/scripts/node_up.sh | head -1 | cut -d: -f1)"
apl="$(grep -n 'terraform -chdir="${TF_DIR}" apply' infra/scripts/node_up.sh | head -1 | cut -d: -f1)"
if [ -n "$pfl" ] && [ -n "$apl" ] && [ "$pfl" -lt "$apl" ]; then
  ok "node_up calls preflight_all (line ${pfl}) before apply (line ${apl})"
else
  bad "node_up does not preflight before apply (preflight=${pfl} apply=${apl})"
fi

echo "preflight-guard: ${pass} ok, ${fail} fail"
[ "$fail" = 0 ]
