# Telemetry — петля обратной связи «где что заблокировали»

Сервис `services/telemetry`. Принимает **анонимные** `FieldSignal` из поля,
агрегирует по `(region, transport_type)`, детектит «этот транспорт в этом регионе
умер» и отдаёт control-plane готовые к автоматике рекомендации ротации. Плюс
принимает авторитетные `HealthEvent` от проб оркестратора и отдаёт метрики
Prometheus/OpenMetrics для дашбордов.

Источник истины по типам — `packages/contracts` (заморожены). Внешних зависимостей
нет: весь сервис на stdlib + contracts, как и остальной монорепо.

## Контракт данных

- **`FieldSignal`** (`contracts/signal.go`) — одно анонимное наблюдение клиента/ноды:
  `node_id`, `transport_type/version`, грубая гео (`region`/`asn`/`isp`), исход
  (`signal_type`, `success`, `rtt_ms`, `loss_pct`), грубый контекст (`platform`,
  `client_version`), `observed_at`, `signal_id` (случайный dedup-ключ).
  **Инвариант приватности:** в `FieldSignal` нет ничего, что идентифицирует юзера.
- **`HealthEvent`** — вердикт оркестратора по ноде из активного пробинга
  (авторитетно, не анонимно). Заводится через аутентифицированный `/v1/health`.

## API

| Метод | Путь | Доступ | Назначение |
|-------|------|--------|------------|
| POST | `/v1/signals` | публично | приём батча анонимных `FieldSignal` (ноды/клиенты) |
| POST | `/v1/health` | bearer | приём `HealthEvent` от проб оркестратора |
| GET | `/v1/aggregates` | bearer | текущие агрегаты по `(region, transport)` |
| GET | `/v1/recommendations` | bearer | вход оркестратора: `mark_node_blocked`, `prioritize_transport` |
| GET | `/metrics` | bearer | OpenMetrics для Prometheus |
| GET | `/healthz` | публично | liveness |

Сигнатуры `/v1/signals` и `/v1/health` — из `packages/contracts/openapi/telemetry.yaml`
(коды 202/400/413/429/401). `/v1/aggregates`, `/v1/recommendations`, `/metrics` —
собственная поверхность сервиса (не трогают замороженные контракты).

Аутентификация внутренних эндпоинтов — bearer-токен `TELEMETRY_INTERNAL_TOKEN`,
сравнение в постоянном времени. Пустой токен = гейт выключен (только dev, main
пишет WARNING).

### Пример: приём сигналов

```bash
curl -XPOST localhost:8085/v1/signals -d '{
  "signals": [
    {"signal_id":"a1","node_id":"node-x","transport_type":"vless-reality",
     "region":"RU-MOW","asn":12345,"signal_type":"dpi_block",
     "observed_at":"2026-07-07T11:59:00Z"}
  ]
}'
# → 202 {"accepted":1,"rejected":0}
```

### Пример: рекомендации control-plane

```jsonc
// GET /v1/recommendations
{
  "generated_at": "2026-07-07T12:00:00Z",
  "window_seconds": 900,
  "node_blocks": [
    {"action":"mark_node_blocked","node_id":"node-x","regions":["RU-MOW"],
     "confidence":"corroborated","reason":"6/6 field sources blocked (share 1.00)"}
  ],
  "region_priorities": [
    {"action":"prioritize_transport","region":"RU-MOW",
     "recommended_transport":"hysteria2",
     "ranked":[{"transport":"hysteria2","score":1,"dead":false,"sources":7},
               {"transport":"vless-reality","score":0,"dead":true,"sources":6}],
     "reason":"prefer hysteria2 (score 1.00, 7 sources)"}
  ]
}
```

`confidence`: `authoritative` (проба оркестратора / `HealthEvent`) выше, чем
`corroborated` (согласие независимых полевых источников).

## Как выполнен [АНТИ-БЛОК]

### 1. Анонимность по построению
- **Строгий декод** (`DisallowUnknownFields`): батч с любым лишним полем (попытка
  протащить PII вроде `user_id`) отклоняется целиком → 400.
- **Санитайзер** (`internal/ingest/anonymize.go`): регион огрубляется до
  `^[A-Z]{2}(-[A-Z0-9]{1,4})?$` (мелкая гео/мусор → reject), `asn`/`rtt`/`loss`
  клэмпятся, `isp`/`client_version` режутся по длине и от control-символов,
  неизвестный `platform` сбрасывается, `observed_at` из будущего/за ретеншном →
  reject, иначе **обрезается до минуты** (гасит тайминг-корреляцию).
- **IP клиента не хранится и не логируется.** Он используется только для
  эфемерного bucket rate-limit — как **salted HMAC** (соль случайна на процесс),
  живёт в памяти с TTL-вытеснением, не связан ни с одним сохранённым сигналом.
- В хранилище лежит только `FieldSignal` — в нём нет поля IP (тест
  `TestPrivacy_ContractHasNoIPField` это стережёт).

Из телеметрии нельзя восстановить, кто куда ходил: нет идентификатора юзера, нет
IP, гео грубое, время огрублено.

### 2. Устойчивость к отравлению (poisoning)
Вердикт считает **различные грубые источники** `SourceKey` (region+ASN → ISP →
region+platform), а не сырые сигналы. Один злоумышленник = один голос, сколько бы
сигналов он ни слал.

Три гейта в `detector.go` (`applyVerdict`), все должны сойтись, чтобы объявить
транспорт мёртвым:
1. `DistinctSources ≥ MinSources` (по умолч. 5) — флудер в одиночку не наберёт.
2. `BlockedShare ≥ DeadShareThresh` (0.70) **и** `BlockedSources ≥ MinBlockedSources`
   (4) — супербольшинство независимых источников И абсолютный порог.
3. `OKShare ≤ MaxOKShareForDead` (0.25) — пока живой трафик успешен, фейковые
   blocked не убьют рабочий транспорт (честные успехи контр-взвешивают).

Плюс:
- **Dedup по `signal_id`** (`dedup.go`) — реплей одного наблюдения не раздувает
  объём (TTL ≥ окна агрегации).
- **Rate-limit** (`ratelimit.go`) — token-bucket на источник + глобальный потолок.
- **Клэмп аномалий** на входе (out-of-range метрики/время отбрасываются).
- **Спайк-детект**: сравнение доли blocked во второй половине окна против первой
  (`SpikeDelta`, `SpikeMinSources`) — помечает всплеск, не полагаясь на один сигнал.

Тесты `TestPoisoning_*` покрывают: одиночный флуд (1000 сигналов с одного ASN не
убивают транспорт), слишком мало источников (<`MinSources` → вердикт не выносится),
контр-взвешивание живым трафиком (5 атакующих ASN против 20 честных OK → не мёртв).

**Предел модели (честно):** source-diversity ломает одиночного злоумышленника, но
не ботнет из ≥`MinSources` реальных ASN — тот всё ещё может продавить вердикт. Это
фундаментальное свойство, а не баг. Митигации следующего уровня (вне текущего
объёма): корреляция с авторитетными `HealthEvent` как обязательное подтверждение
перед автоматической ротацией, репутация ASN, аномалия-детект по историческому
базовому уровню.

### 3. Каждый blocked-вердикт → сигнал ротации
`Recommend` (`aggregate/recommend.go`) превращает вердикты в действия для
оркестратора (Волна 2):
- `mark_node_blocked {node_id, regions, confidence}` — из авторитетных
  `HealthEvent` и/или из полевых сигналов, прошедших poisoning-гейты.
- `prioritize_transport {region, recommended_transport, ranked[]}` — ранжирование
  транспортов по health-score (`1 − blocked_share − 0.5·degraded_share`, мёртвый = 0).

Тест `TestLoop_IngestToRecommendation` гоняет петлю целиком: 6 независимых
источников репортят dpi_block → `/v1/recommendations` отдаёт `mark_node_blocked`.

## Агрегация

`aggregate.Compute` — **чистая функция** над срезом сигналов в окне (по умолч. 15м),
детерминирована → воспроизводимые тесты. На каждый `(region, transport)`:
доли blocked/degraded/ok по источникам, `reset_rate`/`probe_rate` по сырым сигналам,
средние `rtt`/`loss`, тренд (вторая половина минус первая), спайк, вердикт
dead/degraded с человекочитаемым `reason`.

## Хранилище и ретеншн

Порт `store.Store` (репозиторий) с двумя реализациями:
- **`MemoryStore`** — дефолт, in-memory, кольцо с жёстким капом (защита от утечки
  памяти) + `Prune`. Всё покрыто тестами.
- **`PostgresStore`** — прод, на `database/sql`, **параметризованные** запросы (нет
  инъекций), батч-вставки в транзакции (нет полу-применённых батчей),
  `ON CONFLICT (signal_id) DO NOTHING` (dedup на уровне БД). DDL — `store/schema.sql`
  (индексы по `observed_at` и `(region, transport, time)`; для TimescaleDB —
  hypertable + `add_retention_policy`).

Ретеншн: горутина в `main` вызывает `Prune(now − retention)` каждые `PruneEvery`
(по умолч. 72ч / 10м).

### Включить Postgres в проде
Драйвер `github.com/jackc/pgx/v5/stdlib` **слинкован** (blank-import в
`cmd/telemetry/main.go`), рантайм-выбор бэкенда — по `DATABASE_URL`:
1. Задать `DATABASE_URL` (DSN Postgres) — сервис откроет пул, сделает readiness
   `PingContext` и переключится на `store.NewPostgresStore`. Пусто → in-memory.
2. **Схему/миграции применять вне старта приложения** (не в `main`, чтобы не было
   гонки миграций между инстансами — Production Checklist): прогнать
   `store/schema.sql` отдельным шагом/джобой. Старт делает только `Ping`.
3. `DATABASE_URL` уже приходит из env (`docker-compose.dev.yml`).

## Конфигурация (env)

| Переменная | Default | Назначение |
|------------|---------|------------|
| `PORT` | `8085` | порт HTTP |
| `TELEMETRY_INTERNAL_TOKEN` | — | bearer для internal-эндпоинтов (пусто = internal-эндпоинты отключены, 403) |
| `DATABASE_URL` | — | DSN Postgres; задан → durable-store, пусто → in-memory |
| `TELEMETRY_WINDOW` | `15m` | окно агрегации/вердиктов |
| `TELEMETRY_RETENTION` | `72h` | горизонт хранения |
| `TELEMETRY_PRUNE_EVERY` | `10m` | период ретеншн-цикла |
| `TELEMETRY_RATE_PER_SEC` / `_BURST` | `5` / `50` | rate-limit на источник |
| `TELEMETRY_GLOBAL_RATE` / `_BURST` | `2000` / `5000` | глобальный потолок |
| `TELEMETRY_MAX_BATCH` | `1000` | максимум сигналов в батче (иначе 413) |
| `TELEMETRY_MIN_SOURCES` | `5` | мин. различных источников для вердикта |
| `TELEMETRY_MIN_BLOCKED_SOURCES` | `4` | мин. blocked-источников |
| `TELEMETRY_DEAD_SHARE` | `0.70` | порог доли blocked для «мёртв» |
| `TELEMETRY_MAX_OK_SHARE` | `0.25` | потолок OK-доли при «мёртв» |
| `TELEMETRY_DEGRADED_SHARE` | `0.40` | порог доли impaired для «деградация» |
| `TELEMETRY_SPIKE_DELTA` / `_MIN_SOURCES` | `0.30` / `3` | детект всплеска |

## Метрики (OpenMetrics)

Счётчики: `telemetry_signals_ingested_total{transport,region,signal_type}`,
`telemetry_signals_rejected_total{reason}`, `telemetry_batches_rate_limited_total`,
`telemetry_health_events_total`. Гейджи из живых агрегатов:
`telemetry_blocked_share`, `telemetry_degraded_share`, `telemetry_ok_share`,
`telemetry_distinct_sources`, `telemetry_avg_rtt_ms`, `telemetry_avg_loss_pct`,
`telemetry_transport_dead`, `telemetry_transport_degraded`
(лейблы `region`, `transport`).

## Тесты

```bash
cd services/telemetry && go test ./...        # или: make test (весь монорепо)
go test -race ./...                            # гонки на конкурентных путях
```

- `aggregate`: доли/тренд/спайк/rtt/loss; симуляция «регион заблокировал reality»
  (`TestSimulation_RegionBlockedReality`); устойчивость к отравлению
  (`TestPoisoning_*`); рекомендации (`TestRecommend_*`).
- `ingest`: анонимизация, dedup, rate-limit, handler (202/400/413/429, отклонение
  неизвестных полей = PII-smuggle, dedup-подсчёт).
- `store`: write/since/prune/hard-cap.
- `api`: петля ingest→recommendation, auth-гейт, экспозиция `/metrics`,
  приватность контракта.
