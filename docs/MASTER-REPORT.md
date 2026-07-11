# CasperVPN — Мастер-отчёт по Волне 1 (фундамент + 6 подсистем)

Дата: 2026-07-07. Свод по отчётам шести агентов (control-plane, subscription,
delivery, billing, telemetry, infra/node). Источник истины по архитектуре —
[`architecture.md`](../architecture.md); контракты — [`contracts.md`](./contracts.md).

Это консолидированный взгляд «сверху»: что реально готово, где швы между
подсистемами не сшиты, какие пробелы блокируют следующую волну и что переносится в
руки оператора. Детальные отчёты каждой подсистемы — по ссылкам в таблице ниже.

> **Обновление 2026-07-11:** `TZ-orchestrator` реализован в
> `services/orchestrator/`; delivery получил auth/readiness и anti-rollback
> freshness check; telemetry подключает Postgres runtime по `DATABASE_URL`.
> Локально зелёные `make build`, `make vet`, `make test`.

---

## 1. Итог одной строкой

Заложен компилящийся монорепо с **замороженными контрактами**, и **6 подсистем
доведены до функциональных скелетов с тестами**. После обновления Волны 2
orchestrator уже закрывает контур антихрупкости на моках и через реальные
HTTP/infra-адаптеры, но система **ещё не собрана в продовый единый организм**:
межсервисные e2e, рантайм-Postgres, operator secrets/cloud apply и полный
prod-hardening остаются следующими блоками. Ни одна подсистема пока не
«прод-готова», но у каждой чистые швы (интерфейсы) под доводку.

---

## 2. Матрица готовности

| Подсистема | Ветка / место | Стадия | Тесты | Prod-готов | Отчёт |
|-----------|---------------|--------|-------|:----------:|-------|
| **control-plane** | `agent/control-plane` (worktree, uncommitted) | функц. готов | unit+contract+integration (Postgres 3/3) | ❌ | [NOTES](../services/control-plane/NOTES.md), [docs](./control-plane.md) |
| **subscription** | `agent/subscription` (worktree, uncommitted) | функц. завершён | golden+fuzz 1.9M+smoke | ❌ | [STATUS](../services/subscription/STATUS.md), [docs](./subscription.md) |
| **delivery** | main-дерево (uncommitted) | ядро+4 канала | unit+e2e, покрытие ≥80% | ❌ | [STATUS](../services/delivery/docs/STATUS.md), [docs](../services/delivery/docs/delivery.md) |
| **billing** | main-дерево (uncommitted) | MVP-скелет+логика | unit+integration | ❌ | [NOTES](../services/billing/NOTES.md), [docs](./billing.md) |
| **telemetry** | main | функц. завершён | 79–93%, `-race` чист | ❌ (нужны миграции/операторский Postgres/Redis для scale) | [HANDOFF](../services/telemetry/HANDOFF.md), [docs](./telemetry.md) |
| **infra/node** | main-дерево (uncommitted) | завершён (без apply) | локальные гейты + CI | ❌ (без живого apply) | [status](./infra-status.md), [docs](./infra.md) |
| **orchestrator** | main | функц. завершён (dry-run safe default) | unit+httptest+mock e2e, `make test` | ❌ (без живого apply/проб РФ) | [docs](./orchestrator.md), [plan](../services/orchestrator/.plan.md) |

Легенда «Prod-готов»: ни одна не закрыла Production Checklist из `CLAUDE.md`
(rate-limit / alerting / durable-стейт / observability). Это осознанно отложено.

---

## 3. Что реально готово (по подсистемам, кратко)

- **control-plane** — реестр нод, юзеров, подписок; выдача per-user секретов;
  очередь пересбора конфигов (blocked → инвалидация + пересбор наборов); RBAC на
  service-token, хеш sub_token; интеграция на живом Postgres 16 (антиблок-путь
  `block → rebuild → нода выпала` — PASS). Чистая domain/usecase/adapters.
- **subscription** — `GET /sub/{token}` в трёх представлениях (base64 / sing-box /
  Clash), Happ deep-link + заголовки; персонализация (uuid/short_id в каждый
  прокси, фильтр plan/region/platform, инвариант диверсити ≥2 транспорта); кеш +
  инвалидация по сигналу; split-tunnel RU (данные в `routing.ru.json`, ноль
  хардкода). Fuzz 1.9M без падений.
- **delivery** — медиа-независимое ядро: артефакт + Ed25519-подпись + AES-256-GCM
  шифр указателя; 4 канала разной природы (telegram/max, DoH+DNS TXT, git-raw,
  стего); registry + failover; e2e «через любой канал артефакт восстановлен и
  прошёл проверку подписи».
- **billing** — `Gateway` за интерфейсом + `Registry` с failover; btcpay
  (вебхук+HMAC) / onchain (поллинг) / mock; двухслойная идемпотентность (replay +
  settle-once); планы/цены/грейс из JSON; `expiry.Sweeper`; ноль PII
  (`anon_user_id`). Крипта — опорный рельс (ADR-004).
- **telemetry** — петля «где что заблокировали» end-to-end: приём анонимных
  `FieldSignal` → анонимизация → агрегация по `(region, transport)` → детект
  «транспорт умер» → рекомендации (`mark_node_blocked`, `prioritize_transport`) →
  метрики; анти-poisoning (source-diversity, dedup, rate-limit, клэмп).
- **infra/node** — Terraform (Hetzner/Vultr/OCI, провайдер-абстракция) + Ansible
  (singbox/transports/firewall/health/rotation) + скрипты `node_up/rotate/down` +
  CI-гейты. REALITY без своего серта, entry≠exit, ноль хардкода (guard-скрипт).

---

## 4. Сквозные пробелы (главное — читать здесь)

Пробелы повторяются у нескольких агентов → это системные задачи Волны 2, а не
частные хвосты.

### A. Пробелы в замороженном контракте (нужна координированная аддитивная правка)

Заморозка сработала — агенты контракт не трогали, но упёрлись в его пределы:

1. **`PATCH /v1/subscriptions/{id}`** (status, expires_at) в control-plane — **БЛОКЕР
   billing**: без него активация/продление не работают против живого CP. billing
   написан под этот endpoint, CP его не отдаёт.
2. **Отзыв подписки / ротация sub-token / cancel** — control-plane отдаёт только
   create/get. Утёкшую ссылку сейчас не отозвать наружу.
3. **Контракт рекомендаций telemetry** уже вынесен в `packages/contracts`
   (`RecommendationAction`, `NodeBlock`, `RegionPriority`, `Recommendations`) и
   потребляется orchestrator. Остался технический хвост: telemetry internals пока
   держат локальные mirror-типы в `internal/aggregate`.

→ Всё это меняется **по протоколу заморозки** (`docs/contracts.md`): аддитивно,
синхронно Go + JSON Schema + OpenAPI, через ревью, с bump версии где нужно.

### B. Postgres везде написан, но не подключён рантаймом

`billing`, `control-plane` и `telemetry` уже имеют pgx/Postgres runtime path
(telemetry выбирает `PostgresStore` при заданном `DATABASE_URL`; миграции
применяются out-of-band). Остаются: `subscription` TokenIndex и durable rebuild
queue в `control-plane`; плюс операторский Postgres/миграционные джобы. Без этого
часть данных всё ещё не переживает рестарт или не масштабируется горизонтально.

### C. Прод-хардненинг отложен всеми (Production Checklist из `CLAUDE.md`)

Универсально нет: **rate-limiting** (публичные `/sub/{token}`, `/v1/invoices`,
вебхуки, `/d/`), **метрики/алертинг** (кроме telemetry `/metrics`),
**readiness/health-детали** (везде только liveness), **durable-очереди/общий стейт**
(control-plane rebuild-queue и telemetry dedup/rate-limit — in-memory, single-instance
→ ломаются при горизонтальном масштабе; нужен Redis/pg LISTEN-NOTIFY),
**structured logs / отгрузка логов**.

### D. Межсервисная интеграция — главный несделанный слой

Сервисы — острова. Связующая ткань не построена:

- **subscription ↔ control-plane** — оба теперь существуют, но сквозной прогон не
  гонялся (subscription тестировался на in-memory адаптере). → запустить integration.
- **billing → control-plane** — упирается в пробел A.1 (PATCH).
- **delivery** — не подключены: `SubProvider` (клиент к subscription), приёмный
  цикл бота (long-poll/webhook), шедулер пересборки `Directory` + `Broadcast` при
  ротации/блоке, потребление телеметрии (авто-снятие заблокированных каналов).
- **orchestrator** — построен как keystone Волны 2: потребляет рекомендации
  telemetry, подтверждает подозрения собственными пробами, планирует через
  anti-poisoning policy, дёргает `infra/scripts/node_{up,rotate,down}.sh` и
  синхронизирует `Node` с control-plane. Осталось операторское: живые облачные
  креды/apply, vantage-пробы из РФ, калибровка порогов на реальном трафике.

### E. Реальный крипто-материал ключей

`control-plane.private_key` — рандомный плейсхолдер, не настоящая x25519/WG-пара,
согласованная с серверным pubkey ноды. Реальная деривация REALITY/WG-ключей —
координация control-plane ↔ orchestrator ↔ infra-провижнинг. Также: секреты юзера
дублируются at-rest в `subscription_sets.bundle` (control-plane п.2) — на ревью
безопасности.

### F. Требуются входы оператора (человек с аккаунтами/деньгами/доменами)

Крипта-шлюзы (BTCPay/нода), Telegram/Max боты, DNS-провайдер + домены, домены
мимикрии, облачные креды, секреты (сиды подписи, seal-ключи, internal-токены),
Postgres прод, юрлицо/юрисдикция. → всё в [OPERATOR-CHECKLIST](./OPERATOR-CHECKLIST.md).

---

## 5. Реестр рисков (что отслеживать)

| Риск | Источник | Митигация Волны 2 |
|------|----------|-------------------|
| Потеря данных при рестарте (in-memory дефолты) | B | Postgres-проход |
| Предел анти-poisoning: ботнет из реальных ASN | telemetry | `HealthEvent` как обязательное подтверждение перед авто-ротацией + репутация ASN |
| Пороги вердикта не откалиброваны | telemetry | настройка по живому трафику через env |
| Ключи юзера ≠ реальному материалу ноды | control-plane, E | реальная деривация + синхр. при ротации |
| Секреты at-rest в `subscription_sets` | control-plane | строить bundle на чтении / шифровать |
| Дубль-инвойсы при retry | billing | идемпотентный create-invoice по ключу |
| Курс крипты vs статичная цена | billing | пересмотр `plans.json`, мониторинг |
| `node_rotate` захардкожен под Hetzner | infra | параметризовать replace-target по облаку |
| Пустой `reality_users` ⇒ `sing-box check` падает | infra | CP подаёт ≥1 user до node-up |
| Пин sing-box (1.11.4 / 1.11.11 расходятся!) | infra vs subscription | **согласовать единую версию** в CI и на ноде |
| Stale `subscription_sets` (юзер не фетчит) | control-plane | форс-пересбор / TTL на dirty |

⚠️ **Немедленно к сверке:** infra пинит sing-box `1.11.4`, subscription CI —
`1.11.11`. Разные версии → риск расхождения схем (dns/amneziawg). Свести к одной.

---

## 6. Вердикт и переход к Волне 2

**Волна 1 закрыта по объёму:** фундамент + 6 функциональных скелетов + тесты + docs.
Швы (интерфейсы `Repository`/`Gateway`/`Provider`/`ChainClient`/каналы) оставлены
под замену без переделки архитектуры.

**Волна 2 (см. ТЗ в [`docs/wave-2/`](./wave-2/)):**
1. `TZ-orchestrator.md` — ✅ реализовано: telemetry-рекомендации → пробы/policy → infra-скрипты → CP.
2. `TZ-contract-changes.md` — координированные аддитивные правки замороженного контракта (A).
3. `TZ-integration.md` — сшить сервисы (D).
4. `TZ-persistence.md` — рантайм-Postgres одним сетевым проходом (B).
5. `TZ-hardening.md` — Production Checklist по всем сервисам (C).

Порядок после закрытия orchestrator: `TZ-integration` + `TZ-persistence`; затем
оставшийся `TZ-hardening` перед прод-выкаткой. Действия оператора —
[OPERATOR-CHECKLIST](./OPERATOR-CHECKLIST.md).
