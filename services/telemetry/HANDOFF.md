# Telemetry — handoff (петля обратной связи)

Ветка: `agent/telemetry`. Область: только `services/telemetry/` + `docs/telemetry.md`.
Полная документация — [`../../docs/telemetry.md`](../../docs/telemetry.md).

## Статус: стадия завершена функционально; НЕ прод-готово по одному пункту (Postgres runtime)

Петля «где что заблокировали» работает end-to-end на in-memory хранилище: приём
анонимных `FieldSignal` → агрегация по `(region, transport)` → детект «транспорт
умер» → рекомендации control-plane → метрики. Всё покрыто тестами. Единственное, что
осталось до прода — подключить Postgres рантаймом (код готов и протестирован
fake-драйвером, но `main` по умолчанию использует память).

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
- **Хранилище** (`internal/store`): порт `Store`; `MemoryStore` (дефолт) +
  `PostgresStore` (`database/sql`, параметризованные запросы, батч в транзакции) +
  `schema.sql`. Ретеншн-цикл в `main`.
- **Метрики** `GET /metrics` (OpenMetrics, stdlib).
- **Тесты**: aggregate 91%, api 93%, ingest 79%, store 75%. `go test -race` чист,
  `go vet` чист. Симуляция «регион заблокировал reality», poisoning-устойчивость,
  петля ingest→recommendation, Postgres-путь через встроенный fake-драйвер.

## НЕ сделано / осталось
1. **Postgres не подключён рантаймом (главное).** Дефолт — `MemoryStore`, данные не
   переживают рестарт → строгое требование «хранилище временных рядов + ретеншн»
   выполнено кодом, но не рабочим дефолтом. Причина: оффлайн-окружение, нулевые
   внешние зависимости в монорепо (нет `go.sum`, нельзя слинковать драйвер).
   **Как доделать** (нужна сеть): `go get github.com/jackc/pgx/v5/stdlib` →
   blank-import в `main` → `sql.Open` → применить `store/schema.sql` → заменить
   `store.NewMemoryStore(...)` на `store.NewPostgresStore(db)`. `DATABASE_URL` уже
   приходит из env.
2. **Покрытие store 75% < порога 80%.** Непокрыты error-ветки и `Close()`. Не
   критично; поднять при желании.
3. **TimescaleDB hypertable/retention policy** — только в `schema.sql` как коммент,
   не автоприменяется.
4. **`golangci-lint` не прогнан** — не установлен в окружении (`make lint` — no-op).
   Прогнать в CI.

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
- **Интеграция с оркестратором (Волна 2)** — контракт рекомендаций (`Recommendation`,
  `NodeBlock`, `RegionPriority`) определён локально в telemetry. Когда оркестратор
  начнёт потреблять — согласовать форму (возможно вынести в `packages/contracts`).

## Как продолжить
- Прод-Postgres — шаги в п.1 выше (нужна сеть).
- Общий стейт (Redis) для dedup/rate-limit при масштабировании.
- Калибровка порогов по реальному трафику.
- Согласование контракта рекомендаций с оркестратором.
