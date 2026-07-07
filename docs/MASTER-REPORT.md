# CasperVPN — Мастер-отчёт по Волне 1 (фундамент + 6 подсистем)

Дата: 2026-07-07. Свод по отчётам шести агентов (control-plane, subscription,
delivery, billing, telemetry, infra/node). Источник истины по архитектуре —
[`architecture.md`](../architecture.md); контракты — [`contracts.md`](./contracts.md).

Это консолидированный взгляд «сверху»: что реально готово, где швы между
подсистемами не сшиты, какие пробелы блокируют следующую волну и что переносится в
руки оператора. Детальные отчёты каждой подсистемы — по ссылкам в таблице ниже.

---

## 1. Итог одной строкой

Заложен компилящийся монорепо с **замороженными контрактами**, и **6 подсистем
доведены до функциональных скелетов с тестами**. Но система **ещё не собрана в
единый организм**: сервисы построены как острова, связующий слой (оркестратор +
интеграции между сервисами + рантайм-Postgres + прод-хардненинг) — не сделан.
Оркестратор (keystone Волны 2) остался заглушкой. Ни одна подсистема не
«прод-готова», но у каждой чистые швы (интерфейсы) под доводку.

---

## 2. Матрица готовности

| Подсистема | Ветка / место | Стадия | Тесты | Prod-готов | Отчёт |
|-----------|---------------|--------|-------|:----------:|-------|
| **control-plane** | `agent/control-plane` (worktree, uncommitted) | функц. готов | unit+contract+integration (Postgres 3/3) | ❌ | [NOTES](../services/control-plane/NOTES.md), [docs](./control-plane.md) |
| **subscription** | `agent/subscription` (worktree, uncommitted) | функц. завершён | golden+fuzz 1.9M+smoke | ❌ | [STATUS](../services/subscription/STATUS.md), [docs](./subscription.md) |
| **delivery** | main-дерево (uncommitted) | ядро+4 канала | unit+e2e, покрытие ≥80% | ❌ | [STATUS](../services/delivery/docs/STATUS.md), [docs](../services/delivery/docs/delivery.md) |
| **billing** | main-дерево (uncommitted) | MVP-скелет+логика | unit+integration | ❌ | [NOTES](../services/billing/NOTES.md), [docs](./billing.md) |
| **telemetry** | `feat/telemetry-feedback-loop` (**закоммичен**) | функц. завершён | 79–93%, `-race` чист | ❌ (Postgres не в рантайме) | [HANDOFF](../services/telemetry/HANDOFF.md), [docs](./telemetry.md) |
| **infra/node** | main-дерево (uncommitted) | завершён (без apply) | локальные гейты + CI | ❌ (без живого apply) | [status](./infra-status.md), [docs](./infra.md) |
| **orchestrator** | — | **ЗАГЛУШКА (не построен)** | — | ❌ | — |

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
3. **Контракт рекомендаций telemetry** (`Recommendation`, `NodeBlock`,
   `RegionPriority`) определён локально в telemetry. Когда оркестратор начнёт
   потреблять — форму согласовать и, вероятно, вынести в `packages/contracts`.

→ Всё это меняется **по протоколу заморозки** (`docs/contracts.md`): аддитивно,
синхронно Go + JSON Schema + OpenAPI, через ревью, с bump версии где нужно.

### B. Postgres везде написан, но не подключён рантаймом

`telemetry`, `billing`, `subscription` (TokenIndex), частично `control-plane`
(durable-очередь) дефолтят в in-memory. Причина у всех одна: **оффлайн-окружение,
нет `go.sum`, нельзя слинковать драйвер**. Данные не переживают рестарт. Лечится
**одним сетевым проходом**: `go get` драйвера (pgx) → blank-import → `sql.Open` →
применить `schema.sql`/миграции → заменить `NewMemory*` на `NewPostgres*`.
`DATABASE_URL` уже прокинут в env/compose.

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
- **orchestrator** — **не построен вообще.** Он keystone: потребляет рекомендации
  telemetry, дёргает `infra/scripts/node_{up,rotate,down}.sh`, синхронизирует
  `Node`/REALITY-ключи с control-plane, детектит блоки и автозаменяет ноды.

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
1. `TZ-orchestrator.md` — построить keystone (telemetry-рекомендации → infra-скрипты → CP).
2. `TZ-contract-changes.md` — координированные аддитивные правки замороженного контракта (A).
3. `TZ-integration.md` — сшить сервисы (D).
4. `TZ-persistence.md` — рантайм-Postgres одним сетевым проходом (B).
5. `TZ-hardening.md` — Production Checklist по всем сервисам (C).

Порядок: сначала `TZ-contract-changes` (разблокирует billing и оркестратор),
параллельно `TZ-orchestrator`; затем `TZ-integration` + `TZ-persistence`; в конце
`TZ-hardening` перед прод-выкаткой. Действия оператора — [OPERATOR-CHECKLIST](./OPERATOR-CHECKLIST.md).
