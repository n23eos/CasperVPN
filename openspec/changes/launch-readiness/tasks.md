## 1. Specification

- [x] 1.1 Зафиксировать исходную структуру и требования OpenSpec; проверить наличие proposal и пяти specs.
- [ ] 1.2 Завершить архитектурные решения и проверить openspec validate --strict.

## 2. Money and access

- [ ] 2.1 Закрыть empty-fleet и cache bypass; тесты HTTP и всех форматов.
- [ ] 2.2 Сделать начисление invoice устойчивым к crash/replay/concurrency; регрессии и Postgres integration.
- [ ] 2.3 Добавить идемпотентное создание invoice и согласованный grace; регрессии повторов и expiry.
- [ ] 2.4 Обеспечить стабильную защищённую ссылку после рестарта; тест двух устройств и ротации.

## 3. Onboarding

- [ ] 3.1 Подключить приватный Telegram start/pay/get и durable обработку повторов; fake Telegram API e2e.
- [ ] 3.2 Проверить полный путь с реальными локальными HTTP и PostgreSQL без admin rotate-token.

## 4. Fleet

- [ ] 4.1 Исправить manifest/workspace identity и rotation/replacement readiness; lifecycle guards.
- [ ] 4.2 Реализовать персональный Hysteria2 и VLESS admission; schema/renderer/node regression.
- [ ] 4.3 Подключить периодический converge с expiry/revocation; проверить удаление одного пользователя при сохранении второго.

## 5. Launch preparation

- [ ] 5.1 Строгий production env, readiness, pool limits и rate limits; отрицательные config/HTTP тесты.
- [ ] 5.2 Обновить toolchain/сборку/CI, включить platform в проверки; build/vet/lint/race gate.
- [ ] 5.3 Подготовить production compose, migrations, backup/restore и preflight; локальная репетиция восстановления.
- [ ] 5.4 Подготовить один runbook запуска и отката с отдельными внешними входами; проверить команды без облачных действий.

## 6. Acceptance

- [ ] 6.1 Пройти unit, Postgres integration, shell guards, render и onboarding e2e; сохранить результаты.
- [ ] 6.2 Независимо проверить существенные изменения и исправить замечания; повторить затронутые тесты.
- [ ] 6.3 Сверить diff и OpenSpec, обновить каноническую карточку и явно отделить локальную готовность от неисполненных live проверок.
