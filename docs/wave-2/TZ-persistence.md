# ТЗ — Рантайм-Postgres (один сетевой проход)

**Агент:** persistence. **Зона:** `services/{control-plane,subscription,billing,telemetry}/`
(store-адаптеры + main-wiring в своих модулях) + `go.work.sum`/`go.sum`. Не трогать
`packages/contracts`. Ветка: `agent/persistence`. **Требует сети** (`go get`).

## Контекст

> **Статус 2026-07-11:** billing/control-plane уже имеют pgx/Postgres path;
> telemetry теперь линкует `pgx/v5/stdlib` и выбирает `PostgresStore` по
> `DATABASE_URL`; subscription `TokenIndex` теперь имеет durable Postgres backend.
> Control-plane rebuild queue тоже закрыта durable Postgres backend
> (`REBUILD_DURABLE=true`). Остаются migration job, операторский Postgres и решение
> сделать durable path дефолтом после обкатки.

Исходное состояние Волны 1: Postgres-код был написан и протестирован
(fake-драйвер / живой PG в control-plane), но часть сервисов дефолтила в
in-memory, потому что Волна 1 шла оффлайн. После проходов 2026-07-11 telemetry
уже имеет runtime Postgres path; subscription `TokenIndex` тоже закрыт отдельным
Postgres backend; durable rebuild-queue закрыта таблицей `rebuild_jobs`. См.
[`MASTER-REPORT.md`](../MASTER-REPORT.md) §4.B и хэндоффы telemetry/billing/subscription.

Это **инфраструктурный проход**, не переписывание логики: интерфейсы
(`Repository`/`Store`/`TokenIndex`) уже есть, меняется только дефолтная реализация +
линковка драйвера.

## Задачи (по каждому сервису одинаковый паттерн)

1. Добавить драйвер: `go get github.com/jackc/pgx/v5/stdlib` (единый на монорепо).
   Зафиксировать `go.sum` в каждом затронутом модуле.
2. Blank-import драйвера в `main`.
3. `sql.Open("pgx", DATABASE_URL)` + `db.PingContext` (readiness).
4. Применить схему/миграции:
   - telemetry: ✅ runtime wiring закрыт (`DATABASE_URL` → `PostgresStore`);
     осталось применить `store/schema.sql`/TimescaleDB policy отдельной миграцией.
   - billing: реализовать `store.Repository` на Postgres + **транзакция**
     settle→activate (сейчас `store.Memory`).
   - subscription: ✅ Postgres-бэкенд `controlplane.TokenIndex` закрыт
     (`DATABASE_URL` → durable index, пусто → in-memory; хранит только хеши).
     Схему `internal/controlplane/schema.sql` применять отдельной миграцией.
   - control-plane: ✅ durable-очередь пересбора закрыта таблицей `rebuild_jobs`
     + `FOR UPDATE SKIP LOCKED`; включается `REBUILD_DURABLE=true`, default пока
     in-memory до обкатки.
5. Заменить `NewMemory*` → `NewPostgres*` за флагом/наличием `DATABASE_URL`
   (оставить in-memory как явный dev-fallback).
6. **Миграции — не на старте приложения** (race при двух инстансах, Production
   Checklist): отдельный шаг/джоба миграции; на старте — только `Ping` + версия-check.

## Критерии приёмки

- Каждый сервис при заданном `DATABASE_URL` использует Postgres; данные переживают
  рестарт (тест: записать → рестарт процесса → прочитать).
- billing: транзакция settle→activate атомарна (тест: падение между шагами не
  оставляет полусостояния).
- `make build`/`vet`/`test` зелёные; интеграционные тесты гоняются на реальном PG
  (docker-compose `postgres` уже есть) или testcontainers.
- control-plane rebuild-queue переживает рестарт при `REBUILD_DURABLE=true`; сделать
  durable path дефолтом после обкатки.
- `go.sum` зафиксированы; сборка воспроизводима.

## Вне объёма

Redis для dedup/rate-limit при горизонтальном масштабе (→ `TZ-hardening`). Реальный
прод-Postgres-инстанс и бэкапы (оператор). Калибровка retention-порогов (по трафику).
