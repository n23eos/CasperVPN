# Telemetry — handoff (петля обратной связи)

Ветка: `agent/telemetry`. Область: только `services/telemetry/` + `docs/telemetry.md`.
Полная документация — [`../../docs/telemetry.md`](../../docs/telemetry.md).

## Статус: стадия завершена функционально; Postgres runtime подключён

Петля «где что заблокировали» работает end-to-end: приём
анонимных `FieldSignal` → агрегация по `(region, transport)` → детект «транспорт
умер» → рекомендации control-plane/orchestrator → метрики. Всё покрыто тестами.
При заданном `DATABASE_URL` сервис открывает Postgres через `pgx`/`database/sql`,
делает readiness `PingContext` и использует `PostgresStore`; без `DATABASE_URL`
остаётся явный dev-fallback на память.

## Сделано
- **Ingest API** (`internal/ingest`, `internal/api`): `POST /v1/signals` (публично),
  `POST /v1/health` (bearer). Строгий декод отклоняет лишние поля (PII-smuggle).
- **Анонимизация** (`anonymize.go`): огрубление региона, клэмп диапазонов, обрезка
  времени до минуты, IP не хранится (только эфемерный salted-HMAC для rate-limit).
- **Анти-poisoning**: source-diversity вердикт (`aggregate/detector.go`), dedup по
  `signal_id`, token-bucket rate-limit, клэмп аномалий, спайк-детект.
- **Агрегация** (`aggregate/aggregator.go`): доли blocked/degraded/ok, тренд, спайк,
  rtt/loss, `reset_rate`/`probe_rate`.
- **Рекомендации** (`aggregate/recommend.go`): `mark_node_blocked`,
  `prioritize_transport` с уровнем `confidence` — вход оркестратора Волны 2.
- **Хранилище** (`internal/store`): порт `Store`; `MemoryStore` (dev fallback) +
  `PostgresStore` (`database/sql`, параметризованные запросы, батч в транзакции) +
  `schema.sql`. `main` выбирает Postgres по `DATABASE_URL`; миграции применяются
  вне старта приложения. Ретеншн-цикл в `main`.
- **Метрики** `GET /metrics` (OpenMetrics, stdlib).
- **Тесты**: aggregate 91%, api 93%, ingest 79%, store 75%. `go test -race` чист,
  `go vet` чист. Симуляция «регион заблокировал reality», poisoning-устойчивость,
  петля ingest→recommendation, Postgres-путь через встроенный fake-драйвер.

## НЕ сделано / осталось
1. **Схема/миграции применяются out-of-band.** Это намеренно: старт сервиса только
   делает `Ping`, чтобы не ловить гонку migrate-on-boot между инстансами.
2. **Покрытие store 75% < порога 80%.** Непокрыты error-ветки и `Close()`. Не
   критично; поднять при желании.
3. **TimescaleDB hypertable/retention policy** — только в `schema.sql` как коммент,
   не автоприменяется.
4. **`golangci-lint` не прогнан** — не установлен в окружении (`make lint` — no-op).
   Прогнать в CI.
5. **Локальные telemetry DTO рекомендаций.** Wire-форма уже канонизирована в
   `packages/contracts`, orchestrator потребляет `contracts.Recommendations`, но
   `services/telemetry/internal/aggregate` пока держит локальные mirror-типы.
   Мигрировать на contracts при следующей содержательной правке telemetry.

## Что отслеживать потом (риски / пределы)
- **Предел анти-poisoning:** source-diversity ломает одиночного злоумышленника, но
  НЕ ботнет из ≥`MinSources` реальных ASN. Митигация след. уровня: сделать
  авторитетный `HealthEvent` обязательным подтверждением перед авто-ротацией +
  репутация ASN + аномалия-детект по историческому базовому уровню.
- **Слабейший `SourceKey`** — `geo:region+platform`, когда нет ASN/ISP. Легче
  подделать. Следить за долей сигналов без ASN.
- **Пороги вердикта** (`MinSources=5`, `DeadShare=0.70` и т.д.) — эвристики из головы,
  не откалиброваны на реальном трафике. Настроить по живым данным через env.
- **Дедуп/rate-limit — in-memory**, на инстанс. При горизонтальном масштабировании
  нужен общий Redis (иначе реплей проходит через разные инстансы).
- **Интеграция с оркестратором (Волна 2)** — форма рекомендаций вынесена в
  `packages/contracts`, а orchestrator потребляет её через `GET /v1/recommendations`.
  Telemetry ещё формирует wire-compatible локальные aggregate-типы; это не ломает
  wire, но лучше убрать дублирование.

## Как продолжить
- Применить `internal/store/schema.sql` отдельным migration job перед включением
  `DATABASE_URL` в окружении.
- Общий стейт (Redis) для dedup/rate-limit при масштабировании.
- Калибровка порогов по реальному трафику.
- Миграция локальных telemetry recommendation DTO на `packages/contracts`.
