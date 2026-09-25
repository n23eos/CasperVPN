## Purpose

Автоматика управляет только выбранной парой entry/exit, сохраняет доступность при замене и доставляет изменения персонального доступа до VPN-нод.

## ADDED Requirements

### Requirement: Explicit resource identity
Lifecycle SHALL связывать CP node, run manifest и Terraform workspace до любых изменений ресурсов.

#### Scenario: Wrong workspace
- **WHEN** идентификатор ноды не соответствует manifest или workspace
- **THEN** операция завершается до Terraform apply/destroy

### Requirement: Ready replacement before draining
Автоматика SHALL считать ротацию завершённой только после converge и guarded activation, а замену SHALL активировать до draining старой пары.

#### Scenario: Failed replacement
- **WHEN** новая пара не проходит проверку или активацию
- **THEN** старая рабочая пара не снимается с обслуживания

### Requirement: Personal multi transport admission
Ноды SHALL использовать персональные credentials для поддерживаемого запуска VLESS и Hysteria2, сохраняя не менее двух семейств транспорта.

#### Scenario: Revoke one user
- **WHEN** пользователь заблокирован или его право доступа истекло
- **THEN** периодический converge удаляет его доступ для обоих транспортов, сохраняя доступ второго пользователя

### Requirement: Bounded convergence
Система SHALL периодически обновлять списки доступа без обязательного ручного запуска; период и ошибка converge SHALL быть наблюдаемыми.

#### Scenario: Expiry without API mutation
- **WHEN** время допуска пользователя заканчивается без отдельного запроса к API
- **THEN** следующий успешный цикл удаляет пользователя с нод
