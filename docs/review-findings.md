# Pre-merge review findings — PR #1 (`fix/audit-remediation`)

Independent final review before merging PR #1, 2026-07-17. **No CRITICAL issues; contracts clean (no drift).** One HIGH was a real billing data-integrity regression (fixed in this branch); everything else is tracked below and does not block a PR that keeps nodes/orchestrator inactive.

Each open item is filed as a GitHub issue; this table is the durable in-repo index.
Issue map: finding #2 → [#3](../../issues/3), #3 → [#4](../../issues/4), #4 → [#5](../../issues/5),
#5 → [#6](../../issues/6), #6 → [#7](../../issues/7), #7 → [#8](../../issues/8). Finding #1 fixed in this branch.

| # | Sev | Area | File | Status |
|---|-----|------|------|--------|
| 1 | HIGH | billing settle: non-transactional claim→activate→status → "paid but never activated" on crash | `services/billing/internal/payment/processor.go`, `store/postgres.go` | **FIXED (this branch)** — durable reconciliation |
| 2 | HIGH (latent) | orchestrator TCP-only prober reports DPI-blocked node Healthy, poisons feedback loop | `services/orchestrator/internal/probe/probe.go:70`, `reconcile/reconcile.go:178` | Gated off (`PROBE_ENABLED=false`, dry-run) → **PR #2 acceptance** |
| 3 | MED | billing per-user activation lock is in-process only → two instances double-create | `services/billing/internal/subscription/activator.go` | **Ticket — block real payments** |
| 4 | MED | crypto confirmation lag vs expiry sweep can drop on-time payments | `services/billing/internal/payment/poller.go`, `onchain/onchain.go` | **Ticket — block real payments** |
| 5 | MED | control-plane subscription create is a non-transactional two-write → orphan sub | `services/control-plane/internal/usecase/subscriptions.go` | **Ticket** |
| 6 | MED | orchestrator replace-then-drain can orphan/duplicate replacement nodes | `services/orchestrator/internal/reconcile/reconcile.go:219` | **PR #2 acceptance** |
| 7 | MED | orchestrator non-dry-run config doesn't require auth tokens → unauthenticated mutations | `services/orchestrator/internal/config/config.go:120` | **PR #2 acceptance** |
| — | LOW | rotate-token non-tx; `/tmp/*.$$` predictable temp files (use mktemp); argv secret-exposure comment overstates; orchestrator swallowed retire error + no graceful drain | various | Noted; opportunistic |

## Finding #1 — the fix (durable settlement reconciliation)

**Regression:** `settle()` ran three non-transactional steps — `ClaimSettlement` (commits) → remote `Activate` → `SetInvoiceStatus(settled)`. A crash after the claim commit but before activation left the invoice `pending` forever (the re-claim returns `false`, so a retry no-ops), and `sweepExpired` then buried it as `expired`. Money captured, no service, undetected. The in-memory store lost the claim on restart and self-healed; the new Postgres store made the claim durable and thus the gap permanent.

**Fix (billing-only, no contract change):**
- `settlements` gains `claimed_at`, `activated_at`, `reconcile_leased_until`.
- `Processor.Reconcile` runs each poll cycle **before** expiry: it leases claimed-but-unsettled invoices older than a threshold via `FOR UPDATE SKIP LOCKED` (cross-process safe — two reconcilers never touch the same invoice), and for each: if activation was already applied it only flips status (no second period), else it activates once, marks it, then flips.
- `ExpireOverdue` replaces the per-invoice sweep with one atomic `UPDATE … WHERE … NOT EXISTS (settlements)` so a paid invoice mid-recovery is never expired.
- `SETTLEMENT_STALE_THRESHOLD` (default 2m, must exceed the activation HTTP timeout) gates recovery latency only, never correctness.

**Tests:** heal-before-activate, settled-noop, age-gate, transient-retry, concurrent-single-term, crash-after-activate-no-double-period, expire-skips-claimed, one-failure-doesn't-block-batch (memory + Postgres integration incl. real `SKIP LOCKED` exclusivity under concurrency).

**Residual (documented):** the exactly-once boundary across the remote `Activate` call is inherent — a crash between the control-plane period-commit and the local `activated_at` mark could re-extend on retry. Fully closing it needs a control-plane idempotency key (frozen-contract change); tracked with #3/#4 as a pre-real-payment hardening.
