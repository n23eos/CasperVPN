# ТЗ — Полная per-user изоляция на всех транспортах

**Приоритет:** HIGH (аудит B1). **Зона:** `packages/contracts` (аддитивно),
`services/control-plane`, `services/subscription`,
`infra/ansible/roles/transports/templates/inbound_*.json.j2`, node-sync путь.
**Зависимость:** координируется с оркестратором (пуш пула на ноду).

## Проблема

Per-user изоляция реально работает **только для VLESS-REALITY** (персональные
`uuid`+`short_id` штампуются в подписку и в allow-list ноды). Для остальных трёх
транспортов подписка отдаёт **узловые** секреты, одинаковые для всех юзеров ноды:

- `hysteria2` → узловой `Password`
- `shadowsocks-2022` → узловой `PSK`
- `amnezia-wg` → узловой `PublicKey` (нет per-user peer)

Источник: `packages/contracts/subscription_output.go` (рендер берёт `p.Password`/
`p.PSK`/`p.PublicKey` — узловые поля транспорта). `RotateSecrets`
(`control-plane/internal/usecase/users.go`) ротирует только short_id/uuid.

**Последствие:** изъятый девайс или honeypot-подписка выдаёт рабочие креды ВСЕХ
юзеров ноды на 3 из 4 транспортов. Отозвать/сменить эти общие секреты для одного
юзера нельзя. Это ломает заявленную гарантию «сжёг одного — не спалил остальных».

## Требуемый результат

Каждый юзер получает **персональные** креды на каждом включённом транспорте, и
нода принимает их через per-user allow-list / peer-таблицу. Бан/ротация одного
юзера не трогает остальных.

### 1. Контракт (аддитивно, по протоколу заморозки)

Per-user материал должен жить в `User` (или в per-user проекции транспорта), не в
узловом `Transport`. Варианты (выбрать на ревью):
- `User.Hysteria2Password string` (per-user)
- `User.ShadowsocksPSK string` (per-user; SS-2022 EIH-совместимо)
- `User.WGPublicKey`/`WGPrivateKey` (per-user peer keypair; `User.PrivateKey`
  сейчас генерится, но мёртв — здесь он обретает смысл)

Синхронно Go + JSON Schema + OpenAPI, `omitempty`, обратно совместимо.

### 2. Генерация (control-plane)

`secret.go` + `usecase/users.go`: при создании юзера генерировать per-user
пароль hy2, PSK ss-2022, WG-пару. `RotateSecrets` — ротировать все, писать в
`UserSecretRotation`.

### 3. Рендер (contracts/subscription_output.go)

hy2 → `User.Hysteria2Password`; ss → `User.ShadowsocksPSK`; wg →
`User.WGPublicKey`/`PrivateKey`. Покрыть тестом: две подписки разных юзеров дают
РАЗНЫЕ креды на каждом транспорте.

### 4. Node-side multi-user (inbound templates)

- `inbound_hysteria2.json.j2` — `users: [{name, password}]` пул (sing-box hy2
  поддерживает несколько users).
- `inbound_shadowsocks2022.json.j2` — multi-user (EIH: сервер держит несколько
  PSK).
- `inbound_amneziawg.json.j2` — `peers: [{public_key, allowed_ips}]` пул.
- REALITY уже мультиюзер (`reality_users` цикл) — привести к единому паттерну.

### 5. Синк пула CP → нода (⚠️ ключевое, завязано на оркестратор)

Нода получает **полный per-user пул** (не только короткий список из
`transports[].*`). Сейчас `reality_users`/`short_ids` пул на ноду попадает только
если control-plane пушит его через `TRANSPORTS_JSON` — механизм не сшит (см.
аудит anti-censorship + `TZ-orchestrator.md`). Этот пункт — часть замыкания петли:
CP держит пулы per-user кред, оркестратор синкает их на ноду при
provision/ротации.

## Критерии приёмки

- Две подписки разных юзеров на одной ноде → разные креды на ВСЕХ включённых
  транспортах (тест в contracts + integration CP↔subscription).
- Бан/ротация юзера отзывает его креды на ноде, не трогая остальных (integration).
- `make build/vet/test -race` зелёные; контракт-правки синхронны Go+Schema+OpenAPI.

## Промежуточная мера (уже сделана)

До выката — `architecture.md` и `CLAUDE.md` честно фиксируют, что изоляция сейчас
= только REALITY. Не заявлять полную изоляцию клиентам/в маркетинге до приёмки.
