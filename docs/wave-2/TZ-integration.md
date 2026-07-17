# ТЗ — Межсервисная интеграция (сшить острова)

**Агент:** integration. **Зона:** правки в `services/{subscription,billing,delivery}/`
(в своих модулях), плюс интеграционные тесты. Не трогать `packages/contracts`
(заморожены; правки контракта — `TZ-contract-changes.md`). Ветка: `agent/integration`.

## Контекст

Сервисы Волны 1 построены как острова с чистыми интерфейсами-швами, но связующая
ткань не сшита (см. [`MASTER-REPORT.md`](../MASTER-REPORT.md) §4.D). Задача — довести
адаптеры до реального межсервисного трафика и прогнать сквозные тесты.

**Предусловие:** `TZ-contract-changes.md` §1 (PATCH subscriptions) смержен — иначе
billing-путь не закрыть до конца.

## Задачи

### 1. subscription ↔ control-plane (оба уже существуют)

- Прогнать реальный HTTP-адаптер subscription (`internal/controlplane/http.go`)
  против живого control-plane (`/v1/users|subscriptions|nodes`,
  `subscription-set`). Раньше тестировалось только на in-memory.
- Интеграционный тест: `token → CP выдаёт bundle → три рендера → валидны`.
- Инвалидация: `нода blocked в CP → CP дёргает subscription /internal/invalidate →
  bump версии → пересбор`. Проверить связку.

### 2. billing → control-plane (после contract §1)

- Подключить billing `controlplane.Client` к реальному `PATCH /v1/subscriptions/{id}`.
- Сквозной тест: `оплаченный инвойс (mock gateway) → Processor → активация →
  CP.subscription.status=active, expires_at продлён`.
- Проверить двойной вебхук → один срок (идемпотентность на живом CP).

### 3. delivery ← subscription / control-plane / telemetry

- **`SubProvider`**: реализовать клиент delivery к `services/subscription` — бот
  отдаёт настоящую subscription-ссылку/payload, не инъецированную заглушку.
- **Приёмный цикл бота**: подключить long-poll/webhook, кормящий `HandleUpdate`
  (бот становится «живым»). Токен бота — операторский (env).
- **Шедулер directory**: периодически пересобирать `Directory` из control-plane/БД
  и `Publisher.Broadcast` по каналам при ротации/блоке (сейчас `Broadcast` никто не
  зовёт).
- **Потребление телеметрии**: подписаться на blocked-каналы/зеркала → `Registry.Remove`
  (авто-снятие мёртвого канала). Свести с формой рекомендаций telemetry.

## Критерии приёмки

- Интеграционные тесты §1–§3 зелёные (на docker-compose или testcontainers, не
  только моки). `make build`/`vet`/`test` зелёные.
- Каждый сквозной путь имеет тест с наблюдаемым конечным состоянием (не только «200 OK»).
- Ни один замороженный контракт не изменён в этом ТЗ.
- Обновить `docs/*` затронутых сервисов: как включается интеграция, какие env.

## [АНТИ-БЛОК]

- delivery: один и тот же подписанный артефакт через несколько каналов; клиент
  ОБЯЗАН проверять подпись. Anti-rollback freshness по `Artifact.IssuedAt` уже
  добавлен; монотонность `Directory.Revision` требует persistent state.
- subscription: инвариант диверсити (≥2 транспорта) не должен схлопываться фильтрами.
- Ноль хардкода доменов/эндпоинтов — всё из конфига/env/CP.

## Вне объёма

Прод-адаптеры публикации (DNS API, git-raw writer, стего-endpoint) — операторские
и/или `TZ-hardening`. Оркестрация ротации — `TZ-orchestrator`.
