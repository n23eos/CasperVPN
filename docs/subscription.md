# Subscription service — выдача конфигов внешним клиентам

Сервис `services/subscription`. Отдаёт по subscription-ссылке **персональный,
много-транспортный** набор точек входа в трёх форматах, которые понимают внешние
клиенты (Happ, sing-box, Clash.Meta/mihomo). Свои приложения не пишем
([ADR-003](./decisions/ADR-003-external-clients-happ.md)); ядро нод — sing-box
([ADR-002](./decisions/ADR-002-singbox-core.md)).

Один канонический набор (`contracts.SubscriptionBundle`) рендерится **тремя**
представлениями замороженными рендерерами контрактов (`ToBase64List`,
`ToSingBoxJSON`, `ToClashMetaYAML`). Сервис их **потребляет**, добавляя сверху
персонализацию, split-tunnel-роутинг, заголовки Happ, кеш и инвалидацию.

## Эндпоинты

| Метод | Путь | Назначение |
|-------|------|-----------|
| `GET` | `/sub/{token}` | подписка в согласованном формате (base64 / sing-box / clash) |
| `GET` | `/sub/{token}/nodes` | сырой канонический `SubscriptionBundle` (JSON) |
| `GET` | `/sub/{token}/happ` (или `/sub/{token}?deeplink=1`) | deep-link `happ://add/<base64>` |
| `GET` | `/healthz` | liveness |
| `POST` | `/internal/tokens` | регистрация `token → (user, subscription)` *(bearer)* |
| `POST` | `/internal/revoke` | отзыв токена (утёкшая ссылка) *(bearer)* |
| `POST` | `/internal/invalidate` | инвалидация кеша по сигналу control-plane *(bearer)* |

`/internal/*` защищены `Authorization: Bearer $INTERNAL_TOKEN`; при пустом
`INTERNAL_TOKEN` они отключены (404).

## Выбор формата

Приоритет: query `?format=` → заголовок `Accept` → `User-Agent` → по умолчанию `base64`.

| Формат | `?format=` | Accept | User-Agent | MIME |
|--------|-----------|--------|-----------|------|
| Base64 URI-список | `base64` | `text/plain` | Happ, v2rayN, nekobox, streisand | `text/plain` |
| sing-box JSON | `singbox` | `application/json` | sing-box | `application/json` |
| Clash-Meta YAML | `clash` | `application/yaml` | clash, mihomo, meta | `application/yaml` |

**Покрытие AmneziaWG** (замороженное решение контрактов): у AmneziaWG нет
портируемой URI-схемы, поэтому он **исключён из base64-списка**, но **присутствует**
в sing-box JSON и Clash YAML. Base64 несёт `vless-reality`, `hysteria2`,
`shadowsocks-2022`. В любой выдаче всегда ≥2 независимых транспорта — клиент
переключается при блокировке одного (см. [АНТИ-БЛОК](#анти-блок)).

## Примеры ссылок под Happ

Subscription URL приходит клиенту через delivery-каналы (Агент D: HTTPS/DoH/
Telegram/GitHub raw/DNS TXT), **не** через единственный статический домен. Ниже
`{host}` — любой рабочий канал.

```
# Универсальная подписка (Happ подхватывает base64 автоматически):
https://{host}/sub/dev-token-abc123

# Deep-link для добавления в Happ одним тапом:
happ://add/<base64(subscription_url)>
#   получить готовый:
curl https://{host}/sub/dev-token-abc123/happ
#   -> happ://add/aHR0cHM6Ly9...  (base64 самого URL подписки)

# Явный формат:
https://{host}/sub/dev-token-abc123?format=singbox
https://{host}/sub/dev-token-abc123?format=clash

# Персонализация по региону/платформе:
https://{host}/sub/dev-token-abc123?region=eu-central&platform=android
```

### Заголовки ответа (Happ / sing-box)

Проверено по [dev-docs Happ](https://www.happ.su/main/dev-docs/app-management).

| Заголовок | Значение | Смысл |
|-----------|----------|-------|
| `Profile-Update-Interval` | целое (часы) | клиент сам регулярно тянет свежий список → заблокированные ноды быстро вымываются |
| `Subscription-Userinfo` | `upload=; download=; total=; expire=` | трафик/квота/срок (Unix) для UI |
| `Profile-Title` | `base64:<UTF-8>` | имя профиля |
| `Announce` | `base64:<UTF-8>` | баннер-объявление |
| `Support-Url`, `Profile-Web-Page-Url` | URL | кнопки в UI |
| `Routing-Enable` | `1` | включить клиентский роутинг (split-tunnel идёт в теле конфигов) |

## Персонализация

Рендерер вшивает персональные `uuid` и `reality_short_id` юзера в **каждый** прокси
(per-user изоляция: блок одного не палит остальных на общей ноде). Дополнительно
`services/subscription` фильтрует набор:

- **план** — `basic` исключает ноды с меткой `tier=premium` и ограничивает число
  нод; `unlimited` — весь набор.
- **регион** (`?region=`) — предпочитает ноды региона, но никогда не оставляет
  юзера без нод (если совпадений нет — фильтр игнорируется).
- **платформа** (`?platform=`) — исключает ноды с меткой `platform_exclude=<platform>`.
- **blocked** — ноды со `status=blocked/retired/...` или меткой `blocked=true`
  исключаются всегда.

**Инвариант диверсити:** если фильтры схлопнули бы набор в монокультуру (`<2`
семейств транспорта), а разнообразие доступно — фильтр автоматически ослабляется,
чтобы в подписке осталось несколько независимых транспортов.

## Split-tunnel роутинг

Замороженный `SingBoxConfig` несёт только `outbounds`; `route`/`dns` контракт
намеренно оставляет потребителю. Сервис оборачивает набор:

- **sing-box JSON:** добавляет `urltest` (`auto`) + `selector` (`proxy`) —
  fallback-каскад клиента; `direct`; блок `route` с правилами «RU geosite/geoip →
  `direct` (мимо туннеля), остальное → `proxy`» и `dns` с раздельными резолверами.
- **Clash YAML:** добавляет `proxy-groups` (`PROXY` select + `AUTO` url-test),
  `rule-providers`, `rules` (RU → `DIRECT`, финал `MATCH,PROXY`) и `dns`.

**Ноль хардкода:** RU-категории, URL rule-set’ов, DNS-резолверы и probe-URL живут
в **конфиге** (`services/subscription/config/routing.ru.json`), не в коде.
Оператор/control-plane заменяют файл целиком через `ROUTING_POLICY_FILE`.

## Кеш и инвалидация

Отрендеренные payload’ы кешируются (`token|format|персонализация`) на `CACHE_TTL`.
Инвалидация:

- **Нода `blocked`** → control-plane шлёт `POST /internal/invalidate` (`{"node_id":…}`)
  → bump версии кеша → все подписки пересобираются без заблокированной ноды.
- **Отзыв токена** → `POST /internal/revoke` (`{"token":…}`) → токен удаляется из
  индекса + сброс кеша.

Вместе с `Profile-Update-Interval` это быстро вымывает заблокированные ноды из
клиентов.

## <a name="анти-блок"></a>Соблюдение [АНТИ-БЛОК]

1. **Много транспортов, не монокультура** — в каждой подписке ≥2 независимых
   транспорта (reality + amneziawg + hysteria2 + ss); инвариант диверсити не даёт
   фильтрам схлопнуть набор.
2. **Per-user изоляция** — персональные `uuid`/`reality_short_id` в каждом прокси.
3. **Не один домен** — URL подписки переносим; выдача не привязана к домену,
   доставка — через Агента D. `happ://add` строится из адреса пришедшего запроса.
4. **Быстрое вымывание** — `Profile-Update-Interval` + инвалидация по сигналу.
5. **Ноль хардкода** — все домены/IP/резолверы из control-plane и конфига.

## Конфигурация (env)

| Переменная | По умолчанию | Назначение |
|-----------|--------------|-----------|
| `PORT` | `8082` | порт HTTP |
| `CONTROL_PLANE_URL` | — | база control-plane API; пусто → in-memory (dev) |
| `CONTROL_PLANE_TOKEN` | — | внутренний bearer к control-plane |
| `INTERNAL_TOKEN` | — | bearer для `/internal/*` (пусто → отключены) |
| `DATABASE_URL` | — | DSN Postgres для token index (durable); пусто → in-memory. Схему `internal/controlplane/schema.sql` применять вне старта |
| `PROFILE_UPDATE_INTERVAL_HOURS` | `12` | значение `Profile-Update-Interval` |
| `CACHE_TTL` | `5m` | TTL кеша выдачи |
| `ROUTING_POLICY_FILE` | `config/routing.ru.json` | split-tunnel policy |
| `SUBSCRIPTION_PROFILE_TITLE` / `_ANNOUNCE` / `_SUPPORT_URL` / `_WEB_PAGE_URL` | — | UI-хинты Happ |
| `SUBSCRIPTION_FIXTURES` | — | dev-only: сид in-memory из JSON |

## Локальный запуск

```bash
cd services/subscription
PORT=18082 INTERNAL_TOKEN=devsecret \
  SUBSCRIPTION_FIXTURES=config/dev-fixtures.json \
  SUBSCRIPTION_PROFILE_TITLE=CasperVPN \
  go run ./cmd/subscription
# затем:
curl -H 'User-Agent: Happ/1.0' localhost:18082/sub/dev-token-abc123 | base64 -d
curl 'localhost:18082/sub/dev-token-abc123?format=singbox'
curl 'localhost:18082/sub/dev-token-abc123/happ'
```

## Тесты

- **Golden ×3** (`internal/render/testdata/*`): base64, sing-box JSON, Clash YAML
  сравниваются с эталоном (обновление — `UPDATE_GOLDEN=1 go test ./internal/render`).
- **sing-box check** (`scripts/singbox-check.sh`, CI-job `singbox-check`):
  реальный `sing-box check` на отрендеренном конфиге. Фикстура — mainline-транспорты
  (reality + hysteria2 + ss) + полный split-tunnel-обёртка. **AmneziaWG исключён из
  этой проверки:** mainline sing-box его не реализует (ноды/клиенты несут
  AmneziaWG-совместимый core); присутствие AmneziaWG в полной выдаче покрыто
  структурными Go-тестами.
- **Fuzz** (`FuzzSubToken`): произвольные токены не роняют хендлер и всегда дают
  валидный код (200/401/404/410/…).
- **Инвариант диверсити** и **инвалидация кеша** — в `internal/httpapi` и
  `internal/personalize`.

```bash
cd services/subscription
go test ./...
go test ./internal/httpapi -run=none -fuzz=FuzzSubToken -fuzztime=30s
UPDATE_GOLDEN=1 go test ./internal/render   # перегенерить эталоны
```
