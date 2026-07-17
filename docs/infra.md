# infra — флот нод CasperVPN (Terraform + Ansible)

Слой, который поднимает, конфигурирует и ротирует ноды флота. Ядро ноды —
**sing-box** ([ADR-002](./decisions/ADR-002-singbox-core.md)). Канонический пин
версии sing-box один на весь репозиторий:
`infra/ansible/roles/singbox/defaults/main.yml` (`singbox_version`), и он обязан
совпадать с `.github/workflows/subscription.yml` (`env.SINGBOX_VERSION`) —
сейчас `1.11.11`. Оркестрацией
жизненного цикла рулит `services/orchestrator` через контракты `Node` /
`HealthEvent`; этот слой — исполнительные модули под ним.

Границы: всё живёт в `infra/` (+ этот док, `docs/mimicry-domains.md`, корневой
`Makefile`, `.github/workflows/infra-ci.yml`). Контракты (`packages/contracts`)
**заморожены** и не меняются.

## Что решает

«Нода за минуты», одноразовая, с мимикрией под легальный HTTPS/HTTP-3. Пять
инвариантов [АНТИ-БЛОК] прошиты в код (см. таблицу в конце).

## Раскладка

```
infra/
  terraform/
    versions.tf
    modules/
      compute/{hetzner,vultr,oci}/   # провайдер-абстракция, ЕДИНЫЙ интерфейс
      entry-node/                    # РОЛЬ entry (провайдер-агностик)
      exit-node/                     # РОЛЬ exit  (провайдер-агностик)
    envs/example/                    # референс-корень entry(Hetzner)+exit(Vultr)
  ansible/
    roles/{singbox,transports,firewall,health_metrics,rotation}/
    playbooks/{node-up,node-rotate,node-down}.yml
    molecule/default/               # docker-сценарий проверки
    inventory/                      # примеры (без реальных IP)
  scripts/{lib,control_plane,node_up,node_rotate,node_down}.sh
  ci/no-hardcode.sh
docs/{infra.md,mimicry-domains.md}
.github/workflows/infra-ci.yml
```

## Мульти-облако: как это устроено и как добавить провайдера

Провайдер — это **выбор compute-подмодуля** в корне env, а не рантайм-условие.
`entry-node`/`exit-node` — провайдер-агностик роль-модули: они выдают cloud-init
+ метки, но **не создают** ВМ и не знают про облако. Env склеивает роль → compute:

```hcl
module "entry_cfg" { source = "../../modules/entry-node"  ... }
module "entry_vm"  { source = "../../modules/compute/hetzner"
                     user_data = module.entry_cfg.user_data ... }
module "exit_cfg"  { source = "../../modules/exit-node"
                     allowed_entry_ip = module.entry_vm.ipv4 ... }
module "exit_vm"   { source = "../../modules/compute/vultr"
                     user_data = module.exit_cfg.user_data ... }
```

Референс-облака: **Hetzner + Vultr + OCI** — мейнстрим-облака, чьи IP-пулы не
выгружаются пачками как «VPN-хостеры».

**Добавить новое облако:** скопируй `modules/compute/<существующий>/` в
`modules/compute/<new>/`, замени ресурс ВМ. Держи **тот же контракт**:

- вход: `name, region, size, ssh_pubkey, user_data, tags, image`
- выход: `id, ipv4, ipv6`

Роль-модули и всё остальное не меняются.

## Транспорты (sing-box)

Нода несёт **несколько транспортов сразу** (разнообразие, не монокультура). Всё —
переменные в `ansible/roles/transports/defaults/main.yml`, mimicry-значения по
умолчанию пустые и **обязаны** приходить из конфига/vault.

| Транспорт | Роль | Заметки |
|-----------|------|---------|
| `vless-reality` | базовый TCP-фронт | REALITY: **нет своего серта/fingerprint**, крадёт хендшейк чужого сайта |
| `hysteria2` | QUIC/HTTP-3, мобильные/lossy | UDP-only → **обязателен TCP-фолбэк** (vless-reality) |
| `amneziawg` | скорость | jc/jmin/jmax/s1/s2/h1..h4 ломают энтропийный/сигнатурный детект WG |
| `shadowsocks-2022` | лёгкий фолбэк | AEAD-2022 |

Сборка `config.json` (`templates/config.json.j2`) валидируется `sing-box check`
при каждом рендере. Инварианты проверяются в `tasks/main.yml`:

- ≥1 транспорт включён;
- если `vless_reality` включён — `server_names`/`handshake_server`/`private_key`
  не пусты (иначе fail — mimicry не хардкодится, а подаётся);
- если `hysteria2` включён — обязан быть включён `vless_reality` (TCP-фолбэк на
  случай реза UDP/443).

### Почему это «настоящий HTTPS», а не «шум»

Инбаунд REALITY (`inbound_vless_reality.json.j2`) содержит **только** блок
`tls.reality` без `certificate` / `certificate_path` / `acme`. Сервер предъявляет
хендшейк реального `handshake_server`. Клиентский uTLS-фингерпринт — чужой
(`chrome`). Проверяется CI (`no-hardcode.sh` + molecule `verify.yml`: в конфиге
есть `reality`, нет `certificate`/`acme`).

## entry ≠ exit

- Раздельные роль-модули; entry и exit могут быть в **разных облаках**.
- Exit **не** публикует транспорт-порты; nftables на exit принимает вход **только**
  с IP парного entry (`allowed_entry_ip`, из cloud-init/inventory — не хардкод).
- Компрометация/фингерпринт entry не открывает и не достаёт до exit.

## Ноль хардкода

`infra/ci/no-hardcode.sh` (и джоба CI) падает, если в TF-модулях или
транспорт/firewall-шаблонах найден литеральный IPv4 или `http(s)://<домен>`.
Домены мимикрии — только в `docs/mimicry-domains.md` (кандидаты) и конфиге.

## Быстрый старт

Предпосылки (на машине оператора / в CI): `terraform`, `ansible`,
`ansible-galaxy collection install -r infra/ansible/requirements.yml`, `jq`,
`curl`. Секреты — только через env:

```bash
export HCLOUD_TOKEN=...            # entry (Hetzner)
export VULTR_API_KEY=...           # exit (Vultr)
export SSH_PUBKEY="ssh-ed25519 AAAA... ops@caspervpn"
export CONTROL_PLANE_URL=http://control-plane:8081
export CONTROL_PLANE_TOKEN=...     # internal bearer
```

```bash
# поднять пару entry+exit
make node-up REGION=hel1 CLOUD=hetzner

# ротировать ноду: свежий эфемерный IP + rekey REALITY, старый IP выводится,
# запись Node обновляется в control-plane — одной командой
make node-rotate NODE=<node-id>

# слить и снести ноду
make node-down NODE=<node-id>
```

Идемпотентность: `terraform apply` и Ansible сходятся; регистрация ноды терпит
`409`; retire терпит `404`. Тайминг-цель: нода поднимается **< 5 минут** «с нуля»
(cloud-init ставит только зависимости; sing-box — пре-собранный бинарь, без
компиляции; реальная конфигурация — Ansible по SSH).

## Интеграция с control-plane (по OpenAPI)

`scripts/control_plane.sh` строит объект `Node` строго по замороженной схеме и
ходит в control-plane (`packages/contracts/openapi/control-plane.yaml`):

| Действие | Вызов | Где в скриптах |
|----------|-------|----------------|
| Зарегистрировать ноду | `POST /v1/nodes` (тело — `Node`) | `node_up.sh` → `cp_register_node` |
| Обновить (ротация IP + REALITY) | `GET` затем `PATCH /v1/nodes/{id}` | `node_rotate.sh` → `cp_get_node` → `cp_patch_node` |
| Вывести ноду | `DELETE /v1/nodes/{id}` | `node_down.sh` → `cp_retire_node` |

Персональные `reality_short_id`/`uuid` юзеров — забота control-plane
(`POST /v1/users/{id}/rotate-secrets`); нода лишь принимает **пул** `short_ids` и
`reality_users` параметром, добавляя/убирая персональные short-id без раскрытия
других юзеров (per-user изоляция).

## Телеметрия здоровья (FieldSignal)

Роль `health_metrics` ставит systemd-timer, который каждые `health_interval`
проверяет листенеры транспортов и шлёт батч `FieldSignal` на telemetry ingest
(`POST /v1/signals`, `packages/contracts/openapi/telemetry.yaml`). Без PII:
только `node_id`/`transport_type`/`region`/`signal_type`/`success`.

## Ротация (дешевле обнаружения)

`make node-rotate NODE=..`:

1. `terraform apply -replace` парного entry-VM → **новый эфемерный IP** (старый
   инстанс и его «сгоревший» IP уничтожаются);
2. Ansible `node-up` на свежий entry, затем `node-rotate` (роль `rotation`):
   `sing-box generate reality-keypair` → новый keypair + свежий `short_id`,
   пере-рендер конфига, рестарт;
3. чтение артефакта `/etc/caspervpn/reality.pub` (новый pubkey/short_id);
4. `PATCH /v1/nodes/{id}` — новый `entry_ip` **и** пропагация нового REALITY —
   в схеме `Node` нет отдельного поля под pubkey/short-id, они живут внутри
   `transports[].vless_reality` (`public_key` + пул `short_ids`). Поэтому
   `node_rotate.sh` делает read-modify-write: `cp_get_node` → в jq патчит
   vless-reality транспорт (bump `public_key`, **добавляет** новый short-id в
   пул, не затирая пул) → `cp_patch_node` целым объектом (проходит
   `additionalProperties:false` и tagged-union валидацию). Если у ноды ещё нет
   vless-reality транспорта в control-plane — пишется WARN (pubkey некуда
   положить, сперва нужен transports-sync).

Асимметрия: регенерация ключа+IP дешевле, чем обнаружение ноды цензором.

## Тесты / CI (`.github/workflows/infra-ci.yml`)

| Джоба | Что делает |
|-------|-----------|
| `terraform` | `fmt -check`, `validate` каждого модуля и env, `plan` (fake vars, allowed-fail без кредов — apply не запускается) |
| `ansible` | `ansible-playbook --syntax-check` по всем плейбукам |
| `shellcheck` | `shellcheck -x` по всем скриптам |
| `no-hardcode` | падает на литеральном домене/IP в шаблонах |
| `molecule` | docker-нода: install → `sing-box check` → listener :443 → REALITY есть, серта нет |

Локально: `make infra-fmt infra-validate infra-syntax infra-molecule infra-nocode`
(нужны соответствующие тулзы; на голой машине их может не быть — тогда прогон в CI).

### Про live REALITY-хендшейк

Molecule проверяет: `sing-box check` проходит, порт слушает, конфиг содержит
`reality` и не содержит `certificate`/`acme`, TLS ClientHello принимается.
**Полный** end-to-end REALITY-хендшейк против реального домена мимикрии требует
egress и живого `handshake_server` → это staging-смоук (нужен выход в интернет),
не оффлайн-CI. Так честнее: контейнер без egress не «проксирует» на реальный сайт.

## Ограничения / ответственности

- Реальный `terraform apply` требует кредов облака; CI делает только
  validate/plan (без apply), как требует ТЗ.
- Клиентский каскад переключения транспортов — на клиенте (Happ,
  [ADR-003](./decisions/ADR-003-external-clients-happ.md)); нода лишь гарантирует
  наличие TCP-фолбэка при включённом hysteria2.
- `amneziawg`-инбаунд требует amneziawg-совместимой сборки sing-box; проверь имя
  ключа обфускации под свою сборку (по умолчанию транспорт выключен).
- `envs/example` — референс на одну пару; полноценный флот (много нод, per-node
  state, автозамена по блок-детекту) — на `services/orchestrator`.

## Карта инвариантов [АНТИ-БЛОК] → код

| Инвариант | Где закрыт |
|-----------|-----------|
| entry ≠ exit | раздельные роль-модули; `firewall` exit-allow-list по `allowed_entry_ip` |
| ноль хардкода | `ci/no-hardcode.sh`; mimicry только переменные + `docs/mimicry-domains.md` |
| < 5 мин, ротация одной командой | cloud-init минимальный + пре-собранный sing-box; `make node-up/node-rotate` |
| настоящий HTTPS, не шум; нет своего серта/fp | `inbound_vless_reality.json.j2` только `reality`, без cert/acme; molecule-assert |
| AmneziaWG ломает детект WG | `inbound_amneziawg.json.j2` jc/jmin/jmax/s1/s2/h1..h4 |
| UDP/443 рез → hysteria2 TCP-фолбэк | assert: hysteria2 ⇒ vless-reality включён |
| per-user short-id | пул `short_ids` + `reality_users`; персоналка через control-plane |
