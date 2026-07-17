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

## Re-review of the fix (2026-07-17)

Independent re-review of `7a3783b..b3d1241` confirmed the original crash window is closed (recovery + never-buried), no CRITICAL, SQL/lease/mirroring sound. It surfaced one **residual HIGH** and further items:

- **HIGH (fixed, follow-up commit):** in the live `settle` path a `MarkSettlementActivated` write error returned before the status flip, leaving `pending`+`activated_at=NULL` → recovery re-activated → **double period**. Reachable by a single transient DB error, not only a crash. Fix: marker is now best-effort and `status=settled` is the durable guard (both `settle` and `finishSettlement`); regression test `TestSettle_MarkFailureStillSettlesNoDoublePeriod`.
- **MED [#9]:** recovery failures are silent — a permanently-stuck claimed invoice is invisible (no log/alert). Ticketed.
- **MED [#10]:** the real `FOR UPDATE SKIP LOCKED` exclusivity is only proven against a live Postgres; CI's store tests skip without a DB. Ticketed (add a Postgres service to build-test).
- **LOW (documented inline):** `LeaseFor` must exceed worst-case `finishSettlement` wall time (comment in `poller.go`); the migration back-stamps legacy `claimed_at` (note in `schema.sql`).

---

# Pre-merge review — PR #2 (`feat/node-reconciler`, CP→node reconciler)

Independent final review before merging PR #2, 2026-07-17. **No CRITICAL; contracts (Go/Schema/OpenAPI) consistent.** Core machinery verified: guarded `/activate` is the only provisioning→active path, SERIALIZABLE+`FOR UPDATE` allow-list gate with a 25-iteration eligibility-race test, exit-first/entry-last rollback, signal traps terminate (130/143), fail-closed probe (≥2 distinct types), secrets (REALITY key / PAIR_PSK / hy2 TLS) kept out of CP/client/logs, and a capstone e2e that drives the real state machine (a banned user's VLESS actually dies).

| Sev | Finding | Status |
|-----|---------|--------|
| HIGH | `NodeService.Update` guarded only `provisioning→active`; plain PATCH `retired/draining/degraded/blocked→active` bypassed all gates (resurrection stronger than `Demote` forbids) | **FIXED** — reject every non-active→active PATCH; table-driven tests |
| MED | reconciler `_cp` had no HTTP timeout → hung CP wedges the run holding the pair lock | **FIXED** — bounded connect/total timeout; timeout→rollback→lock release; new state-guard case |
| MED | node-up hy2 `set_fact` folded the durable password without `no_log` | **FIXED** — `no_log: true` (matches keygen tasks) |
| LOW | mkdir-lock not released on SIGKILL (dev/macOS) | **Ticket [#11]** — deferred to avoid pre-merge scope creep |
| LOW | exit `evidence` is a static string, not crypto proof | **Documented** — trusted-attestation comment in `reality_users.go` |

**Static/molecule check for hy2 `no_log` (considered, not added):** `no_log` hides task output, so a molecule assertion can't observe it, and a YAML-unaware grep over task bodies is false-positive/negative-prone. Coverage rests on review + the now-consistent `no_log` across all hy2-password tasks; revisit if a robust ansible-lint rule becomes available.
