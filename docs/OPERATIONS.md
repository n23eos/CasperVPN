# Эксплуатация CasperVPN

Эти инструменты работают локально и не выполняют cloud provisioning, платежи,
рассылку уведомлений или автоматическое изменение production. Панель и monitor
только читают данные. Backup вызывает существующие команды `launch.py backup`
и `restore-check`.

## Приватная панель

Запуск из checkout:

```sh
python3 scripts/operator.py --env .launch/production.env
```

Панель доступна только на `http://127.0.0.1:8090`. Сервер отказывается
привязываться к внешнему IP, запрещает redirects upstream, проверяет `Host` и
`Origin`, принимает только read-only запросы и не передает токены браузеру.
Ответы control-plane и billing всегда собираются в новую безопасную структуру.
IP нод, Telegram ID, email, транспортные credentials, subscription tokens,
provider IDs и исходные ошибки сервисов в нее не входят.

Для удаленного управляющего host используйте SSH port forwarding, не меняя
loopback bind:

```sh
ssh -L 8090:127.0.0.1:8090 operator@example-host
```

Локальная проверка интерфейса с явно помеченными fake данными:

```sh
python3 scripts/operator.py --demo-fixtures --port 8090
```

Никакие production действия в demo режиме не выполняются. Этот режим нельзя
считать проверкой реальных API.

Поддерживаемые настройки панели:

```text
OPERATOR_BIND=127.0.0.1
OPERATOR_PORT=8090
OPERATOR_CONTROL_PLANE_URL=http://127.0.0.1:8081
OPERATOR_SUBSCRIPTION_URL=http://127.0.0.1:8082
OPERATOR_DELIVERY_URL=http://127.0.0.1:8083
OPERATOR_BILLING_URL=http://127.0.0.1:8084
OPERATOR_TELEMETRY_URL=http://127.0.0.1:8085
OPERATOR_HTTP_TIMEOUT_SECONDS=2
OPERATOR_OVERVIEW_DEADLINE_SECONDS=5
OPERATOR_MAX_RESPONSE_BYTES=1048576
```

`CP_ADMIN_TOKEN` и `BILLING_INTERNAL_TOKEN` читаются из приватного launch env и
остаются только в процессе сервера. `OPERATOR_ORCHESTRATOR_URL` является
необязательным: production compose не запускает orchestrator HTTP process. Если
он действительно запущен отдельно, можно явно добавить, например,
`OPERATOR_ORCHESTRATOR_URL=http://127.0.0.1:8086`.

## Локальный monitor

Один запуск проверяет readiness пяти production сервисов, сравнивает результат
с предыдущим состоянием и пишет в локальный JSONL journal только события
`initial`, `down` и `recovered`. Одинаковое состояние повторно не записывается.
Journal содержит имя сервиса, HTTP status и время. Ответы сервисов, URL с
credentials и секреты туда не попадают. Исходящих уведомлений нет.

```sh
python3 scripts/maintenance.py --env .launch/production.env monitor
```

По умолчанию файлы находятся в `.launch/monitor/state.json` и
`.launch/monitor/alerts.jsonl` с правами 0600. Пути и timeout можно настроить:

```text
MONITOR_STATE_FILE=/var/lib/caspervpn/monitor/state.json
MONITOR_ALERT_JOURNAL=/var/lib/caspervpn/monitor/alerts.jsonl
MONITOR_HTTP_TIMEOUT_SECONDS=2
```

Monitor возвращает ненулевой код, если хотя бы один ожидаемый сервис unhealthy.
Это делает ошибку видимой systemd, но само по себе не отправляет сообщение и не
перезапускает сервисы.

## Зашифрованный backup bundle

Bundle содержит consistent PostgreSQL dump, соответствующий
`.launch/production.env` с `SUBSCRIPTION_TOKEN_KEY` и явно перечисленные
manifests или state файлы. Это сохраняет возможность восстановить постоянные
subscription links. Bundle шифруется OpenSSL CMS с AES-256-GCM на сертификат
получателя. Незашифрованные промежуточные файлы создаются с правами 0600 в
`.launch/maintenance` и удаляются при выходе.

Создайте отдельную пару для backup. Закрытый ключ нужен автоматической проверке
восстановления и должен быть mode 0600. Сохраните его дополнительную защищенную
копию отдельно от backup destination:

```sh
sudo install -d -m 0700 /etc/caspervpn
sudo openssl req -x509 -newkey rsa:4096 -sha256 -days 3650 -nodes \
  -subj '/CN=CasperVPN backup recipient' \
  -keyout /etc/caspervpn/backup-recipient-key.pem \
  -out /etc/caspervpn/backup-recipient.pem
sudo chown caspervpn:caspervpn \
  /etc/caspervpn /etc/caspervpn/backup-recipient-key.pem \
  /etc/caspervpn/backup-recipient.pem
sudo chmod 0700 /etc/caspervpn
sudo chmod 0600 \
  /etc/caspervpn/backup-recipient-key.pem \
  /etc/caspervpn/backup-recipient.pem
```

Выберите существующий внешний mount или синхронизируемый off-host каталог вне
репозитория. Утилита не создает destination, не принимает `/`, home, каталог
репозитория или symlink. Пример `/etc/caspervpn/operations.env`:

```text
BACKUP_DESTINATION=/mnt/caspervpn-backups
BACKUP_RECIPIENT_CERT=/etc/caspervpn/backup-recipient.pem
BACKUP_RECIPIENT_KEY=/etc/caspervpn/backup-recipient-key.pem
BACKUP_RETENTION_COUNT=14
BACKUP_MAX_BUNDLE_BYTES=10737418240
BACKUP_INCLUDE_PATHS=/var/lib/caspervpn/runs:/var/lib/caspervpn/terraform
```

Файл настроек должен иметь права 0600. Запуск:

```sh
set -a
. /etc/caspervpn/operations.env
set +a
python3 scripts/maintenance.py --env .launch/production.env backup
```

В production env запрещены shell quoting и expansion, поэтому operational пути
для systemd хранятся в отдельном `operations.env`. Не добавляйте в него сами
секреты сервисов.

Последовательность backup fail-closed:

1. `launch.py backup` создает новый raw dump во временном приватном каталоге.
2. Dump, launch env и разрешенные include paths упаковываются без symlinks.
3. Bundle шифруется CMS AES-256-GCM во временный файл destination.
4. Файл расшифровывается и dump передается `launch.py restore-check --cleanup`.
5. Только после успешной проверки файл получает окончательное имя и применяется retention.

Encryption, authentication или restore failure не публикует новый backup и не
удаляет прежние. Retention удаляет только обычные файлы с именем формата
`caspervpn-YYYYmmddTHHMMSSZ-<hex>.bundle.cms`. Другие файлы и symlinks не
затрагиваются.

Повторная ручная проверка конкретного bundle:

```sh
python3 scripts/maintenance.py --env .launch/production.env restore-check \
  /mnt/caspervpn-backups/caspervpn-20261006T031700Z-a1b2c3d4.bundle.cms \
  --destination /mnt/caspervpn-backups \
  --recipient-cert /etc/caspervpn/backup-recipient.pem \
  --recipient-key /etc/caspervpn/backup-recipient-key.pem
```

Restore-check создает только изолированную временную БД и удаляет ее через
cleanup. Рабочая база не заменяется.

## systemd

Готовые units находятся в `infra/systemd/`:

- `caspervpn-operator.service`
- `caspervpn-monitor.service` и `.timer`
- `caspervpn-backup.service` и `.timer`

Они предполагают checkout `/opt/caspervpn`, пользователя `caspervpn`, приватный
launch env в `/opt/caspervpn/.launch/production.env` и operational settings в
`/etc/caspervpn/operations.env`. Перед установкой исправьте только пути и
пользователя под конкретный host. Пользователю backup service нужен доступ к
Docker daemon; это привилегированный доступ, который следует ограничить этим
host.

Подготовьте приватные файлы для выбранного service user без открытия group или
world access:

```sh
sudo chown caspervpn:caspervpn /opt/caspervpn/.launch/production.env
sudo chmod 0600 /opt/caspervpn/.launch/production.env
sudo chown root:root /etc/caspervpn/operations.env
sudo chmod 0600 /etc/caspervpn/operations.env
```

Systemd читает `operations.env` до перехода к service user. Recipient key и
certificate читает сам процесс, поэтому они принадлежат `caspervpn` и остаются
mode 0600. `StateDirectory=caspervpn/monitor` автоматически создает приватный
`/var/lib/caspervpn/monitor` для state и alert journal. Значения
`MONITOR_STATE_FILE` и `MONITOR_ALERT_JOURNAL` в `operations.env` должны
указывать именно в этот каталог, если unit не дополнен другим `ReadWritePaths`.

Проверка файлов без запуска таймеров:

```sh
systemd-analyze verify \
  infra/systemd/caspervpn-operator.service \
  infra/systemd/caspervpn-monitor.service infra/systemd/caspervpn-monitor.timer \
  infra/systemd/caspervpn-backup.service infra/systemd/caspervpn-backup.timer
```

Репозиторий не устанавливает и не включает units автоматически. После установки
на целевой host сначала вручную запустите каждый `.service`, проверьте journal и
зашифрованный bundle, затем отдельно примите решение о включении timers.
