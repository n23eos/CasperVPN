# Control Plane — статус части

**Стадия: функционально готов для поставленного объёма. НЕ production-hardened.**

Дата: 2026-07-07. Ветка/воркспейс: `casper-control`, работа только в
`services/control-plane/`. `make build` / `make vet` / `make test` — зелёные.
Интеграция на реальном Postgres 16 — 3/3 PASS, включая антиблок-путь
`block → async rebuild → нода выпала из набора`.

## Что сделано (закрыто по ТЗ)

- Схема + миграции (advisory-lock от race на старте).
- Чистая архитектура: domain / usecase / adapters (postgres, httpapi, rebuild, memory).
- Эндпоинты замороженного OpenAPI + аддитивные (subscription-set для Агента C,
  signals/aggregate, node rotate).
- Очередь пересбора конфигов: blocked → инвалидация кеша + пересбор наборов затронутых юзеров.
- AuthN/Z: service-token → роль, RBAC, constant-time. sub_token хешируется (SHA-256).
- Сиды + compose (dev-токены/SEED). Unit + contract + integration тесты. docs/control-plane.md.

## Что НЕ сделано (осознанные пробелы)

**Критично для прода:**
1. **Очередь пересбора.** ✅ Добавлена durable pg-очередь (`rebuild_jobs`,
   `FOR UPDATE SKIP LOCKED`, лиз/ретрай/дроп poison-job), включается
   `REBUILD_DURABLE=true` — переживает рестарт и делится между инстансами.
   Дефолт пока in-memory (single-instance, теряется при рестарте; `enqueue` при
   полном канале спавнит goroutine — под бурстом может расти). Логика диспетчера
   покрыта офлайн-тестами на fake-store; pg-адаптер — integration-тестом
   (`-tags integration`). Осталось: сделать durable дефолтом после обкатки.
2. **Секреты юзера дублируются at-rest** в `subscription_sets.bundle` (jsonb).
   Server-side, но копия `reality_short_id`/`uuid`/`private_key` вне `users`.
   → на ревью безопасности: строить bundle на чтении вместо персиста, либо шифровать.
3. **`private_key` — рандомный base64-плейсхолдер**, не настоящая x25519-пара,
   согласованная с серверным pubkey ноды. Реальная деривация ключей REALITY/WG —
   TODO, координация с orchestrator/провижнингом ноды.
4. **Нет rate limiting, метрик, readiness-проверки БД.** `/healthz` — только liveness.
   Логи в stdout, не отгружаются. Нет Prometheus/трейсинга.

**Функционально неполно:**
5. Набор нод — **все активные всем**. Нет таргетинга по региону/плану, нет учёта
   `Subscription.Status` (expired/suspended юзер получает полный набор). Entry/exit
   пары (`entry_node_id`) в выдаче отдельно не разворачиваются.
6. Node PATCH = **полная замена** представления, не истинный partial-patch. Частичный
   body затрёт непереданные поля.
7. Subscription: только create/get (по замороженному спеку). Нет update/cancel/
   rotate-token — отзыв утёкшей ссылки-подписки не выставлен наружу.
8. `quota_bytes`/`used_bytes` есть, но **приём счётчиков трафика не подключён**;
   fair-use throttle тут не форсится.
9. Затронутые юзеры при блоке = только те, у кого нода **уже** в наборе. Новая активная
   нода доедет до юзера лишь на следующем rebuild/fetch (ленивая доставка).
10. Токены статичные из env, без ротации/mTLS. `quota` хранится как BIGINT (int64),
    контракт uint64 — реальных объёмов хватает, но не полный диапазон.
11. Down-миграции: `0001_init.down.sql` есть, но раннер применяет только `up`.

## Что отследить потом (verify под нагрузкой)

- Лаг пересбора и поведение durable очереди при бурсте; после обкатки сделать
  `REBUILD_DURABLE=true` дефолтом.
- Рост `subscription_sets`; `dirty`-записи не пересобираются, если юзер не фетчит и нет
  события по ноде → возможна вечная stale-запись.
- Секреты at-rest в `subscription_sets` (см. п.2).
- Согласованность ключей юзера с реальным серверным материалом ноды (см. п.3).

## Готовность к интеграции

Готов как источник данных по API для Агентов C (subscription-set), E (users/subs),
F. Замороженные `packages/contracts` и чужие папки не трогал. Дальше:
observability, реальные ключи, таргетинг набора и включение durable queue как
дефолта после обкатки — по приоритетам следующей волны.
