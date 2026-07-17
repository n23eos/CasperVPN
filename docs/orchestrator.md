# Orchestrator — замыкание петли антихрупкости

Оркестратор — keystone Волны 2 ([`docs/wave-2/TZ-orchestrator.md`](./wave-2/TZ-orchestrator.md)).
Он соединяет уже готовые швы флота в автоматический контур
**«блок → замена ноды → юзеры переехали»**: потребляет рекомендации telemetry,
подтверждает подозрения собственными пробами, принимает решение через
policy-движок (с анти-poisoning гейтом) и приводит его в исполнение через
infra-скрипты жизненного цикла ноды и control-plane.

Зона владения — только `services/orchestrator/` (+ этот файл). `packages/contracts`
заморожены; чужие `services/*` не редактируются.

## Главный принцип безопасности: сначала мозг, потом руки

Риск №1 оркестратора — стать автоматом, который ротирует парк по **отравленной
телеметрии**. Поэтому:

1. **`DRY_RUN=true` по умолчанию.** Из коробки оркестратор только считает план и
   логирует его — не трогает ни инфру, ни control-plane. Чтобы он начал
   действовать, оператор должен явно выставить `DRY_RUN=false` и задать URL/токены.
2. **Анти-poisoning гейт.** Одиночная анонимная полевая жалоба (`FieldSignal`)
   **никогда** не триггерит замену. Необратимое действие (rotate/replace/retire)
   разрешено только когда вердикт `authoritative` (получен из авторитетного
   `HealthEvent` собственной пробы telemetry) **или** `corroborated` подтверждён
   свежей собственной пробой оркестратора. Всё, что слабее — максимум помечает
   ноду `degraded`.
3. **Потолок side-effects на цикл** (`MAX_ACTIONS_PER_CYCLE`, дефолт 1). Даже
   полностью отравленный вход сожжёт не больше N нод за один проход — парк не
   «сваливается» лавиной. Это прямое следствие правила [АНТИ-БЛОК] о разнообразии.

## Контур (один reconcile-цикл)

```
        ┌──────────────┐   GET /v1/recommendations   ┌───────────────┐
        │  telemetry   │◀────────────────────────────│               │
        │              │──── POST /v1/health ────────▶│ orchestrator  │
        └──────────────┘   (вердикты своих проб)      │  reconcile    │
        ┌──────────────┐   GET/PATCH /v1/nodes        │  loop         │
        │ control-plane│◀────────────────────────────▶│               │
        └──────────────┘                              └──────┬────────┘
        ┌──────────────┐   node_up/rotate/down.sh            │
        │ infra/scripts│◀───────────────────────────────────┘
        └──────────────┘
```

Один цикл (`internal/reconcile`):

1. **Observe.** Состояние флота из control-plane (`ListNodes`) — жёсткая
   зависимость. Рекомендации telemetry — best-effort: при их отсутствии плановая
   ротация и retire по grace всё равно идут.
2. **Confirm.** Для нод, на которые жалуется telemetry, гоняются собственные пробы
   (`Prober`); их авторитетные вердикты (а) служат вторым фактором анти-poisoning
   гейта и (б) отправляются обратно в telemetry (`POST /v1/health`) — петля
   обратной связи замыкается. В `DRY_RUN=true` пробы (read-only наблюдение) всё
   равно идут, чтобы план был реалистичным, но вердикты **не** отправляются в
   telemetry — отправка влияет на будущие рекомендации и считается side-effect.
3. **Plan** (чистая функция `internal/policy`, без side-effects). Из рекомендаций +
   состояния + порогов + проб — план действий: `noop`, `mark_degraded`, `rotate`,
   `replace`, `retire`.
4. **Act** (только при `DRY_RUN=false`). План исполняется по порядку; при первой
   ошибке цикл останавливается — полу-выполненный план не каскадит.

## Решения policy-движка

| Ситуация | Действие |
|----------|----------|
| Рекомендация для неизвестной/уже draining/retired ноды | `noop` |
| Рекомендации устарели (> `RECOMMENDATION_MAX_AGE`) | `noop` |
| Блок `corroborated` без подтверждающей свежей пробы | `mark_degraded` (гейт) |
| Блок `corroborated`, но своя проба говорит `healthy` | `mark_degraded` (вето пробы) |
| Блок `authoritative` **или** `corroborated`+свежая проба `blocked`, нода с эфемерным entry | `rotate` |
| То же, нода со статичным entry | `replace` |
| `Node.rotate_after` прошёл | `rotate` (плановая ротация) |
| Нода `draining` дольше `DRAIN_GRACE` | `retire` |
| Нода `draining` без валидной метки времени | `noop` + сигнал оператору |

`rotate` vs `replace`: ротация меняет только эфемерный entry-IP и перевыпускает
REALITY — дёшево; replace нужен, когда entry не эфемерный. Экономика асимметрии:
регенерация дешевле обнаружения, ноды одноразовые.

## Автозамена: порядок операций

Для `replace` порядок намеренно консервативный, чтобы юзеры не остались без выхода:

1. `node_up.sh` поднимает **замену** и регистрирует её в control-plane —
   **до** того, как старая нода начнёт draining.
2. Старая нода переводится в `draining` с меткой
   `orchestrator.drain_started_at` (RFC 3339). Новая и старая **сосуществуют**;
   юзеры переезжают лениво через пересбор наборов в control-plane (уже реализовано).
3. Через `DRAIN_GRACE` следующий цикл видит истёкшую метку и зовёт `node_down.sh`
   (drain → retire записи Node → terraform destroy).

При падении `node_up.sh` старая нода не тронута — откатывать нечего.

Для `rotate` control-plane обновляет сам `node_rotate.sh` (новый `entry_ip` +
пропагация REALITY pubkey/short_id в `transports[].vless_reality`, аддитивно к
пулу short-id — per-user изоляция не схлопывается). Оркестратор после ротации
**проверяет** результат через `GetNode` и логирует предупреждение, если IP не
изменился.

## Контракты стыков

- **telemetry `GET /v1/recommendations`** (bearer) → `contracts.Recommendations`
  (`node_blocks[]` с `confidence`, `region_priorities[]`). Форма уже
  канонизирована в `packages/contracts/recommendation.go`.
- **telemetry `POST /v1/health`** (bearer) ← `contracts.HealthEvent`.
- **control-plane `GET/PATCH /v1/nodes`, `GET /v1/nodes/{id}`** (роль
  `orchestrator`) — чтение/запись `contracts.Node`. PATCH несёт полный объект
  (control-plane заменяет его целиком) — идемпотентно.
- **infra `node_up.sh` / `node_rotate.sh` / `node_down.sh`** — драйвер за
  интерфейсом `Provisioner`, параметры через env (`REGION`/`CLOUD`/`NODE`).

## Интерфейсы (все мокируемы)

`internal/ports` определяет `TelemetryClient`, `ControlPlaneClient`,
`Provisioner`, `Prober`, `Clock`. Реальные адаптеры — за ними:
`telemetryclient`, `cpclient` (HTTP: bearer, таймауты, конечный retry для
идемпотентных запросов), `provision.ScriptRunner` (скрипты с timeout и
структурированным результатом), `probe.TCPProber` (MVP-пробер).

## Конфигурация (env)

| Переменная | Дефолт | Назначение |
|------------|--------|------------|
| `DRY_RUN` | `true` | **безопасный дефолт**; `false` разрешает side-effects |
| `PORT` | `8086` | локальный HTTP (`/healthz`) |
| `RECONCILE_INTERVAL` | `1m` | пауза между циклами |
| `TELEMETRY_URL` / `TELEMETRY_TOKEN` | — | шов telemetry (обязателен при `DRY_RUN=false`) |
| `CONTROL_PLANE_URL` / `CONTROL_PLANE_TOKEN` | — | шов control-plane (обязателен при `DRY_RUN=false`) |
| `SCRIPTS_DIR` | `infra/scripts` | каталог со скриптами жизненного цикла |
| `SCRIPT_TIMEOUT` | `10m` | таймаут одного вызова скрипта |
| `RECOMMENDATION_MAX_AGE` | `15m` | старше — рекомендация не триггерит действий |
| `PROBE_MAX_AGE` | `10m` | старше — своя проба не подтверждает `corroborated` |
| `DRAIN_GRACE` | `30m` | сосуществование старой и новой ноды до retire |
| `MAX_ACTIONS_PER_CYCLE` | `1` | потолок side-effects на цикл |
| `PROBE_ENABLED` | `false` | включить подтверждающие пробы |
| `PROBE_SOURCE` | `orchestrator-local` | метка источника в `HealthEvent` |
| `PROBE_TIMEOUT` | `10s` | таймаут одной пробы |
| `DEFAULT_REGION` / `DEFAULT_CLOUD` | — | placement замены, если у ноды его нет |

Секреты — только из env/secret-manager; ни одного домена/IP в коде (правило
[АНТИ-БЛОК] №5).

## Failure modes

- **telemetry недоступна** — цикл продолжается без рекомендаций (плановая ротация
  и retire идут); ошибка логируется.
- **control-plane недоступен** — цикл прерывается (нет состояния флота — нет
  безопасных действий).
- **скрипт упал/таймаут** — структурированная ошибка (`exit code`, хвост вывода);
  цикл останавливается, никакого каскада.
- **ротация не сменила IP в CP** — предупреждение в лог (проверь `CONTROL_PLANE_URL`
  на стороне скрипта); авто-повтора нет — операторское внимание.
- **draining без валидной метки времени** — `noop`, оператору решать.

## Границы ответственности (операторское / вне объёма)

- Реальные облачные креды и `apply` инфраструктуры — оператор
  ([`OPERATOR-CHECKLIST.md`](./OPERATOR-CHECKLIST.md)).
- Реальные пробы из РФ — операторские vantage-точки; `probe.TCPProber` это MVP,
  проверяющий только достижимость с места, где запущен оркестратор (не точка
  цензуры). Интерфейс `Prober` позволяет заменить его без правки цикла.
- Клиентский каскад переключения (Happ, [ADR-003](./decisions/ADR-003-external-clients-happ.md)) — вне объёма.
- Сама механика провижна (Terraform/Ansible) — infra.

## Тесты и покрытие

`policy` покрыт исчерпывающе, включая ядро безопасности: «одиночный/некорро-
борированный `FieldSignal` без подтверждающей пробы НЕ ротирует», вето пробы,
устаревшее окно, неизвестная нода, потолок side-effects. `reconcile` — e2e на
моках: dry-run не трогает инфру; `authoritative` блок → rotate → CP обновлён;
replace ставит замену раньше draining; retire после grace; ротация переживает
падение telemetry. `httpx`/адаптеры — httptest на 200/401/500/malformed/timeout/
идемпотентный retry. `provision` — временные скрипты (env/args/exit/timeout).

> **Статус проверки 2026-07-11:** локально пройдены `make build`, `make vet`,
> `make test` (race, как задано в Makefile) на `go1.20.3 darwin/arm64`.
