# CasperVPN

Централизованный VPN-сервис с подпиской, устойчивый к DPI-блокировкам класса
ТСПУ/РКН. Живучесть — за счёт **разнообразия транспортов и петли обратной связи**,
а не «шифрования посильнее»: выживает трафик, мимикрирующий под разрешённый HTTPS/
HTTP-3, и система, которая умеет переключаться, а не один протокол.

Ядро на всех нодах — **sing-box** ([ADR-002](docs/decisions/ADR-002-singbox-core.md)).
Клиенты — **внешние** (Happ / sing-box-совместимые) через per-user subscription URL;
свои приложения не пишем ([ADR-003](docs/decisions/ADR-003-external-clients-happ.md)).

Источник истины по архитектуре — [`architecture.md`](architecture.md); контракты —
[`docs/contracts.md`](docs/contracts.md); решения — [`docs/decisions/`](docs/decisions/).

**Сайт** — [n23eos.github.io/CasperVPN/ru.html](https://n23eos.github.io/CasperVPN/ru.html)
([in English](https://n23eos.github.io/CasperVPN/)).

English version — [README.md](README.md).

## Лицензия

**Двойное лицензирование:**

- **AGPLv3** ([LICENSE](LICENSE)) — бесплатно, включая коммерческое
  использование, но при запуске модифицированной версии как сетевого сервиса
  вы обязаны открыть исходники на тех же условиях.
- **Коммерческая лицензия** — для закрытого кода / SaaS без обязательств AGPL.
  См. [COMMERCIAL.md](COMMERCIAL.md) или пишите на <mns.nicholas@gmail.com>.

## Требование [АНТИ-БЛОК] (к каждому сервису)

1. **Много транспортов одновременно, не монокультура** — нода несёт несколько
   `Transport`; клиент переключается (VLESS-REALITY / Hysteria2 / AmneziaWG /
   Shadowsocks-2022).
2. **Быстрая ротация и entry ≠ exit** — атака на entry не палит exit.
3. **Per-user изоляция** — персональные `reality_short_id`/`uuid`/ключ.
   ⚠️ Сейчас энфорсится только для **VLESS-REALITY**; hysteria2/shadowsocks-2022/
   amnezia-wg пока несут узловые (общие) креды (см. `architecture.md`,
   `docs/wave-2/`).
4. **Петля обратной связи** — анонимные `FieldSignal` + `HealthEvent` превращают
   «где заблокировали» в новые конфиги/домены.
5. **Ноль хардкода** — ни домена мимикрии, ни IP в коде; только из конфига/БД.

## Сервисы

Монорепо на Go workspace (`go.work`): `packages/contracts` + 6 сервисов. Каждый —
отдельный модуль `github.com/caspervpn/<name>`.

| Сервис | Порт (dev) | Роль |
|--------|-----------|------|
| `control-plane` | 8081 | реестр нод, per-user REALITY id/секреты, сборка конфигов, guarded activation |
| `subscription` | 8082 | per-user subscription URL, рендер base64/sing-box/clash, ротация токена |
| `delivery` | 8083 | мультиканальная доставка (HTTPS/DoH/Telegram/GitHub raw/DNS TXT) |
| `billing` | 8084 | подписки, квоты, крипта-биллинг ([ADR-004](docs/decisions/ADR-004-crypto-billing.md)) |
| `telemetry` | 8085 | приём анонимных FieldSignal + HealthEvent |
| `orchestrator` | 8086 | provision/ротация нод (Terraform+Ansible), детект блокировок, автозамена |

`packages/contracts/` — **заморожен**: Go-типы + JSON Schema + OpenAPI, единый
источник для 6 команд. Менять только аддитивно и синхронно (см. `docs/contracts.md`).

## Сборка и тесты

```bash
make build    # собрать все модули
make test     # тесты по всем модулям (-race)
make vet      # go vet
make fmt      # gofmt -w
make up       # docker compose: postgres + сервисы (dev)
make down     # остановить стек
```

Go floor **1.20** (потолок из решения — 1.23). Docker-образы — `golang:1.22`.
Опциональные e2e (docker; часть требует операторский `REALITY_DEST`):
`make e2e-first-user`, `e2e-real-node`, `e2e-transport-probe`, `e2e-reconcile`.
Инфра-гварды без облака: `make infra-guards`.

## Репозиторий

```
packages/contracts/   заморожённые типы/схемы/OpenAPI (единый контракт)
services/<name>/       6 сервисов (control-plane, subscription, delivery, billing, telemetry, orchestrator)
infra/                 Terraform + Ansible флота; scripts/ (node lifecycle, preflight, gate0)
test/e2e/              docker e2e + pure-shell guards
test/infra/            pure-shell guards для live-lifecycle (без облака)
docs/                  контракты, ADR, операторские runbook'и
web/admin/             панель оператора (плейсхолдер)
```

## Статус

- **Billing reliability** — Postgres-интеграция, денежные гонки и наблюдаемость
  восстановления закрыты (тесты под `-race`). Baseline заморожен.
- **VPS-apply** — код готов и в main (preflight, изолированный workspace, cost-safe
  teardown, live reconcile wrapper, GATE-0 preflight — `make gate0`,
  [docs/GATE-0-preflight.md](docs/GATE-0-preflight.md)). Живого apply на VPS ещё не
  было; orchestrator fleet-loop OFF (`DRY_RUN=true`, `PROBE_ENABLED=false`) до
  закрытия #3/#7/#8. См. [docs/FIRST-WORKING-USER.md](docs/FIRST-WORKING-USER.md).

## Безопасность

Секреты — только env/secret manager, никогда в коде и деплой-скриптах. Dev-креды в
`docker-compose.dev.yml` — только для локалки. Формат коммитов:
`<type>: <описание>` (feat/fix/refactor/docs/test/chore/perf/ci).
Правила для агентов и людей — [`CLAUDE.md`](CLAUDE.md).
