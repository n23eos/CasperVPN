# CasperVPN constitution

Version: 1.0.0. Ratified: 2026-10-06. Source: existing AGENTS.md, CLAUDE.md and user instructions.

- Preserve the existing eight-module Go workspace, PostgreSQL and external-client architecture. New operator tooling uses Python standard library and static HTML/CSS/JS.
- Extend shared contracts additively with matching Go, JSON Schema and OpenAPI. Do not break existing fields, identity binding, payment idempotency or access revocation.
- Keep APIs private and fail closed. Browser responses must never contain service tokens, Telegram identities, personal access credentials or payment addresses. Operator UI binds to loopback and rejects cross-origin writes.
- No production publication, cloud provisioning, paid actions or actual message sending during local acceptance. Test Telegram through an isolated fake API.
- Keep all endpoints and external addresses configurable. Preserve per-user VLESS and Hysteria2 and the internal entry/exit link.
- Use repository checks and meaningful regression tests. Visually inspect changed screens and preserve !notes exclusion.
- Capture new changes in specs, plan and tasks; keep prior OpenSpec launch-readiness as the historical record.
- Russian user-facing text; no U+2013 or U+2014 in authored files.
