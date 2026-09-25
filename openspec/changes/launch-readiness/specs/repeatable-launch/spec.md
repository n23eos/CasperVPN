## Purpose

Подготовка запуска должна воспроизводиться одной документированной последовательностью и показывать реальные ошибки конфигурации и проверок до платных действий.

## ADDED Requirements

### Requirement: Production configuration fails closed
Production SHALL требовать durable storage, обязательные service tokens и постоянные ключи; mock payments SHALL быть доступны только в явном dev/test режиме.

#### Scenario: Missing production secret
- **WHEN** обязательный ключ или адрес БД не задан
- **THEN** сервис завершает запуск с понятным сообщением без вывода секрета

### Requirement: Repeatable release verification
Проект SHALL предоставлять локальный release gate со сборкой, unit/integration, OpenSpec validation и функциональным onboarding; пропущенные live проверки SHALL обозначаться отдельно.

#### Scenario: Local gate
- **WHEN** оператор запускает подготовку без cloud credentials
- **THEN** выполняются локальные проверки без создания ресурсов, а live readiness не объявляется доказанной

### Requirement: Recoverable deployment
Проект SHALL предоставлять production compose, идемпотентные миграции, процедуры backup/restore, проверку готовности и rollback.

#### Scenario: Restore rehearsal
- **WHEN** локальная БД восстановлена из подготовленного backup в отдельный экземпляр
- **THEN** учётные записи, invoice и subscription state сохранены

### Requirement: Honest launch handoff
Инструкция SHALL перечислять только необходимые внешние входы для будущего запуска и команды preflight, start, verify и stop без неявных расходов.

#### Scenario: Missing operator input
- **WHEN** домен, платёжный провайдер или облачный токен отсутствует
- **THEN** preflight перечисляет незаполненные входы и не создаёт инфраструктуру
