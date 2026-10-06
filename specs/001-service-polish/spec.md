# Feature Specification: CasperVPN service polish

Created: 2026-10-06. Status: Implemented and locally verified.
Input: User requested implementing all six product-audit improvements with multiple agents.

## Existing structure

Eight Go modules: contracts, platform and six services. PostgreSQL owns account, invoice and bot-update durability. Telegram has private /start, /pay and /get. Billing activation and subscription token persistence already exist. Website is static docs/index.html and docs/ru.html. web/admin is a placeholder. Production launch is scripts/launch.py. Previous launch-readiness remains in OpenSpec; only this new change uses Spec Kit.

## User scenarios

### US1: Choose, pay, understand status (P1)

A private-chat user sees Russian buttons and current tariff prices/duration from billing, chooses a supported currency, creates an invoice, checks payment and sees current subscription expiry. Existing commands remain supported. A suspended user cannot import an account. Pending, expired, invalid and unavailable conditions have useful next actions.

Acceptance: changing catalog prices changes bot offers; different sender cannot inspect another user's invoice; webhook replay does not add time; paid status does not claim usable access until CP confirms eligibility.

### US2: Connect an external app (P1)

A paid user receives their unchanged subscription URL and existing Happ-compatible import URI, plus device-specific instructions and configurable installation/support URLs. Telegram buttons must follow Telegram URL constraints; custom happ URI can be displayed as copyable text or reached through a safe web flow.

Acceptance: stable link survives restart; another account and group cannot obtain it; link includes configured public base and no shared node credentials.

### US3: Renewal and activation feedback (P2)

A background delivery notifier checks opt-in private bot users, notifies confirmed new payment activation and impending expiry/grace, persists notification progress and retries failures without losing the message. Notifications use existing billing/CP data and are disabled when bot is disabled.

Acceptance: duplicate runs and restart do not repeat successfully recorded notifications; unavailable upstream does not produce false expiry/activation. Exactly-once external delivery is not promised across a crash after send and before recording.

### US4: Understand the website (P1)

Both languages explain actual supported capability and pilot status. Compact hero preserves mascot/palette. A clear CTA leads to working local connection instructions, device selection, help and requirements. No invented price, bot handle, supported app version or unconditional DPI claim. Public pilot CTA must not impersonate an already open sales service.

Acceptance: navigable on desktop and phone width, usable without JavaScript for core instructions, correct internal links and keyboard focus.

### US5: Operate and recover (P2)

Local private operator dashboard shows health, fleet, safe account/subscription summary and billing status. Service secrets stay server-side. Commands support encrypted external backup, retention and restore verification on a configurable schedule. Monitoring generates local transition alerts; actual outbound destinations are configured separately and not contacted in this task.

Acceptance: server binds loopback; untrusted Origin/Host rejected; upstream failure visible; no raw user/node credential payload reaches browser; encryption failure keeps original backups; restore verifies isolated DB and preserves production.

## Requirements

FR-001: authenticated additive billing catalog/latest-invoice/operator-summary routes with bounded results.
FR-002: Russian bot navigation and tariff selection using live catalog, status and safe errors.
FR-003: stable external-client import and installation instructions.
FR-004: durable background activation and renewal notifications under private identity binding.
FR-005: accurate compact EN/RU website without invented commercial input.
FR-006: private operator overview and locally verifiable monitoring/backup automation.
FR-007: original money/access invariants and eight-module release-check remain passing.

## Edge cases

Empty catalog, unsupported currency, awaiting confirmations, settlement pending CP delivery, old settled invoice after expiry, grace, suspended user, no nodes, bot send timeout, restart after successful send, timer overlap, stale backup, unavailable service, unsafe backup paths and malicious upstream text.

## Success criteria

SC-001: fake Telegram E2E covers menu -> tariff -> invoice -> pending -> paid -> import -> expiry/error, with persisted identity and no double credit.
SC-002: modified modules pass race/vet/lint and PostgreSQL integration; release-check passes.
SC-003: website and private dashboard visually checked in desktop/mobile view with real local scenario.
SC-004: backup/monitor tests verify failure paths and a local restore rehearsal succeeds where Docker is available.

## Boundaries

No production deploy or real purchases/messages. Cloud/DPI/real-phone acceptance remains a separately authorized live pilot. Quotas and new protocols are outside this polish change.
