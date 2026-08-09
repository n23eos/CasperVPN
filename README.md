# CasperVPN

**A subscription-based, DPI-censorship-resistant VPN platform.** Backend for
running a commercial VPN service that survives state-level traffic filtering
(Russia's TSPU/RKN-class DPI): traffic mimics allowed HTTPS/HTTP-3, nodes
rotate automatically when blocked, and a feedback loop turns "what got blocked
where" into new configs and domains.

Survivability comes from **transport diversity and feedback**, not "stronger
encryption": multiple protocols per node, fast rotation, and clients that
switch — not a single protocol monoculture.

- Core on every node — **sing-box** ([ADR-002](docs/decisions/ADR-002-singbox-core.md)).
- Clients are **external** (Happ / any sing-box-compatible app) via per-user
  subscription URL; we don't ship our own apps ([ADR-003](docs/decisions/ADR-003-external-clients-happ.md)).
- Architecture source of truth — [`architecture.md`](architecture.md); contracts —
  [`docs/contracts.md`](docs/contracts.md); decisions — [`docs/decisions/`](docs/decisions/).

**Website** — [n23eos.github.io/CasperVPN](https://n23eos.github.io/CasperVPN/)
([по-русски](https://n23eos.github.io/CasperVPN/ru.html)).

Русская версия — [README.ru.md](README.ru.md).

## License

**Dual-licensed:**

- **AGPLv3** ([LICENSE](LICENSE)) — free, including commercial use, but if you
  run a modified version as a network service you must open-source it under
  the same terms.
- **Commercial license** — for closed-source / SaaS use without AGPL
  obligations. See [COMMERCIAL.md](COMMERCIAL.md) or email
  <mns.nicholas@gmail.com>.

## The [ANTI-BLOCK] requirement (applies to every service)

1. **Many transports at once, no monoculture** — a node carries several
   `Transport`s; the client switches (VLESS-REALITY / Hysteria2 / AmneziaWG /
   Shadowsocks-2022).
2. **Fast rotation and entry ≠ exit** — attacking an entry node must not
   expose the exit.
3. **Per-user isolation** — personal `reality_short_id`/`uuid`/keys.
   ⚠️ Currently enforced only for **VLESS-REALITY**; hysteria2 /
   shadowsocks-2022 / amnezia-wg still carry node-level (shared) credentials
   (see `architecture.md`, `docs/wave-2/`).
4. **Feedback loop** — anonymous `FieldSignal` + `HealthEvent` turn "blocked
   where" into new configs/domains.
5. **Zero hardcode** — no mimicry domain and no IP in code; config/DB only.

## Services

Go workspace monorepo (`go.work`): `packages/contracts` + 6 services. Each is
a separate module `github.com/caspervpn/<name>`.

| Service | Port (dev) | Role |
|---------|-----------|------|
| `control-plane` | 8081 | node registry, per-user REALITY ids/secrets, config assembly, guarded activation |
| `subscription` | 8082 | per-user subscription URL, base64/sing-box/clash rendering, token rotation |
| `delivery` | 8083 | multi-channel delivery (HTTPS/DoH/Telegram/GitHub raw/DNS TXT) |
| `billing` | 8084 | subscriptions, quotas, crypto billing ([ADR-004](docs/decisions/ADR-004-crypto-billing.md)) |
| `telemetry` | 8085 | ingest of anonymous FieldSignal + HealthEvent |
| `orchestrator` | 8086 | node provision/rotation (Terraform+Ansible), block detection, auto-replacement |

`packages/contracts/` is **frozen**: Go types + JSON Schema + OpenAPI — the
single source for 6 teams. Change only additively and in sync
(see `docs/contracts.md`).

## Build and test

```bash
make build    # build all modules
make test     # tests across all modules (-race)
make vet      # go vet
make fmt      # gofmt -w
make up       # docker compose: postgres + services (dev)
make down     # stop the stack
```

Go floor **1.20** (decision ceiling — 1.23). Docker images — `golang:1.22`.
Optional e2e (docker; some need an operator `REALITY_DEST`):
`make e2e-first-user`, `e2e-real-node`, `e2e-transport-probe`, `e2e-reconcile`.
Infra guards without cloud: `make infra-guards`.

## Repository layout

```
packages/contracts/   frozen types/schemas/OpenAPI (the single contract)
services/<name>/       6 services (control-plane, subscription, delivery, billing, telemetry, orchestrator)
infra/                 fleet Terraform + Ansible; scripts/ (node lifecycle, preflight, gate0)
test/e2e/              docker e2e + pure-shell guards
test/infra/            pure-shell guards for live lifecycle (no cloud)
docs/                  contracts, ADRs, operator runbooks
web/admin/             operator panel (placeholder)
```

## Status

- **Billing reliability** — Postgres integration, money races and recovery
  observability closed (tests under `-race`). Baseline frozen.
- **VPS apply** — code ready and in main (preflight, isolated workspace,
  cost-safe teardown, live reconcile wrapper, GATE-0 preflight — `make gate0`,
  [docs/GATE-0-preflight.md](docs/GATE-0-preflight.md)). No live apply on a VPS
  yet; orchestrator fleet-loop OFF (`DRY_RUN=true`, `PROBE_ENABLED=false`)
  until #3/#7/#8 are closed. See [docs/FIRST-WORKING-USER.md](docs/FIRST-WORKING-USER.md).

## Security

Secrets — env/secret manager only, never in code or deploy scripts. Dev creds
in `docker-compose.dev.yml` are local-only. Commit format:
`<type>: <description>` (feat/fix/refactor/docs/test/chore/perf/ci).
Rules for agents and humans — [`CLAUDE.md`](CLAUDE.md).
