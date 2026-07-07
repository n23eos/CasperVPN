# CasperVPN — Контракты (заморожено)

Единый источник межсервисных типов — `packages/contracts/`. Один и тот же набор
описан **двумя** способами, которые обязаны совпадать:

- **Go:** `packages/contracts/*.go` (пакет `contracts`).
- **JSON Schema:** `packages/contracts/schema/contracts.schema.json` (draft 2020-12).
- **HTTP-интерфейсы:** `packages/contracts/openapi/*.yaml` (OpenAPI 3.1) ссылаются на JSON Schema через `$ref`.

> 🧊 **ЗАМОРОЗКА.** После этой волны файлы в `packages/contracts/` заморожены.
> 6 команд строят подсистемы параллельно поверх этих типов. Правила изменений — в конце.

Все wire-значения (JSON-имена полей, значения enum) **стабильны**. Смена значения
enum, переименование или удаление поля = ломающее изменение.

Инвариант проекта: **ни одного домена мимикрии и ни одного IP в коде** — только через
эти поля из конфига/БД.

---

## Enum'ы

| Тип | Значения (wire) | Назначение |
|-----|-----------------|------------|
| `NodeStatus` | `provisioning`, `active`, `degraded`, `blocked`, `draining`, `retired` | жизненный цикл ноды |
| `NodeRole` | `entry`, `exit`, `combined` | разделение entry/exit (антиблок) |
| `TransportType` | `vless-reality`, `amneziawg`, `hysteria2`, `shadowsocks-2022` | семейство транспорта |
| `TransportVersion` | `v1` | ревизия схемы параметров транспорта (негоциация) |
| `UserStatus` | `active`, `suspended`, `expired`, `banned` | состояние аккаунта |
| `SubscriptionStatus` | `trialing`, `active`, `past_due`, `canceled`, `expired` | состояние подписки |
| `SubscriptionPlan` | `basic`, `unlimited` | тариф (≈100 ₽ / ≈200–300 ₽) |
| `SignalType` | `ok`, `reset`, `timeout`, `dpi_block`, `probe`, `throttle`, `dns_poison`, `handshake_fail` | тип полевого сигнала |
| `HealthStatus` | `healthy`, `degraded`, `blocked`, `unreachable` | вердикт оркестратора по ноде |
| `DeliveryChannel` | `https`, `doh`, `telegram`, `github_raw`, `dns_txt` | канал доставки подписки |
| `Platform` | `android`, `ios`, `windows`, `macos`, `linux` | платформа клиента (грубо) |

---

## Node

Нода флота. Антиблок-ДНК: разделение entry/exit, быстрая ротация, много транспортов сразу.

| Поле (JSON) | Тип | Обяз. | Описание |
|-------------|-----|:----:|----------|
| `id` | string | ✔ | идентификатор ноды |
| `role` | `NodeRole` | ✔ | entry/exit/combined |
| `status` | `NodeStatus` | ✔ | текущее состояние |
| `entry_node_id` | string | — | **только для exit**: ссылка на парный entry (гарантия entry≠exit) |
| `provider` | string | ✔ | облако-провайдер (hetzner/vultr/gcore…), мультиоблако |
| `cloud` | string | ✔ | логический бакет облака/аккаунта |
| `region` | string | ✔ | регион (напр. eu-central) |
| `entry_ip` | string | — | текущий ingress-адрес |
| `ephemeral_entry_ip` | bool | ✔ | entry_ip одноразовый → агрессивная ротация |
| `transports` | `[]Transport` | ✔ | предлагаемые транспорты (несколько семейств — не монокультура) |
| `capacity_users` | int | — | мягкий fair-use лимит юзеров (экономика shared-нод) |
| `rotate_after` | date-time | — | момент, после которого ноду ротировать (слить+вывести) |
| `created_at` | date-time | ✔ | |
| `expires_at` | date-time | — | |
| `labels` | map[string]string | — | произвольные операционные метки |

**Антиблок:** `role`+`entry_node_id` (атака на entry не палит exit); `rotate_after`+`ephemeral_entry_ip`
(регенерация дешевле обнаружения); `transports[]` (разнообразие).

---

## Transport

Тегированный union: `type` выбирает ровно один непустой блок параметров. Нода держит
много `Transport` сразу — клиент live-переключается по fallback-каскаду.

| Поле (JSON) | Тип | Обяз. | Описание |
|-------------|-----|:----:|----------|
| `tag` | string | ✔ | уникальная в пределах ноды метка |
| `type` | `TransportType` | ✔ | выбирает активный вариант ниже |
| `version` | `TransportVersion` | ✔ | ревизия схемы параметров для `type` |
| `port` | int | ✔ | порт прослушки на ноде |
| `enabled` | bool | ✔ | false → анонсируется, но не отдаётся |
| `priority` | int | ✔ | меньше = раньше пробуется в каскаде |
| `vless_reality` | `VlessRealityParams` | по type | вариант |
| `amneziawg` | `AmneziaWGParams` | по type | вариант |
| `hysteria2` | `Hysteria2Params` | по type | вариант |
| `shadowsocks_2022` | `Shadowsocks2022Params` | по type | вариант |

`Transport.Validate()` требует: `type` и `version` известны, и ровно один вариант, совпадающий с `type`.

### VlessRealityParams (`vless-reality`)

VLESS + XTLS-Vision + REALITY. `server_names`/`dest` — **цель мимикрии из конфига**, не хардкод.

| Поле | Тип | Обяз. | Описание |
|------|-----|:----:|----------|
| `server_names` | []string | ✔ | SNI мимикрии, предъявляемые сети |
| `dest` | string | ✔ | реальный upstream `host:port`, чей хендшейк «крадём» |
| `public_key` | string | ✔ | REALITY x25519 публичный ключ сервера |
| `short_ids` | []string | ✔ | пул допустимых short-id; каждому юзеру выдаётся свой (per-user изоляция) |
| `flow` | string | ✔ | напр. `xtls-rprx-vision` |
| `fingerprint` | string | — | uTLS fingerprint клиента (напр. `chrome`) |
| `spider_x` | string | — | REALITY spiderX-путь |

### AmneziaWGParams (`amneziawg`)

Обфусцированный WireGuard (ломает энтропийный/сигнатурный детект WG). Ключи `jc/s/h` — обфускация.

| Поле | Тип | Обяз. | Описание |
|------|-----|:----:|----------|
| `public_key` | string | ✔ | WG публичный ключ сервера |
| `mtu` | int | — | MTU |
| `jc` | int | ✔ | число junk-пакетов |
| `jmin` | int | ✔ | мин. размер junk-пакета |
| `jmax` | int | ✔ | макс. размер junk-пакета |
| `s1` | int | ✔ | junk init-пакета |
| `s2` | int | ✔ | junk response-пакета |
| `h1`..`h4` | int64 | ✔ | magic headers 1–4 |

### Hysteria2Params (`hysteria2`)

Hysteria2 поверх QUIC/HTTP-3 для мобильных/lossy-сетей, с обфускацией и клиентским TCP-фоллбэком.

| Поле | Тип | Обяз. | Описание |
|------|-----|:----:|----------|
| `obfs` | string | — | тип обфускации (напр. `salamander`) |
| `obfs_password` | string | — | секрет обфускации |
| `password` | string | ✔ | секрет аутентификации |
| `sni` | string | ✔ | SNI мимикрии (конфиг) |
| `up_mbps` | int | — | подсказка congestion |
| `down_mbps` | int | — | подсказка congestion |
| `insecure` | bool | — | пропуск проверки серта (self-signed фронты) |

### Shadowsocks2022Params (`shadowsocks-2022`)

Shadowsocks-2022 (AEAD-2022), лёгкий запасной транспорт. Персональный ключ юзера — поверх PSK.

| Поле | Тип | Обяз. | Описание |
|------|-----|:----:|----------|
| `method` | string | ✔ | напр. `2022-blake3-aes-256-gcm` |
| `psk` | string | ✔ | серверный pre-shared key (base64) |

---

## User

Аккаунт. Антиблок-ДНК: per-user изоляция — свой `reality_short_id` и `uuid`/ключ, блок
одного юзера не палит остальных на общей ноде.

| Поле (JSON) | Тип | Обяз. | Описание |
|-------------|-----|:----:|----------|
| `id` | string | ✔ | идентификатор |
| `telegram_id` | int64 | — | Telegram (основной канал масс-маркета) |
| `email` | string | — | опционально |
| `status` | `UserStatus` | ✔ | состояние аккаунта |
| `reality_short_id` | string | ✔ | **персональный** REALITY short-id |
| `uuid` | string | ✔ | **персональный** VLESS UUID |
| `private_key` | string | — | серверный секрет транспорта; **не отдавать в клиентские payload** |
| `subscription_id` | string | — | активная подписка |
| `device_limit` | int | ✔ | антиперепродажа (лимит устройств) |
| `quota_bytes` | uint64 | ✔ | лимит трафика; `0` = безлимит |
| `used_bytes` | uint64 | ✔ | учёт для fair-use throttle |
| `created_at`/`updated_at` | date-time | ✔ | |

---

## Subscription

Биллинговое право за юзером; определяет, что выдаётся в подписке.

| Поле (JSON) | Тип | Обяз. | Описание |
|-------------|-----|:----:|----------|
| `id` | string | ✔ | идентификатор |
| `user_id` | string | ✔ | владелец |
| `plan` | `SubscriptionPlan` | ✔ | тариф |
| `status` | `SubscriptionStatus` | ✔ | состояние |
| `token` | string | ✔ | секрет в subscription URL; ротируемый (отзыв ссылки без трогания аккаунта) |
| `traffic_limit_bytes` | uint64 | ✔ | `0` = безлимит; драйвит fair-use throttle |
| `speed_limit_mbps` | int | ✔ | `0` = без лимита |
| `device_limit` | int | ✔ | лимит устройств |
| `starts_at` | date-time | ✔ | |
| `expires_at` | date-time | — | `null` = бессрочно |
| `created_at`/`updated_at` | date-time | ✔ | |

---

## FieldSignal

Одно **анонимное** наблюдение клиента — топливо петли «где что заблокировали».

> 🔒 **Инвариант приватности:** `FieldSignal` НЕ несёт идентичности юзера. География
> грубая (region/ASN/ISP), не точная. Корреляция по ноде/транспорту, не по юзеру.

| Поле (JSON) | Тип | Обяз. | Описание |
|-------------|-----|:----:|----------|
| `signal_id` | string | ✔ | клиентский dedup-ключ (случайный, неидентифицирующий) |
| `node_id` | string | ✔ | что пробовали |
| `transport_type` | `TransportType` | ✔ | |
| `transport_version` | `TransportVersion` | ✔ | |
| `region` | string | ✔ | грубая гео, напр. `RU-MOW` |
| `asn` | int | — | наблюдённый ASN |
| `isp` | string | — | наблюдённый оператор |
| `signal_type` | `SignalType` | ✔ | исход (reset/timeout/dpi_block/…) |
| `success` | bool | ✔ | удалось ли соединение |
| `rtt_ms` | int | — | RTT когда измерим |
| `loss_pct` | float | — | потери 0..100 |
| `client_version` | string | — | версия клиента (грубо) |
| `platform` | `Platform` | — | платформа (грубо) |
| `observed_at` | date-time | ✔ | |

## HealthEvent

Собственный **авторитетный** вердикт оркестратора по ноде из активных проб (в т.ч. изнутри РФ).

| Поле (JSON) | Тип | Обяз. | Описание |
|-------------|-----|:----:|----------|
| `node_id` | string | ✔ | нода |
| `status` | `HealthStatus` | ✔ | вердикт |
| `probe_source` | string | ✔ | откуда проба (напр. `ru-probe-1`) — блок скоупится по региону |
| `blocked_from_regions` | []string | — | регионы, где нода выглядит заблокированной |
| `latency_ms` | int | — | |
| `detail` | string | — | |
| `observed_at` | date-time | ✔ | |

---

## Формат выдачи подписки

Один канонический набор (`SubscriptionBundle` = `user` + `nodes[]` + `generated_at`)
рендерится **тремя** способами. Рендерер вшивает персональные секреты юзера
(`uuid`, `reality_short_id`) в каждый прокси — выдача per-user.

| Представление | Метод (Go) | MIME | Потребители |
|---------------|-----------|------|-------------|
| Base64-список URI | `ToBase64List()` | `text/plain` | Happ, v2rayN, импорт sing-box |
| sing-box JSON | `ToSingBoxJSON()` | `application/json` | нативно sing-box |
| Clash-Meta YAML | `ToClashMetaYAML()` | `application/yaml` | Clash.Meta / mihomo |

**🧊 Замороженное решение о покрытии AmneziaWG.** У AmneziaWG нет портируемой URI-схемы,
поэтому он **исключён из base64-списка URI**, но **присутствует** в sing-box JSON и
Clash-Meta YAML (оба моделируют WireGuard/Amnezia нативно). Base64-список несёт
`vless-reality`, `hysteria2`, `shadowsocks-2022`.

Формы URI в base64-списке:
- `vless://{uuid}@{addr}:{port}?security=reality&sni=…&pbk=…&sid={reality_short_id}&flow=…#{tag}`
- `hysteria2://{password}@{addr}:{port}?sni=…&obfs=…&obfs-password=…&insecure=0|1#{tag}`
- `ss://{base64url(method:psk)}@{addr}:{port}#{tag}`

sing-box outbounds отдаются как объекты (`type: vless|hysteria2|shadowsocks|wireguard`);
формы следуют текущим соглашениям sing-box и версионируются через `TransportVersion`.

---

## Правила изменения (после заморозки)

1. **Аддитивно и обратно совместимо** — новое **опциональное** поле можно добавить
   через ревью, синхронно в Go **и** JSON Schema **и** (если влияет на API) OpenAPI.
2. **Ломающее** (смена/удаление enum-значения, переименование/удаление поля, смена
   типа) — только через координированную миграцию и **bump `TransportVersion`**
   (или новой версии контракта), не молча.
3. Go и JSON Schema обязаны совпадать; расхождение — баг контракта.
4. Секреты (`private_key`, серверные PSK/пароли) **никогда** не попадают в
   клиентские/публичные payload’ы, только в серверные пути.
