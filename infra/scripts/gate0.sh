#!/usr/bin/env bash
# gate0.sh — operator GATE-0 preflight. Validates EVERYTHING required before the
# paid GATE-PAID step WITHOUT creating a single cloud resource, and prints a
# PASS/FAIL table. It NEVER runs `terraform apply` / node_up / any billable op.
#
# SECRETS: the operator sets them in the environment; gate0 reports only PRESENCE
# and validation RESULT — never a value. Nothing secret is printed or committed.
# It creates two test users + subscriptions in the staging control-plane (not a
# paid op) and writes their tokens to a 0600 file under .runs/ (gitignored).
#
# Required env (operator-set, values never printed):
#   HCLOUD_TOKEN VULTR_API_KEY SSH_PUBKEY PAIR_PSK REALITY_SERVER_NAMES REALITY_DEST
#   CONTROL_PLANE_URL CONTROL_PLANE_TOKEN SUBSCRIPTION_URL TUNNEL_SUB_URL
#   REGION  (entry region for the plan)  TF_WORKSPACE  (non-default, isolated)
# Optional: HY2_SNI (+HY2_TLS_CERT/HY2_TLS_KEY), RUN_ID (generated if unset),
#   RUN_MAX_MINUTES (default 120), BUDGET_ALERTS_CONFIRMED=1, E2E_CONFIRMED=1.
# The gate_* checks are invoked indirectly (passed by name to `check`), which the
# linter cannot see; disable "never invoked" (SC2329) and "unreachable" (SC2317)
# file-wide (different shellcheck versions flag one or the other).
# shellcheck disable=SC2329,SC2317
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "${HERE}/lib.sh"
# shellcheck source=preflight.sh
source "${HERE}/preflight.sh"
# shellcheck source=run_manifest.sh
source "${HERE}/run_manifest.sh"
# shellcheck source=control_plane.sh
source "${HERE}/control_plane.sh"
ROOT="$(repo_root)"
# shellcheck source=../versions.sh
source "${ROOT}/infra/versions.sh"
TF_DIR="${ROOT}/${TF_ENV_DIR_DEFAULT}"
# The sourced libs enable `set -euo pipefail`; gate0 handles every failure
# EXPLICITLY (each check records PASS/FAIL, the table drives the exit code), so a
# no-match grep or a missing operator var must show as a row, never abort the run.
set +eu +o pipefail

ROWS=()
row() { ROWS+=("$1|$2"); }                 # STATUS|label
# check runs a command in a SUBSHELL (so a pf_* `die` can't exit gate0) with output
# suppressed (so a secret can never leak), and records PASS/FAIL.
check() { local label="$1"; shift; if ( "$@" ) >/dev/null 2>&1; then row PASS "$label"; else row FAIL "$label"; fi; }

# --- custom checks ------------------------------------------------------------
gate_cp_auth() {
  curl -fsS --connect-timeout 5 --max-time 15 "${CONTROL_PLANE_URL%/}/healthz" >/dev/null || return 1
  # an authenticated admin GET proves the token/role works
  curl -fsS --connect-timeout 5 --max-time 15 -H "Authorization: Bearer ${CONTROL_PLANE_TOKEN:?}" \
    "${CONTROL_PLANE_URL%/}/v1/nodes" >/dev/null
}
gate_sub_health() {
  curl -fsS --connect-timeout 5 --max-time 15 "${SUBSCRIPTION_URL%/}/healthz" >/dev/null
}
# The public tunnel must be routed to SUBSCRIPTION, not the control-plane. Two
# decisive checks, because CP and subscription both expose a public /healthz:
#   POSITIVE — a real /sub/<token> (from the token file gate_make_tokens wrote)
#     resolves; only subscription has /sub, so a tunnel misrouted to the CP 404s.
#   NEGATIVE — the CP admin API is NOT reachable through the tunnel EVEN WITH a
#     valid admin bearer (a 2xx on /v1/nodes with the admin token means the public
#     hostname is pointed at the control-plane — the exact leak this gate catches;
#     an auth-protected CP returns 401 to an unauthenticated probe, so the probe
#     must carry the token to be decisive).
# It reads the probe URL from the token FILE, not a variable, because each check
# runs in a subshell (a variable set in gate_make_tokens would not survive).
gate_tunnel() {
  curl -fsS --connect-timeout 5 --max-time 15 "${TUNNEL_SUB_URL%/}/healthz" >/dev/null || return 1
  local sub
  sub="$(jq -r '.[0].sub_url // empty' "$(run_dir)/${RUN_ID}.tokens.json" 2>/dev/null)"
  [ -n "$sub" ] || return 1
  curl -fsS --connect-timeout 5 --max-time 15 "$sub" >/dev/null || return 1
  local code
  code="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer ${CONTROL_PLANE_TOKEN:?}" \
    --connect-timeout 5 --max-time 15 "${TUNNEL_SUB_URL%/}/v1/nodes" 2>/dev/null || echo 000)"
  case "$code" in 2??) return 1 ;; *) return 0 ;; esac
}
# Read-only cloud credential + config check via terraform plan (NO apply) in the
# isolated workspace. The plan can carry sensitive attributes, so it is written
# 0600 like every other run artifact.
gate_plan() {
  require_env HCLOUD_TOKEN VULTR_API_KEY SSH_PUBKEY REGION
  terraform -chdir="${TF_DIR}" init -input=false >/dev/null 2>&1 || return 1
  tf_select_workspace "${TF_DIR}" >/dev/null 2>&1 || return 1
  local pf; pf="$(run_dir)/${RUN_ID}.plan.txt"
  ( umask 077; terraform -chdir="${TF_DIR}" plan -input=false \
      -var "ssh_pubkey=${SSH_PUBKEY}" -var "entry_region=${REGION}" >"$pf" 2>&1 )
  local rc=$?; chmod 600 "$pf" 2>/dev/null; return "$rc"
}
# Create two test users + subscriptions; save tokens to a 0600 file. Tokens are
# NEVER echoed — only the file path is reported. Telegram ids are unique PER RUN so
# a fix-a-row-and-re-run never collides on the UNIQUE(telegram_id) constraint.
gate_make_tokens() {
  require_env CONTROL_PLANE_URL CONTROL_PLANE_TOKEN TUNNEL_SUB_URL
  mkdir -p "$(run_dir)"; local out; out="$(run_dir)/${RUN_ID}.tokens.json"
  local base; base="$(date -u +%s 2>/dev/null || echo 0)"
  local acc="[]" i uid sub tok tg url
  for i in 1 2; do
    tg="$(( base * 10 + i ))"
    uid="$(curl -fsS -X POST "${CONTROL_PLANE_URL%/}/v1/users" \
      -H "Authorization: Bearer ${CONTROL_PLANE_TOKEN}" -H 'Content-Type: application/json' \
      -d "{\"telegram_id\":${tg}}" | jq -re .id)" || return 1
    local subjson
    subjson="$(curl -fsS -X POST "${CONTROL_PLANE_URL%/}/v1/subscriptions" \
      -H "Authorization: Bearer ${CONTROL_PLANE_TOKEN}" -H 'Content-Type: application/json' \
      -d "{\"user_id\":\"${uid}\",\"plan\":\"basic\"}")" || return 1
    sub="$(jq -re .id <<<"$subjson")" || return 1
    tok="$(jq -re .token <<<"$subjson")" || return 1
    url="${TUNNEL_SUB_URL%/}/sub/${tok}"
    acc="$(jq -c --arg u "$uid" --arg s "$sub" --arg url "$url" \
      '. += [{user_id:$u, subscription_id:$s, sub_url:$url}]' <<<"$acc")"
  done
  ( umask 077; printf '%s\n' "$acc" >"$out" ); chmod 600 "$out"
}

# --- run identity -------------------------------------------------------------
: "${RUN_ID:=run-$(date -u +%Y%m%d-%H%M%S 2>/dev/null || echo manual)}"
export RUN_ID
# Validate RUN_ID before it is used in any artifact path (a '/' or '..' would
# redirect writes outside .runs/).
case "$RUN_ID" in
  *[!A-Za-z0-9._-]*) echo "RUN_ID must match [A-Za-z0-9._-]" >&2; exit 2 ;;
esac
: "${TF_WORKSPACE:=}"
: "${RUN_MAX_MINUTES:=120}"
mkdir -p "$(run_dir)"; chmod 700 "$(run_dir)" 2>/dev/null

echo "GATE-0 preflight — run ${RUN_ID}"

# 1. secrets present + shape (pf_* die on failure; run in subshells)
check "secrets present (cloud tokens, SSH, PSK, REALITY, CP token)" pf_require_present
check "binaries + sing-box pin present" pf_binaries
check "SSH_PUBKEY parses" pf_ssh_pubkey
check "PAIR_PSK is a 32-byte SS2022 key" pf_pair_psk
check "REALITY_DEST speaks TLS 1.3" pf_reality_tls13
if [ -n "${HY2_SNI:-}" ]; then
  check "HY2 cert/key match + SAN covers HY2_SNI" pf_hy2_tls
else
  row SKIP "HY2 disabled (HY2_SNI unset)"
fi

# 2. staging services. Tokens are created BEFORE the tunnel check, which uses a
#    real /sub/<token> to prove the tunnel is routed to subscription (not the CP).
check "control-plane health + auth" gate_cp_auth
check "subscription health" gate_sub_health
check "2 test users + subscriptions created; tokens saved 0600" gate_make_tokens
check "public tunnel serves /sub, hides CP admin API" gate_tunnel

# 3. run identity: isolated workspace + deadline (written 0600, no secrets)
if [ -n "${TF_WORKSPACE}" ] && [ "${TF_WORKSPACE}" != "default" ]; then
  RUN_DEADLINE_UTC="$(date -u -d "+${RUN_MAX_MINUTES} min" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null \
    || date -u -v "+${RUN_MAX_MINUTES}M" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo unknown)"
  ( umask 077; printf 'RUN_ID=%s\nTF_WORKSPACE=%s\nRUN_DEADLINE_UTC=%s\nRUN_MAX_MINUTES=%s\n' \
      "$RUN_ID" "$TF_WORKSPACE" "$RUN_DEADLINE_UTC" "$RUN_MAX_MINUTES" >"$(run_dir)/${RUN_ID}.env" )
  chmod 600 "$(run_dir)/${RUN_ID}.env"
  row PASS "RUN_ID / non-default TF_WORKSPACE / RUN_DEADLINE_UTC (${RUN_DEADLINE_UTC})"
else
  row FAIL "TF_WORKSPACE must be set and non-default (isolated per-run state)"
fi

# 4. read-only cloud creds + terraform plan (NO apply)
check "cloud creds + terraform plan (read-only, no apply)" gate_plan

# 5. provider budget alerts — operator attestation (can't be read via API)
if [ "${BUDGET_ALERTS_CONFIRMED:-0}" = "1" ]; then
  row PASS "provider budget alerts confirmed (operator-attested)"
else
  row FAIL "provider budget alerts NOT confirmed — set BUDGET_ALERTS_CONFIRMED=1 after enabling them"
fi

# 6. docker/REALITY e2e — operator attestation (need docker + REALITY_DEST)
if [ "${E2E_CONFIRMED:-0}" = "1" ]; then
  row PASS "Docker/REALITY e2e green (operator-attested)"
else
  row FAIL "Docker/REALITY e2e not confirmed — run them, then set E2E_CONFIRMED=1"
fi

# --- table --------------------------------------------------------------------
echo
echo "  RESULT  CHECK"
echo "  ------  -----"
p=0; f=0; s=0
for r in "${ROWS[@]}"; do
  st="${r%%|*}"; lb="${r#*|}"
  printf '  [%-4s]  %s\n' "$st" "$lb"
  case "$st" in PASS) p=$((p+1));; FAIL) f=$((f+1));; SKIP) s=$((s+1));; esac
done
echo
echo "Docker/REALITY e2e — run these (opt-in; need docker + your REALITY_DEST), then set E2E_CONFIRMED=1:"
echo "  make e2e-first-user"
echo "  REALITY_DEST=<host:443> REALITY_SERVER_NAME=<host> make e2e-real-node"
echo "  REALITY_DEST=<host:443> REALITY_SERVER_NAME=<host> make e2e-transport-probe"
echo "  make e2e-reconcile   # optional: full CP->data-plane loop"
echo
echo "GATE-0: ${p} PASS / ${f} FAIL / ${s} SKIP"

if [ "$f" -eq 0 ]; then
  echo
  echo "==================== GATE-0 PASS ===================="
  echo "Terraform plan (read-only) saved: $(run_dir)/${RUN_ID}.plan.txt"
  echo "Tokens (0600): $(run_dir)/${RUN_ID}.tokens.json    Run env (0600): $(run_dir)/${RUN_ID}.env"
  echo
  echo "Planned resources (reference env — entry Hetzner, exit Vultr):"
  grep -E '# .* will be created|resource \"' "$(run_dir)/${RUN_ID}.plan.txt" 2>/dev/null | sed 's/^/  /' | head -20
  grep -E '^Plan: ' "$(run_dir)/${RUN_ID}.plan.txt" 2>/dev/null | sed 's/^/  /'
  echo
  echo "  entry: hcloud_server (${REGION}, size cx22)      ~ EUR 4/mo"
  echo "  exit : vultr_instance (exit region, vc2-1c-1gb)  ~ USD 5/mo"
  echo "  workspace: ${TF_WORKSPACE}"
  echo "  DESTROY (teardown): RUN_ID=${RUN_ID} make node-down"
  echo
  echo "GATE-PAID is a SEPARATE, explicit approval. Do NOT run node-up until granted."
  exit 0
else
  echo "GATE-0 FAILED — fix the FAIL rows above. No paid step until GATE-0 is all PASS."
  exit 1
fi
