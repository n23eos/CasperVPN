# Implementation Plan: service polish

Date: 2026-10-06. Spec: spec.md. Current phase: complete locally, ready for review.

## Decisions and architecture

Preserve Go workspace and PostgreSQL, use Go 1.27.1 via PATH. Spec Kit 1.0.5 installed from existing CLI, staged safely and copied only new .specify/.agents files. Existing code and OpenSpec were not regenerated.

Billing adds private reads. Delivery binds account ID to Telegram sender, reads billing and CP for status, emits existing Happ import format and uses durable notification state. Replies use Telegram ReplyKeyboardMarkup to avoid callback identity complexity. Operator dashboard uses standard-library loopback Python server with server-side API credentials and safe projection. Public website stays static, with useful instructions and explicit pilot status.

## Ownership

- Root: services/control-plane operator summary, packages/contracts, specs/.specify, Makefile/release checks, integration E2E, production env/runbook integration, status card, final verification.
- Billing agent: services/billing only.
- Delivery agent: services/delivery only.
- Website agent: docs/index.html, docs/ru.html, docs/style.css, docs/tokens.css, new public docs/setup pages.
- Operations agent: web/admin, scripts/operator.py, scripts/maintenance.py, test/ops, new infra/systemd operator/backup/monitor files, docs/OPERATIONS.md.
- Final reviewer after a worker completes: read-only independent review; fixes remain with owning worker/root.

## Shared billing read API

GET /v1/plans -> {items:[{id,duration_seconds,grace_seconds,prices:{CURRENCY:decimal-string}}]}.
GET /v1/accounts/{anon_user_id}/invoices/latest -> {invoice_id,plan,status,amount,currency,created_at,expires_at}, 404 if none.
GET /v1/operator/summary -> {counts:{pending,settled,expired,invalid},recent:[{invoice_id,anon_user_id,plan,status,amount,currency,created_at,expires_at}]}, recent max 50, stable sort.
All new billing reads require configured InvoiceToken in production as creation does. No provider payment addresses, gateway order IDs or checkout URLs in read surfaces.

## Validation

Run worker-focused tests, then full make release-check with cached Go plus existing lint/vulnerability tools. Extend isolated fake Telegram/PG E2E for actual new menu/status/link behavior. Run Python ops tests and visually exercise local RU/EN site and dashboard at desktop and mobile width via cua_repl. Inspect diff, secrets, contracts and literal forbidden characters. Update this same plan/tasks and canonical project card.

## Completed checks

2026-10-06: new contracts and CP operator endpoint race tests passed; admin RBAC, secret projection, failure sanitization, stable ordering/limit and memory snapshot isolation verified. PostgreSQL integration remains in the full gate.

## Blockers

None for local implementation. Missing real DNS/Telegram/BTCPay inputs affect only production pilot.

## Next step

Review local changes and approve deployment separately; follow docs/LAUNCH.md and docs/OPERATIONS.md for a controlled live pilot.

2026-10-06: isolated PostgreSQL integration and Telegram menu/catalog/payment/status/Happ/restart/restore scenario passed. Browser verified RU/EN landing/setup at 375, 768 and 1440 widths and operator overview against five real isolated services, refresh and safe failure states. Independent security review findings corrected: redirects rejected, automated restore DB cleanup, orchestrator opt-in, durable pending notifications, bounded concurrent overview. Full build/vet/race passed. CP goimports formatting corrected; release gate resumed at lint, all module lint stages passed.

2026-10-06 final: all local release stages passed after correcting CP import formatting and resuming at lint. PostgreSQL outbox persistence/restart assertions ran (not skipped), Telegram E2E passed with latest binaries. 13 launch and 13 ops tests passed. Real sing-box1.14.2 revoke check passed on both transports. govulncheck: zero reachable vulnerabilities; subscription imported packages carry two findings with no reachable call path. OpenSpec launch-readiness validation passed. JSON Schema checked against actual API responses. Gitleaks changed-file scan and diff check passed after disposable protocol fixture cleanup. Linux systemd runtime, production destination/mount, target client/device/network, DNS/BTCPay/Telegram remain outside local acceptance. Runtime acceptance did not publish Git changes or deploy services.

2026-10-06 publication: owner requested committing and pushing this verified change to origin/main. Runtime behavior is unchanged; publication checks cover diff, ignored private files, source secrets and upstream history.
