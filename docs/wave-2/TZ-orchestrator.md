# ТЗ — Orchestrator (keystone Волны 2)

**Агент:** orchestrator. **Зона (владение):** только `services/orchestrator/`
(+ `docs/orchestrator.md`). Не трогать `packages/contracts` (заморожены) и чужие
`services/*`. Ветка: `agent/orchestrator`.

## Контекст

Оркестратор — недостающее звено, замыкающее петлю антихрупкости. Сейчас — заглушка
(`/healthz`). Вокруг уже готовы швы: telemetry отдаёт рекомендации, infra даёт
скрипты жизненного цикла ноды, control-plane хранит `Node`. Твоя задача — соединить
их в автоматический контур «блок → замена ноды → юзеры переехали».

Источники: [`architecture.md`](../../architecture.md) (Orchestrator, «Принципы
антихрупкости»), [`MASTER-REPORT.md`](../MASTER-REPORT.md) §4.D,
[`infra-status.md`](../infra-status.md) §«Интеграция с orchestrator»,
[`telemetry HANDOFF`](../../services/telemetry/HANDOFF.md) §«Интеграция Волны 2».

## Что построить (объём)

1. **Потребление рекомендаций telemetry.** Поллинг/подписка на telemetry-выдачу
   рекомендаций (`mark_node_blocked`, `prioritize_transport` с `confidence`).
   Форма контракта рекомендаций сейчас локальна в telemetry — согласовать с
   `TZ-contract-changes.md` (возможно вынос в `packages/contracts`). До выноса —
   потреблять по HTTP-форме telemetry, адаптер за интерфейсом.
2. **Политика решения.** Из рекомендации + порога `confidence` + (обязательно)
   подтверждающего `HealthEvent` от собственных проб — решение «ротировать/заменить
   ноду». Анти-poisoning: **не** авто-ротировать только по анонимным `FieldSignal`
   без авторитетного подтверждения (см. предел анти-poisoning в telemetry HANDOFF).
3. **Драйвер жизненного цикла ноды.** Вызов `infra/scripts/node_up.sh` /
   `node_rotate.sh` / `node_down.sh` за интерфейсом `Provisioner` (мокируемым в
   тестах). Учесть: `node_rotate` пока захардкожен под Hetzner — параметризация
   replace-target идёт в infra, здесь передать облако/адрес через переменные.
4. **Синхронизация с control-plane.** После up/rotate — записать/обновить `Node`
   (включая `entry_ip`, `transports[].vless_reality.public_key` целиком) через
   control-plane API. При ротации новый REALITY pubkey кладётся нодой в артефакт —
   прочитать и прописать в `Node.transports`. Пометить старую ноду `draining` →
   `retired`.
5. **Петля автозамены.** blocked-нода → provision замены → CP обновлён → старая
   `draining`/`retired`. Юзеры переезжают лениво через пересбор наборов CP
   (уже реализован). Обеспечить: замена появляется в активном наборе до retire старой.
6. **Собственные пробы из РФ (мин.).** Интерфейс `Prober` (заглушка ок для MVP,
   реальные пробы — операторские), результат → `HealthEvent` на telemetry `/v1/health`.
7. **Быстрая ротация по расписанию.** Уважать `Node.rotate_after` / эфемерные
   entry — плановая ротация, не только по блоку.

## Критерии приёмки (verify)

- `make build` / `make vet` / `make test` зелёные; gofmt чист; покрытие ≥80%.
- Тест-симуляция end-to-end на моках: `рекомендация blocked + HealthEvent → Provisioner.rotate вызван → CP.Node обновлён (новый ip+pubkey) → старая retired`.
- Тест: одиночный `FieldSignal`-шум **без** HealthEvent НЕ триггерит ротацию.
- Интерфейсы `Provisioner`/`Prober`/`ControlPlaneClient`/`TelemetryClient` —
  мокируемы; реальные адаптеры за ними.
- `docs/orchestrator.md`: контур, контракты стыков, env, что операторское.

## [АНТИ-БЛОК] (обязательно)

- Разнообразие: не схлопывать флот в один транспорт; `prioritize_transport` меняет
  приоритеты, не убивает семейства.
- entry≠exit при provision пары; уважать `role`/`entry_node_id`.
- Экономика асимметрии: замена ноды дешевле обнаружения — ротация быстрая, ноды одноразовые.
- Ноль хардкода доменов/IP — всё из CP/конфига/переменных.

## Вне объёма

Реальные облачные креды и apply (оператор), реальные пробы из РФ (оператор),
клиентский каскад переключения (Happ, ADR-003), сама провижн-механика (infra).
