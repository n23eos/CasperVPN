# GATE-0 preflight (operator)

GATE-0 validates every input the first live VPS run needs **before** the paid
`node-up` step, and creates **no cloud resource**. It is `infra/scripts/gate0.sh`
(`make gate0`). It prints only PRESENCE and validation RESULT — **never a secret
value**. Secrets stay in your shell / a protected env file; nothing secret is
printed or committed.

## What it checks

| Check | How |
|-------|-----|
| Secrets present + shape | cloud tokens, SSH key parses, `PAIR_PSK` = 32-byte SS2022 key, `REALITY_DEST` speaks TLS 1.3, HY2 cert/key match + not expired + SAN covers `HY2_SNI` |
| Control-plane | `/healthz` + an authenticated admin GET |
| Subscription | `/healthz` |
| Public tunnel surface | Cloudflare Tunnel serves `/sub`/health **and hides the CP admin API** (a 2xx on an admin path through the tunnel is a FAIL) |
| Cloud creds + config | read-only `terraform plan` in the isolated workspace — **no apply** |
| Run identity | generates `RUN_ID`, requires a non-default `TF_WORKSPACE`, sets `RUN_DEADLINE_UTC`; written 0600 |
| Budget alerts | operator attests `BUDGET_ALERTS_CONFIRMED=1` (can't be read via API) |
| Test users | creates 2 users + subscriptions in staging CP; saves tokens to a **0600** file under `.runs/` (gitignored) |
| Docker/REALITY e2e | you run them (below), then attest `E2E_CONFIRMED=1` |

## Run it

Set secrets in your shell or a protected env file (values never go to chat/logs/commits):

```sh
export HCLOUD_TOKEN=... VULTR_API_KEY=... SSH_PUBKEY="ssh-ed25519 ..."
export PAIR_PSK=...                         # base64, decodes to 32 bytes
export REALITY_SERVER_NAMES=... REALITY_DEST=host:443
export HY2_SNI=... HY2_TLS_CERT=/path HY2_TLS_KEY=/path   # optional (hysteria2)
export CONTROL_PLANE_URL=http://127.0.0.1:8081 CONTROL_PLANE_TOKEN=...
export SUBSCRIPTION_URL=http://127.0.0.1:8082
export TUNNEL_SUB_URL=https://<your-staging-tunnel-host>
export REGION=hel1 TF_WORKSPACE=run-$(date -u +%Y%m%d-%H%M%S)
export RUN_MAX_MINUTES=120                  # hard teardown deadline window

# Docker/REALITY e2e (opt-in; need docker + your REALITY_DEST):
make e2e-first-user
REALITY_DEST=$REALITY_DEST REALITY_SERVER_NAME=${REALITY_DEST%%:*} make e2e-transport-probe
REALITY_DEST=$REALITY_DEST REALITY_SERVER_NAME=${REALITY_DEST%%:*} make e2e-reconcile
export E2E_CONFIRMED=1

# After enabling provider budget alerts in both dashboards:
export BUDGET_ALERTS_CONFIRMED=1

make gate0
```

## Outcome

- **Any FAIL** → GATE-0 blocks; fix the row. No paid step.
- **All PASS** → gate0 prints the read-only terraform plan (the two VMs), the
  workspace, the reference cost (entry Hetzner cx22 ~EUR 4/mo, exit Vultr
  vc2-1c-1gb ~USD 5/mo), and the `RUN_ID=<id> make node-down` teardown command.

**GATE-PAID is a separate, explicit approval.** Do NOT run `make node-up` until it
is granted. The Cloudflare Tunnel publishes only the subscription origin; the CP
admin API is never exposed. See [`live-run-runbook.md`](./live-run-runbook.md) for
the complete operator sequence, stop gates, evidence template, and teardown.
