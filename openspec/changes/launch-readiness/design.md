## Context

Монорепозиторий Go содержит contracts/platform и control-plane, subscription, billing, delivery, telemetry, orchestrator. PostgreSQL сохраняет состояние; shell/Ansible управляют entry/exit. Аудит commit 4eba62f воспроизвёл double-credit после crash и direct-only профиль при пустом флоте. Текущий стек остаётся основой.

## Goals / Non-Goals

Цель: завершить локально проверяемую подготовку к будущему запуску и один самостоятельный путь пользователя. Решения по обычной реализации делегированы пользователем. Внешние входы перечисляются в runbook, не имитируются как имеющиеся.

За рамками: покупка ресурсов, реальная обработка денег, публикация, сторонние аккаунты, доказательство устойчивости к DPI без полевого теста. Собственные клиентские приложения и новая админка не создаются.

## Decisions

1. Billing хранит durable invoice-credit с заранее зафиксированным целевым периодом и монотонной revision. Сохранение намерения и изменение schedule атомарны. Повтор invoice получает прежний результат. CP применяет PUT billing-state только по неубывающей revision. Sweeper использует тот же механизм и перечитывает состояние под блокировкой. Это защищает от crash-window и запоздавших HTTP-ответов.
2. Аддитивные API CP: PUT `/v1/subscriptions/{id}/billing-state` с revision/status/expires_at/grace_until; POST `/v1/users/ensure-telegram`; POST `/v1/users/{id}/ensure-subscription`; POST `/v1/subscriptions/{id}/delivery-link`; POST `/v1/subscription-tokens/resolve`; GET `/v1/nodes/{id}/access-users`. Контракты расширяются синхронно в Go/JSON Schema/OpenAPI. Создание неоплаченной подписки не открывает бессрочный доступ.
3. Telegram получает отдельный постоянный случайный токен через delivery-link. CP хранит hash и AES-GCM ciphertext под внешним ключом, сохраняя прежние импорты. Повтор get не вращает ссылку. Subscription сверяет token hash с авторитетным CP перед отдачей профиля, поэтому пропущенное downstream-уведомление не возвращает отозванный доступ. Expiry проверяется по entitlement и не уничтожает постоянную ссылку для последующего продления.
4. Бот работает только с приватными Telegram updates, identity берётся из sender ID. Дедупликация update и idempotency key invoice защищают повторы. Локальные e2e используют fake Telegram/payment endpoints, но реальные HTTP и PostgreSQL.
5. User получает персональный hysteria2_password. Новый access-users snapshot включает VLESS/Hysteria2, revision и valid_until; Ansible синхронизирует оба списка. Отсутствие персонального секрета не разрешает общий пароль. Поддерживаемый запуск сохраняет минимум две семьи транспорта.
6. Lifecycle выбирает run/workspace по manifest и сверяет node identity до изменений. Новая пара проходит converge/activation до draining старой. Периодический controller доставляет доступ, node-side lease watchdog закрывает доступ при просроченном snapshot. Активация учитывает новую access revision.
7. Пустой fleet возвращает 503 во всех профилях. Перед cache hit проверяется текущая авторизация; нежелательно возвращать старые credentials после их ротации, поэтому cache привязывается к актуальному снимку либо не используется в пути выдачи до безопасного варианта.
8. Production режим требует постоянные секреты, БД и service auth. Dev/test задаётся явно. Compose открывает наружу только необходимые публичные интерфейсы; migrations, backup/restore, readiness и launch preflight оформляются штатными командами.

## Ownership

- Архитектор: `services/control-plane`, `packages/contracts` и соответствующие CP/schema contract tests.
- Billing: `services/billing`.
- Onboarding: `services/delivery`.
- Fleet: `services/orchestrator`, `infra` и `test/infra`; согласование схем через владельца contracts.
- Главный агент: subscription, telemetry, platform, root build/compose/CI, e2e, OpenSpec, документация и интеграция.

Владельцы не переписывают файлы других исполнителей. Изменение общего API сначала сообщается всем потребителям. Независимое итоговое ревью обязательно для денег и доступа.

## Risks / Trade-offs

- Revision fence и durable billing ledger сложнее одного mutex, но нужны при неизвестном результате HTTP-запроса.
- Один управляющий процесс на первом запуске; параллельные cloud-операции ограничены lock.
- Node lease выбирает безопасность при потере контроллера, возможна временная недоступность вместо неограниченного сохранения отозванного доступа.
- Неисполненные cloud/phone/payment проверки остаются отдельным live gate, даже если все локальные тесты проходят.
- Обновление sing-box требует проверки форматов; в первом релизе фиксируется проверенная версия и явная матрица клиентской совместимости.

## Migration Plan

Аддитивные SQL-миграции и совместимые endpoints. Существующие subscription token hashes сохраняются. Пользовательские Hy2 credentials backfill до переключения нод на персональный режим. Применить schemas, настроить секреты, проверить readiness, выполнить локальный acceptance, затем будущий live gate. Backup и проверка восстановления предшествуют production migration.

## Open Questions

Блокирующих локальную реализацию вопросов нет. Домены, bot token, платёжный store и cloud credentials являются внешними входами будущего запуска; отсутствие этих значений не препятствует подготовке и fake-endpoint проверкам.
