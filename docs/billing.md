# Billing — крипта-опорный биллинг (services/billing)

Приём крипто-оплаты, активация/продление подписки, автоистечение. Опорный
платёжный рельс — крипта (см. [ADR-004](./decisions/ADR-004-crypto-billing.md)):
нет карт → нет точки давления. Минимум PII: аккаунт привязан к **анонимному
идентификатору** (`anon_user_id`), никаких email/телефонов в биллинге.

## Принцип: несколько платёжных путей, без единой точки отказа

Провайдеры оплаты спрятаны за интерфейсом `payment.Gateway`. Несколько активны
одновременно; отказ/изъятие одного не останавливает продажи — реестр
(`payment.Registry`) при создании инвойса перебирает шлюзы, поддерживающие валюту,
до первого успеха (failover).

| Шлюз | Тип | Как подтверждает | Точка давления |
|------|-----|------------------|----------------|
| `btcpay` | self-hosted (BTCPay-класс) | подписанный вебхук (HMAC) | нет кастодиана — сервер оператора |
| `onchain` | прямая ончейн-проверка | поллинг адреса + подтверждения | нет вообще — только нода/эксплорер оператора |
| `mock` | testnet/дев | подписанный вебхук (HMAC) | — (только для тестов/локалки) |

Ни один домен/адрес/ключ не захардкожен — всё из env/конфига (правило анти-блока
«ноль хардкода»).

## Поток оплаты

```
клиент/бот ──POST /v1/invoices──▶ billing ──registry.CreateInvoice──▶ шлюз
                                     │  (цена из plan-каталога по валюте)
                                     ▼
                              инвойс в store (pending)

шлюз ──вебхук / поллинг──▶ billing ──Processor.Process──▶ активация ──▶ control-plane
   подтверждение оплаты        │  (идемпотентно + антифрод)      (create/renew sub)
                               ▼
                        инвойс settled
```

1. **Создание инвойса.** `POST /v1/invoices {anon_user_id, plan, currency,
   provider?}`. Цена берётся из plan-каталога по паре (план, валюта). Ответ:
   `invoice_id`, `pay_address` (адрес или ссылка на checkout), `amount`, `currency`.
2. **Подтверждение.** Вебхук `POST /v1/webhooks/{provider}` (подпись проверяет сам
   шлюз) **или** фоновый поллинг (`Poller`, для `onchain`). Оба нормализуются в
   `model.Event` и идут через один `Processor`.
3. **Активация.** При `settled` → вычисляется срок → `control-plane` создаёт/продлевает
   подписку и выпускает/сохраняет `sub_token`.

## Идемпотентность (двухслойная)

Единственная точка обработки — `payment.Processor.Process`. Два независимых замка:

- **Replay доставки** — `SeenEvent/RecordEvent` по `(provider, external_id)`.
  Повтор той же подписанной доставки → no-op.
- **Settle-once по инвойсу** — `ClaimSettlement/ReleaseSettlement` по `invoice_id`.
  Две **разные** доставки (или вебхук + поллинг), закрывающие один инвойс, дают
  **ровно один** период. `Release` откатывает захват при сбое активации, чтобы
  retry смог дозачислить (сбой не «съедает» платёж).

Это и есть гарантия **«двойной вебхук ≠ двойной срок»** (тесты:
`TestProcess_DoubleWebhookGivesSingleTerm`,
`TestEndToEnd_DoubleWebhookSingleTerm`).

## Антифрод-минимум

- **Подпись вебхука** — HMAC-SHA256 над сырым телом; неверная → `401`, ноль
  изменений состояния. Проверка внутри шлюза (`ParseWebhook`).
- **Replay вебхука** — дедуп по `(provider, external_id)` + окно свежести
  `BILLING_REPLAY_WINDOW` (события старше окна отбрасываются).
- **Двойное зачисление** — settle-once замок по инвойсу.
- **Недоплата** — `amount >= expected` (точная десятичная сверка через
  `math/big.Rat`), иначе инвойс → `invalid`, активации нет.

## Планы, сроки, грейс, автоистечение

- Каталог планов — из JSON-конфига (`BILLING_PLAN_CATALOG`, пример:
  `services/billing/config/plans.example.json`): план → срок, грейс, цены по
  валютам, лимиты. Цены/сроки не в коде.
- **Продление** всегда от `max(now, текущий expiry)` — ранняя оплата не сжигает
  оплаченные дни (`TestActivate_RenewExtendsFromCurrentExpiry`); после лапса — от
  `now` (`TestActivate_RenewAfterLapseExtendsFromNow`).
- **Грейс/истечение** — `expiry.Sweeper` (фон): `active → past_due` (после
  `expires_at`) → `expired` (после `expires_at + grace`). Идемпотентен.

## Разделение (антихрупкость)

- Биллинг — отдельный модуль `github.com/caspervpn/billing`, свой store, свой
  бинарник/порт (`8084`). С control-plane общается **только по HTTP** за
  интерфейсом `controlplane.Client` — физически отделён от control-plane и нод.
- Ноль PII: `anon_user_id` непрозрачен; в биллинге нет email/телефона/карт/KYC.

## ⚠️ Контрактный пробел — координация с control-plane

Замороженный `packages/contracts/openapi/control-plane.yaml` имеет
`POST /v1/subscriptions` (create, отдаёт `sub_token`) и `GET`, но **не имеет
эндпоинта продления/смены срока** подписки. Активация/продление/истечение требуют
**аддитивного, обратно совместимого** эндпоинта:

```
PATCH /v1/subscriptions/{id}
body: { "status": "<SubscriptionStatus>", "expires_at": "<RFC3339>" }
→ 200 Subscription
```

По правилам заморозки (см. `CLAUDE.md` → «Contracts заморожены») это аддитивное
изменение вносит команда control-plane синхронно в Go + JSON Schema + OpenAPI
через ревью. Замороженный yaml из биллинга **не редактировался**. Биллинг
абстрагирует зависимость за `controlplane.Client`; HTTP-адаптер уже вызывает этот
эндпоинт (`SetSubscriptionPeriod`), тесты идут через `controlplane.Fake`.

## Конфигурация (env)

| Переменная | Назначение | Дефолт |
|-----------|-----------|--------|
| `PORT` | порт HTTP | `8084` |
| `BILLING_PLAN_CATALOG` | путь к JSON-каталогу планов | — (без него цены не выдаются) |
| `CONTROL_PLANE_URL` | базовый URL control-plane | `http://control-plane:8081` |
| `CONTROL_PLANE_TOKEN` | bearer для service-to-service | — |
| `BTCPAY_BASE_URL` / `_API_KEY` / `_STORE_ID` / `_WEBHOOK_SECRET` / `_CURRENCIES` | BTCPay шлюз | — |
| `BILLING_MOCK_SECRET` / `BILLING_MOCK_CURRENCIES` | dev-шлюз | — |
| `BILLING_SWEEP_INTERVAL` / `BILLING_POLL_INTERVAL` / `BILLING_REPLAY_WINDOW` | периоды фона / окно replay | `1m` / `30s` / `1h` |

Секреты — только из env/secret manager, никогда в коде (`CLAUDE.md` → «Прочее»).

## Эндпоинты

| Метод | Путь | Назначение |
|-------|------|-----------|
| `GET` | `/healthz` | liveness |
| `POST` | `/v1/invoices` | создать крипто-инвойс (`anon_user_id`, `plan`, `currency`, `provider?`) |
| `POST` | `/v1/webhooks/{provider}` | приём подтверждения от шлюза (подпись обязательна) |

## Тесты

```bash
cd services/billing && go test ./...
```

- **unit**: активация/продление/лапс (`subscription`), грейс/истечение (`expiry`),
  идемпотентность + недоплата (`payment`), HMAC-подпись (`payment/mock`),
  ончейн-поллинг (`payment/onchain`), десятичная сверка (`money`), каталог (`plan`).
- **integration**: полный HTTP-путь invoice→webhook→активация и
  **двойной вебхук → один срок** (`httpapi`), повторный поллинг → один кредит
  (`payment`).

## Прод-гэпы (осознанно вне MVP)

`store.Memory` in-memory (нужен Postgres + транзакции для multi-step writes и
переживания рестарта); rate-limiting на эндпоинтах; alerting; реальные
`onchain.ChainClient`/`AddressPool` (сейчас за интерфейсом, оператор поставляет
имплементацию под свою ноду/эксплорер). Таймауты исходящих HTTP и graceful
shutdown — уже есть.
