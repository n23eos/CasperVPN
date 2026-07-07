# ТЗ — Прод-хардненинг (Production Checklist по всем сервисам)

**Агент:** hardening. **Зона:** все `services/*` (в своих модулях) + `infra/` +
`docker-compose`/CI по необходимости. Не трогать `packages/contracts`. Ветка:
`agent/hardening`. Делать **последним** перед прод-выкаткой.

## Контекст

Все подсистемы Волны 1 осознанно отложили прод-хардненинг (см.
[`MASTER-REPORT.md`](../MASTER-REPORT.md) §4.C и Production Checklist в
[`CLAUDE.md`](../../CLAUDE.md)). Задача — закрыть чеклист системно, единым стилем.

## Задачи (сгруппировано)

### Resilience
- **Rate limiting** на всех публичных/приёмных путях: `/sub/{token}` (subscription),
  `/v1/invoices` + вебхуки (billing), `/v1/signals` (telemetry — есть частично),
  `/d/{channel}/{token}` + `POST /v1/channels` (delivery). Token-bucket/leaky.
- **Timeouts на исходящие HTTP** (CP-клиенты, шлюзы, каналы) + **circuit breaker**
  на внешние вызовы.
- **Graceful shutdown** везде (у telemetry/billing есть — распространить).
- **Health/readiness**: `/healthz` (liveness, есть) + `/readyz` (проверка БД/зависимостей).

### Observability
- **Метрики** Prometheus/OpenMetrics по всем сервисам (у telemetry `/metrics` — есть,
  привести остальных к тому же): RPS, коды ответов, латентность, hit/miss кеша
  (subscription), лаг очереди пересбора (control-plane), доли каналов (delivery).
- **Structured logs** + отгрузка (не только stdout/локальный диск).
- **Alerting** на ключевые сбои: застрявшие `pending`-инвойсы старше TTL (billing),
  расхождение billing↔CP статуса, рост stale `subscription_sets`, падение инстанса.

### Architecture / State
- **Общий стейт (Redis)** для того, что сейчас in-memory single-instance и ломает
  горизонтальный масштаб: telemetry dedup/rate-limit, control-plane rebuild-queue
  (или pg LISTEN/NOTIFY), subscription/delivery кеши по необходимости.
- **Auth на админ-эндпоинтах**: `POST /v1/channels` (delivery) сейчас открыт —
  закрыть bearer/mTLS. Ротация service-токенов (control-plane — статичные из env).

### Security
- **Ревью секретов at-rest**: `subscription_sets.bundle` дублирует
  `reality_short_id`/`uuid`/`private_key` (control-plane п.2) — строить bundle на
  чтении или шифровать.
- **Anti-rollback на клиенте delivery**: проверка `Directory.Revision`/`IssuedAt`
  (max-age) при приёме артефакта.
- Проверить, что деплой не логирует секреты (env-токены).

### Infra
- **Свести версию sing-box к одной** (infra пинит `1.11.4`, subscription CI —
  `1.11.11`) — согласовать и зафиксировать в одном месте.
- Параметризовать `node_rotate.sh` replace-target по облаку (сейчас хардкод Hetzner).
- Guard пустого `reality_users` (node-up падает без ≥1 user).

## Критерии приёмки

- Пройтись по Production Checklist из `CLAUDE.md` — каждый пункт либо закрыт, либо
  явно помечен как «вне скоупа + почему».
- Нагрузочный smoke (базовый) на ключевые публичные эндпоинты — не падает под спайком.
- `make build`/`vet`/`test`/`lint` зелёные (в т.ч. `golangci-lint` — прогнать в CI,
  Волна 1 его не гоняла: не был установлен).
- Обновить `docs/*` и runbook по инцидентам (сейчас нет — Production Checklist).

## Вне объёма

Реальные алерт-каналы/дашборды-бэкенды (оператор поднимает Grafana/Prometheus),
нагрузочное тестирование в проде, DR-восстановление из бэкапа (оператор).
