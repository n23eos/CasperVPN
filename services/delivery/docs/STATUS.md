# Delivery — статус подсистемы (handoff-заметка)

Дата: 2026-07-07. Ветка: `feat/delivery-multichannel`. Автор части: delivery.
Границы соблюдены — правки только в `services/delivery/`. Контракты не менялись.

> **Обновление 2026-07-11:** закрыты два самодостаточных hardening-пункта:
> `POST /v1/channels` защищён bearer-токеном и fail-closed при пустом токене,
> добавлен `/readyz`; resolver получил опциональный anti-rollback max-age по
> `Artifact.IssuedAt` с failover на свежий канал.

## Стадия: НЕ «всё готово»

Завершена **чётко очерченная стадия**: медиа-независимое ядро (артефакт + подпись +
шифр указателя) и **4 канала доставки** разной природы с e2e-доказательством
«артефакт через любой канал восстанавливается и проходит проверку подписи». Всё
собрано, `make build/vet/test` зелёные, покрытие каждого пакета ≥80%.

**Что это НЕ значит:** сервис ещё не подключён к соседним подсистемам и к реальным
внешним медиа. Многие каналы по умолчанию **read-only** (интерфейс есть, prod-адаптер
публикации — нет). Это ожидаемо для фазы: сначала ядро+контракты каналов, потом
интеграция.

## ✅ Сделано (готово к ревью/использованию как библиотека)

- `internal/artifact` — конверт, детерминированная сериализация, `Sign`/`Open`.
- `internal/sign` — Ed25519, verifier-keyring (ротация ключей).
- `internal/seal` — AES-256-GCM шифр указателя.
- `internal/pointer` — `Directory` + `Pack`/`Unpack` (verify→decrypt).
- `internal/channel` — интерфейс, registry (динамич.), resolver (failover), publisher.
- Каналы: `telegram` (+Max-адаптер, rate-limit/антиспам), `dns` (DoH+TXT),
  `gitraw` (ротация зеркал), `steg` (стего в analytics-JSON).
- `internal/httpapi` — `/healthz`, `/readyz`, `GET/POST /v1/channels`,
  `GET /d/{channel}/{token}`; mutating admin surface требует bearer-токен.
- `internal/config`, `internal/app`, `internal/memkv`, `cmd/delivery`.
- `internal/bridge` — Snowflake-модель как интерфейс-заглушка.
- `docs/delivery.md` — threat-model по каждому каналу.
- Тесты: unit по каналам (моки), sign/verify, seal, failover, e2e round-trip, HTTP.

## ❌ НЕ сделано / заглушки (следующая фаза)

**Интеграция с соседями (главное):**
- **`SubProvider` не реализован** — бот знает форму ответа, но источник
  subscription-ссылок (клиент к `services/subscription`) не подключён. Бот выдаёт
  ссылку только с инъецированным провайдером.
- **Приём апдейтов бота не подключён** — `HandleUpdate` есть, но нет long-poll/webhook
  цикла, который его кормит. Бот пока не «живой».
- **Цикл обновления directory отсутствует** — нет шедулера, который пересобирает
  `Directory` из control-plane/БД и `Broadcast`-ит по всем каналам при
  ротации/блоке. `Publisher.Broadcast` есть, но его никто не вызывает.

**Реальные внешние адаптеры публикации (сейчас каналы fetch-only):**
- DNS `Publisher.SetTXT` — нет prod-реализации через API DNS-провайдера.
- gitraw `RawWriter` — нет реализации коммита файла в репо (наполнение зеркал — CI).
- steg `Carrier` — нет реального endpoint’а, только in-memory фейк в тестах.
- Telegram `BlobStore` = in-memory (`memkv`) → не переживает рестарт, не кросс-процесс.

**Ключи / крипта-операции:**
- Ключи подписи/шифра — только из env. **Нет процедуры ротации**, нет раздачи pubkey
  клиентам, нет endpoint’а публикации pubkey. Ephemeral-fallback — только dev.
- Anti-rollback по времени закрыт опциональным `Artifact.IssuedAt` max-age в
  resolver. Проверка монотонности `Directory.Revision` между процессными
  рестартами остаётся будущей задачей, потому что требует persistent client state.

**Петля обратной связи (телеметрия):**
- Не потребляет `FieldSignal`/`HealthEvent` → нет автоснятия заблокированных
  каналов/зеркал. `Registry.Remove` есть, но его никто не дёргает.

**Прочее:**
- `bridge` — целиком заглушка (`ErrNotImplemented`): нет rendezvous/NAT-traversal.
- `POST /v1/channels` — только валидация дескриптора, живые каналы не конструирует.
- Нет HTTPS-канала (`KindHTTPS` объявлен, не разведён) — базовый HTTPS как ещё один
  транспорт не строил (фокус задачи — 4 альт-канала).
- Нет rate-limit на HTTP-путях (`/d/`), нет метрик/алертинга/health-check
  интеграции с LB. Админский `POST /v1/channels` теперь закрыт bearer-токеном.
- Стего: один статичный шаблон cover, без ротации; низкая ёмкость, хрупкость
  (описано в docs). Не «продакшн-хардненно».
- Нет персистентности/БД в delivery.

## 🔭 Что отслеживать потом

- Здоровье реальных каналов (`GET /v1/channels` → `healthy`) после подключения живых
  endpoint’ов — сейчас doh/dns_txt/github_raw = `false` на фейковых адресах.
- Событие ротации ключа подписи (клиенты должны принимать старый+новый — keyring
  готов, нужен процесс раздачи).
- Работает ли автоснятие канала по телеметрии, когда петля будет подключена.
- Доля восстановлений через каждый канал (какой реально несёт нагрузку, какой мёртв).

## Как запустить / проверить

```bash
make build && make vet && go test ./services/delivery/...
# локальный прогон:
PORT=18083 DELIVERY_DNS_ZONE=cfg.example. \
  DELIVERY_DOH_ENDPOINT=https://r.example/dns-query \
  DELIVERY_GITRAW_MIRRORS=https://raw.example/repo \
  go run ./services/delivery/cmd/delivery
curl -s localhost:18083/v1/channels
```

Prod-переменные: `DELIVERY_SIGN_SEED` (b64 32B ed25519 seed),
`DELIVERY_SEAL_KEY` (b64 32B), `DELIVERY_VERIFY_KEYS` (`id:pub,...`),
`DELIVERY_TELEGRAM_BASE/TOKEN`, `DELIVERY_MAX_BASE/TOKEN`,
`DELIVERY_ADMIN_TOKEN`, `DELIVERY_ARTIFACT_MAX_AGE`.
