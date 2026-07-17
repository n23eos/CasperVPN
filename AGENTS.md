# CasperVPN — правила репозитория (для агентов и людей)

Централизованный VPN-сервис с подпиской, устойчивый к DPI-блокировкам класса
ТСПУ/РКН. Источник истины по архитектуре — [`architecture.md`](./architecture.md).
Документация контрактов — [`docs/contracts.md`](./docs/contracts.md). Решения —
[`docs/decisions/`](./docs/decisions/).

Ядро на всех нодах — **sing-box** ([ADR-002](./docs/decisions/ADR-002-singbox-core.md)).
Клиенты — **внешние** (Happ / sing-box-совместимые) через subscription URL, свои
приложения не пишем ([ADR-003](./docs/decisions/ADR-003-external-clients-happ.md)).

## Сборка и тесты

Монорепо на Go workspace (`go.work`): `packages/contracts` + 6 сервисов.

```bash
make build   # собрать все модули
make test    # тесты по всем модулям
make vet     # go vet
make lint    # golangci-lint (no-op с предупреждением, если не установлен)
make fmt     # gofmt -w
make up      # docker compose: postgres + сервисы
make down    # остановить стек
```

- Go floor — **1.20** (собирается на текущем тулчейне; потолок из решения — 1.23,
  поднять `go`-директиву можно свободно). Docker-образы собираются на `golang:1.22`.
- Каждый сервис — отдельный модуль `github.com/caspervpn/<name>`, тянет
  `contracts` через `go.work` + `replace` (без внешнего реестра).
- Порты в dev: control-plane 8081, subscription 8082, delivery 8083, billing 8084,
  telemetry 8085, orchestrator 8086, postgres 5432. Порт сервиса — из env `PORT`.

## Карта владения (границы папок)

Каждая подсистема строится независимо. **Не редактируй чужую папку** без причины из
своей задачи; общий контракт меняется только по правилам заморозки (ниже).

| Папка | Владелец / подсистема | Роль |
|-------|-----------------------|------|
| `packages/contracts/` | **общее, заморожено** | типы/схемы/OpenAPI — единый источник |
| `services/control-plane/` | control-plane | реестр нод, выдача per-user REALITY id/секретов, сборка конфигов |
| `services/subscription/` | subscription | per-user subscription URL, рендер base64/sing-box/clash |
| `services/delivery/` | delivery | мультиканальная доставка (HTTPS/DoH/Telegram/GitHub raw/DNS TXT) |
| `services/billing/` | billing | подписки, квоты, крипта-опорный биллинг ([ADR-004](./docs/decisions/ADR-004-crypto-billing.md)) |
| `services/telemetry/` | telemetry | приём анонимных FieldSignal + HealthEvent, петля обратной связи |
| `services/orchestrator/` | orchestrator | provision/ротация нод (Terraform+Ansible), детект блокировок, автозамена |
| `web/admin/` | admin UI | панель оператора (плейсхолдер) |
| `infra/` | infra | Terraform/Ansible флота (плейсхолдер) |
| `test/chaos/` | reliability | chaos/устойчивость (плейсхолдер) |
| `docs/` | общее | контракты, ADR |

## 🧊 Contracts заморожены

Файлы в `packages/contracts/` (Go-типы, `schema/contracts.schema.json`,
`openapi/*.yaml`) **заморожены** после волны фундамента. По ним 6 команд пишут
параллельно.

- **Не меняй контракт под свою фичу.** Нужно новое поле — только **аддитивно и
  обратно совместимо**, синхронно в Go **и** JSON Schema **и** (если про API) OpenAPI,
  через ревью.
- **Ломающее** изменение (смена/удаление enum-значения, переименование/удаление
  поля, смена типа) — только координированной миграцией и bump `TransportVersion`.
- Go и JSON Schema обязаны совпадать. Полный свод правил — в `docs/contracts.md`.

## Требование [АНТИ-БЛОК] — к КАЖДОМУ сервису

Система живуча за счёт разнообразия и обратной связи, а не «шифрования посильнее».
Любой сервис обязан соблюдать (см. `architecture.md`):

1. **Много транспортов одновременно, не монокультура.** Никогда не сужай флот до
   одного `TransportType`. Нода несёт несколько `Transport`; клиент переключается.
2. **Быстрая ротация и entry≠exit.** Уважай `Node.rotate_after`,
   `ephemeral_entry_ip`, `role`/`entry_node_id`. Атака на entry не должна палить exit.
3. **Per-user изоляция.** Оперируй персональными `User.reality_short_id`/`uuid`/ключ.
   Блок/бан одного юзера не должен палить остальных на общей ноде.
   ⚠️ **Сейчас энфорсится только для VLESS-REALITY.** hysteria2/shadowsocks-2022/
   amnezia-wg в подписке пока несут узловые (общие) креды — см. границу в
   `architecture.md` и `docs/wave-2/TZ-per-user-isolation.md`. Не заявлять полную
   изоляцию, пока это ТЗ не выкачено.
4. **Петля обратной связи.** Пиши/потребляй `FieldSignal` (анонимно, без PII) и
   `HealthEvent`, чтобы «где что заблокировали» превращалось в новые конфиги/домены.
5. **Ноль хардкода.** Ни одного домена мимикрии и ни одного IP в коде — только
   из конфига/БД через поля контракта.

## Прочее

- Секреты — только env/secret manager, никогда в коде и деплой-скриптах.
- Dev-креды в `docker-compose.dev.yml` — только для локалки, не для прода.
- Формат коммитов: `<type>: <описание>` (feat/fix/refactor/docs/test/chore/perf/ci).

## Imported Claude Cowork project instructions
