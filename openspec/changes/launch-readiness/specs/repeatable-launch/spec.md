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

### Requirement: Untrusted telemetry cannot authorize fleet actions
Публичные FieldSignal SHALL оставаться информационными наблюдениями; клиентские ASN и ISP SHALL NOT служить доказательством независимых источников для автоматических действий.

#### Scenario: Forged source diversity
- **WHEN** один клиент передаёт несколько отчётов с разными ASN
- **THEN** отчёты не создают команды блокировки нод или смены приоритета транспорта
- **AND** автоматическое действие требует аутентифицированной инфраструктурной проверки

### Requirement: Reverse proxy preserves independent client limits
Subscription SHALL доверять forwarded IP только от явно заданных proxy CIDR и применять отдельный rate limit для каждого полученного адреса клиента.

#### Scenario: Shared trusted proxy
- **WHEN** два клиента обращаются через один доверенный proxy
- **THEN** исчерпание лимита первого не исчерпывает лимит второго
- **AND** недоверенный peer не может изменить bucket заголовком X-Forwarded-For

### Requirement: Current core compatibility is executable
Проект SHALL фиксировать проверенную версию sing-box и проверять сгенерированный клиентский JSON реальным ядром без compatibility flags. Runbook SHALL указывать минимальную проверенную версию клиента.

#### Scenario: Supported client imports a profile
- **WHEN** профиль singbox с текущей routing policy передан ядру 1.14.2
- **THEN** sing-box check завершается успешно без legacy DNS
- **AND** локальный data-plane тест подтверждает работу и независимый отзыв VLESS и Hysteria2
