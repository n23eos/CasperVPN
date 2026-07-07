# ТЗ — Координированные изменения замороженного контракта

**Агент:** contracts-steward (доверенный, право менять `packages/contracts`).
**Зона:** `packages/contracts/` + затронутые OpenAPI + `docs/contracts.md`.
**Особый режим:** это **единственное** ТЗ, которому разрешено трогать замороженный
контракт — строго по протоколу заморозки. Ветка: `agent/contract-changes`.

## Контекст

Заморозка сработала: агенты Волны 1 не ломали контракт, но уперлись в его пределы
(см. [`MASTER-REPORT.md`](../MASTER-REPORT.md) §4.A). Нужны **аддитивные,
обратно совместимые** расширения, синхронно в трёх местах: Go-типы + JSON Schema +
OpenAPI. Каждое — через явный diff и ревью. Правила — `docs/contracts.md` §«Правила
изменения».

## Изменения (по приоритету)

### 1. [БЛОКЕР billing] `PATCH /v1/subscriptions/{id}` в control-plane OpenAPI

- Добавить операцию в `packages/contracts/openapi/control-plane.yaml`: partial
  update подписки — поля `status` (`SubscriptionStatus`), `expires_at` (date-time).
- Семантика — **истинный partial** (не замена): непереданные поля не трогаются.
- Ответы: `200` (Subscription), `404`, `422`, `401`.
- Go: тип запроса опционально (напр. `SubscriptionPatch` с указателями) — если
  добавляешь в пакет, синхронь JSON Schema. Минимум — задокументировать форму.
- **Разблокирует:** billing активацию/продление против живого control-plane.

### 2. Отзыв/ротация подписки

- `POST /v1/subscriptions/{id}/rotate-token` → новый `token`, старый инвалидируется
  (отзыв утёкшей ссылки без пересоздания аккаунта).
- `POST /v1/subscriptions/{id}/cancel` → `status=canceled`.
- Ответы: `200`/`404`. Синхронь OpenAPI (control-plane).

### 3. Контракт рекомендаций telemetry → `packages/contracts`

- Сейчас `Recommendation` / `NodeBlock` / `RegionPriority` живут локально в
  `services/telemetry`. Оркестратор Волны 2 будет их потреблять → вынести
  каноническую форму в `packages/contracts` (Go + JSON Schema) и описать в
  telemetry OpenAPI как ответ выдачи рекомендаций.
- Согласуй поля с фактической структурой из telemetry (`aggregate/recommend.go`):
  тип действия, `node_id`/`region`/`transport_type`, `confidence`, окно/время.
- Не ломать существующие FieldSignal/HealthEvent — только добавить новые типы.

## Критерии приёмки

- Изменения **только аддитивные**; ни одно существующее поле/enum-значение не
  переименовано и не удалено (иначе — bump версии контракта и миграция, чего здесь
  избегаем).
- Go-типы ⇄ JSON Schema ⇄ OpenAPI **согласованы** (одинаковые имена/типы полей).
- `make build`/`vet`/`test` зелёные; существующий тест формата выдачи
  (`subscription_output_test.go`) не сломан.
- Каждое изменение отражено в `docs/contracts.md` (таблицы полей + раздел изменений).
- OpenAPI-файлы парсятся (3.1), `$ref` на JSON Schema резолвятся.

## Порядок

Делать **до/параллельно** `TZ-orchestrator` и перед доводкой billing-интеграции —
это разблокирует обоих. После мержа — уведомить billing (endpoint), telemetry и
orchestrator (форма рекомендаций).

## Вне объёма

Реализация endpoint’ов (control-plane), потребление (billing/orchestrator) —
отдельные ТЗ. Здесь только **интерфейс/контракт**.
