# infra — статус части (handoff)

Волна: слой инфраструктуры флота (`infra/`) — Terraform + Ansible + скрипты
ротации + CI. Дата: 2026-07-07. Владелец: infra (стыкуется с
`services/orchestrator`). Источник: [`architecture.md`](./../architecture.md),
план — `~/.claude/plans/humble-wondering-feather.md`.

## Стадия: ЗАВЕРШЕНА (для заявленного объёма)

Все пункты ТЗ реализованы, все [АНТИ-БЛОК]-критерии закрыты в коде, локальные
проверки зелёные (см. ниже). **Не** значит «развёрнуто в проде» — реальный
`terraform apply` на живом облаке не запускался (нет кредов; ТЗ: без apply).
Гейты `validate/plan/syntax/molecule` исполняются в CI, не локально.

### Проверено локально
`bash -n` всех скриптов · YAML-load 24 файлов · Jinja-рендер `config.json.j2` →
валидный JSON для всех 4 транспортов · REALITY-инвариант (есть `reality`, нет
`certificate`/`acme`) · AmneziaWG junk-параметры · `no-hardcode.sh` (30 файлов) ·
`jq`-сборка `Node` по замороженной схеме · `make -n` таргеты.

### Проверяется только в CI (тулзы локально отсутствуют)
`terraform validate/plan` · `ansible-playbook --syntax-check` · `shellcheck` ·
`molecule test` (docker-нода, listener :443, REALITY-assert).

## Что НЕ сделано (осознанно вне объёма)

1. **Реальный apply / живое облако** — нет кредов; ТЗ явно «без apply». Первый
   боевой прогон — задача оператора/оркестратора.
2. **Сквозной чейнинг entry→exit egress** — `exit_endpoint` в роли `transports`
   есть, но `node_up.sh` его не проставляет. Разделение узлов/firewall гарантия
   есть; фактическую маршрутизацию entry→exit включает переменная (KISS/YAGNI).
3. **OCI-сеть** — модуль `compute/oci` требует существующие VCN/subnet (OCID —
   переменные). Создание сети не входит в compute-модуль.
4. **Vault/secret-manager** — скрипты читают секреты из env. Интеграция с
   конкретным менеджером не подключена.
5. **Только 3 облака** — DigitalOcean/AWS/GCP не поставлены (легко добавить:
   скопировать `compute/<cloud>`).
6. **amneziawg** — по умолчанию выключен; инбаунд требует amneziawg-совместимой
   сборки sing-box, имя ключа обфускации не сверено с реальной сборкой.

## TODO / нужно доделать (приоритет сверху)

1. **`node_rotate.sh` — replace-target захардкожен под Hetzner**:
   `module.entry_vm.hcloud_server.this`. Если entry не на Hetzner — адрес ресурса
   другой. Параметризовать под облако (или перейти на `-replace` по имени модуля
   через переменную `ENTRY_REPLACE_ADDR`).
2. **`reality_users` пустой ⇒ `sing-box check` падает** (нужен ≥1 user). node-up
   с пустым пулом не поднимет vless. Control-plane обязан подавать `reality_users`
   до/вместе с node-up, либо добавить graceful-заглушку.
3. **Пин версии sing-box `1.11.4`** (`roles/singbox/defaults`): при bump —
   свериться со схемой ключей amneziawg и hysteria2 в новой версии.
4. **Проброс REALITY pubkey в control-plane при ротации** — сейчас `node_rotate`
   патчит только `entry_ip`; новый publicKey кладётся в артефакт
   `/etc/caspervpn/reality.pub`, но в `Node.transports[].vless_reality.public_key`
   его пишет оркестратор (нужна синхронизация transports целиком).
5. **health-метрики vs HealthEvent** — нода шлёт `FieldSignal` на `/v1/signals`
   (по ТЗ). Для авторитетных проб оркестратора может понадобиться `HealthEvent` →
   `/v1/health`. Развести источники, если появятся собственные пробы из РФ.
6. **Wire `exit_endpoint`** в `node_up.sh` для реального split entry/exit egress.

## Что отслеживать потом

- **Первый прогон CI**: `terraform fmt` (сделан информативным — собрать nits и
  причесать), поведение molecule-образа `geerlingguy/docker-debian12-ansible`,
  что sing-box биндит `:443` в контейнере.
- **Live REALITY-хендшейк** против реального домена мимикрии — staging-смоук
  (нужен egress), не оффлайн-CI.
- **Ротация IP-пулов**: `-replace` даёт новый IP от провайдера; если провайдер
  переиспользует «сгоревшие» диапазоны — следить по телеметрии (FieldSignal).
- **Интеграция с `services/orchestrator`**: он должен дёргать `scripts/node_*.sh`
  и потреблять `Node`/`HealthEvent`. Контракт стыка — `docs/infra.md`.

## Границы (не мой скоуп)

`packages/contracts` (заморожены), чужие `services/*`, клиентский каскад
переключения (Happ, ADR-003), полноценный флот с per-node state и автозаменой
по блок-детекту (оркестратор).
