#!/usr/bin/env bash
# gate0-guard.sh — GATE-0 preflight orchestrator: PASS/FAIL logic, tunnel-surface
# leak detection, secret hygiene (values never printed), 0600 token file, and the
# no-paid-op guarantee. Pure shell: network/cloud/tls primitives are stubbed.
set -uo pipefail
cd "$(dirname "$0")/../.."

pass=0; fail=0
ok()  { echo "  ok: $1"; pass=$((pass+1)); }
bad() { echo "  FAIL: $1" >&2; fail=$((fail+1)); }
perms() { stat -c '%a' "$1" 2>/dev/null || stat -f '%Lp' "$1"; }

BIN="$(mktemp -d)"
cat >"$BIN/terraform" <<'SH'
#!/usr/bin/env bash
sub=""; for a in "$@"; do case "$a" in -chdir=*|-*) ;; *) sub="$a"; break;; esac; done
[ "$sub" = plan ] && echo "Plan: 4 to add, 0 to change, 0 to destroy."
exit 0
SH
cat >"$BIN/openssl" <<'SH'
#!/usr/bin/env bash
exit 0
SH
cat >"$BIN/timeout" <<'SH'
#!/usr/bin/env bash
shift; exec "$@"
SH
for t in ansible ansible-playbook; do printf '#!/usr/bin/env bash\nexit 0\n' >"$BIN/$t"; done
cat >"$BIN/curl" <<'SH'
#!/usr/bin/env bash
args="$*"
if [[ "$args" == *"%{http_code}"* ]]; then
  if [[ "$args" == *"tunnel.local/v1/nodes"* ]]; then echo "${FAKE_TUNNEL_ADMIN_CODE:-403}"; else echo 200; fi
  exit 0
fi
if [[ "$args" == *"/v1/users"* ]]; then echo '{"id":"u-1"}'; exit 0; fi
if [[ "$args" == *"/v1/subscriptions"* ]]; then echo '{"id":"s-1","token":"SECRET-TOKEN-XYZ"}'; exit 0; fi
exit 0    # healthz / authed cp /v1/nodes
SH
chmod +x "$BIN"/*
export PATH="$BIN:$PATH"

KD="$(mktemp -d)"; ssh-keygen -t ed25519 -N '' -f "$KD/id" >/dev/null 2>&1
GOODENV=(
  RUN_DIR="$(mktemp -d)" REGION=hel1 TF_WORKSPACE=ws-gate0-test
  CONTROL_PLANE_URL=http://cp.local CONTROL_PLANE_TOKEN=cptok
  SUBSCRIPTION_URL=http://sub.local TUNNEL_SUB_URL=http://tunnel.local
  HCLOUD_TOKEN=htok VULTR_API_KEY=vtok
  SSH_PUBKEY="$(cat "$KD/id.pub")" PAIR_PSK="$(head -c 32 /dev/zero | base64)"
  REALITY_SERVER_NAMES=sni.test REALITY_DEST=sni.test:443
  BUDGET_ALERTS_CONFIRMED=1 E2E_CONFIRMED=1 RUN_ID=gate0-test
)
run_gate0() { env "${GOODENV[@]}" "$@" bash infra/scripts/gate0.sh 2>&1; }

# --- A. all-good => GATE-0 PASS, exit 0 ---
out="$(run_gate0)"; rc=$?
if [ "$rc" -eq 0 ] && grep -q 'GATE-0 PASS' <<<"$out"; then ok "all-good run reaches GATE-0 PASS"; else bad "all-good did not PASS (rc=$rc): $out"; fi
grep -q 'DESTROY (teardown): RUN_ID=gate0-test make node-down' <<<"$out" && ok "prints the destroy command" || bad "no destroy command"
grep -q 'GATE-PAID is a SEPARATE' <<<"$out" && ok "does not auto-proceed to paid" || bad "missing GATE-PAID gating notice"

# --- B. secret hygiene: values NEVER printed ---
grep -qF "SECRET-TOKEN-XYZ" <<<"$out" && bad "a subscription token leaked to stdout" || ok "subscription token never printed"
psk="$(head -c 32 /dev/zero | base64)"; grep -qF "$psk" <<<"$out" && bad "PAIR_PSK leaked to stdout" || ok "PAIR_PSK never printed"

# --- C. token file exists, 0600, and DOES hold the token (the file, not stdout) ---
TOKENS="$(mktemp -d)/x"   # placeholder; locate the real file from the last RUN_DIR
rd="$(grep -oE 'Tokens \(0600\): [^ ]+' <<<"$out" | awk '{print $3}')"
if [ -n "$rd" ] && [ -f "$rd" ]; then
  [ "$(perms "$rd")" = "600" ] && ok "token file is 0600" || bad "token file perms $(perms "$rd")"
  grep -qF "SECRET-TOKEN-XYZ" "$rd" && ok "token stored in the 0600 file" || bad "token not saved"
else
  bad "token file not found ($rd)"
fi

# --- D. tunnel surface leak: CP admin reachable via tunnel => FAIL, exit 1 ---
out="$(run_gate0 FAKE_TUNNEL_ADMIN_CODE=200)"; rc=$?
if [ "$rc" -ne 0 ] && grep -qi 'FAIL.*hides CP admin' <<<"$out"; then ok "tunnel exposing CP admin => GATE-0 FAIL"; else bad "tunnel leak not caught (rc=$rc)"; fi

# --- E. default workspace => FAIL ---
out="$(env "${GOODENV[@]}" TF_WORKSPACE=default bash infra/scripts/gate0.sh 2>&1)"; rc=$?
[ "$rc" -ne 0 ] && grep -qi 'must be set and non-default' <<<"$out" && ok "default workspace => FAIL" || bad "default workspace accepted"

# --- F. unconfirmed budget alerts => FAIL ---
out="$(env "${GOODENV[@]}" BUDGET_ALERTS_CONFIRMED=0 bash infra/scripts/gate0.sh 2>&1)"; rc=$?
[ "$rc" -ne 0 ] && grep -qi 'budget alerts NOT confirmed' <<<"$out" && ok "unconfirmed budget => FAIL" || bad "budget gate not enforced"

# --- G. it NEVER runs a paid op (no 'apply' in the terraform stub calls) ---
cat >"$BIN/terraform" <<'SH'
#!/usr/bin/env bash
for a in "$@"; do case "$a" in apply) echo "PAID-APPLY-INVOKED" >&2; exit 3;; esac; done
sub=""; for a in "$@"; do case "$a" in -chdir=*|-*) ;; *) sub="$a"; break;; esac; done
[ "$sub" = plan ] && echo "Plan: 4 to add."
exit 0
SH
chmod +x "$BIN/terraform"
out="$(run_gate0)"; grep -q 'PAID-APPLY-INVOKED' <<<"$out" && bad "gate0 invoked terraform apply" || ok "gate0 never runs terraform apply"

rm -rf "$BIN" "$KD"
echo "gate0-guard: ${pass} ok, ${fail} fail"
[ "$fail" = 0 ]
