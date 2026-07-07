# Subscription service — статус (агент `agent/subscription`)

Дата: 2026-07-07. Ветка: `agent/subscription` (clone `casper-sub`).
Зона: только `services/subscription/` (+ `docs/subscription.md`, `.github/workflows/subscription.yml`).

## Стадия: функционально завершена ✅

Задача из промпта закрыта целиком. `make build` / `make vet` / `make test` зелёные,
gofmt чист, fuzz 1.9M прогонов без падений, end-to-end смоук проходит. Контракты не
тронуты, чужие сервисы не тронуты. **Не закоммичено** (пользователь не просил).

## Сделано (по пунктам задачи)

- [x] `GET /sub/{token}` — три представления одного набора; выбор формата
      query→Accept→User-Agent→base64. Плюс `/sub/{token}/nodes`, `/sub/{token}/happ`, `/healthz`.
- [x] Happ: deep-link `happ://add/<base64(url)>` из адреса запроса; заголовки
      `Profile-Update-Interval`, `Subscription-Userinfo`, `Profile-Title`, `Announce`,
      `Support-Url`, `Profile-Web-Page-Url`, `Routing-Enable`. Сверено по dev-docs Happ.
- [x] Персонализация: `uuid`/`reality_short_id` в каждом прокси; фильтр plan/region/platform;
      выброс blocked; инвариант диверсити (≥2 транспорта, не даём фильтрам схлопнуть).
- [x] Кеш выдачи (TTL) + инвалидация по сигналу control-plane (`POST /internal/invalidate`,
      нода blocked → bump версии → пересбор); `POST /internal/tokens`/`/revoke` (bearer).
- [x] Split-tunnel: RU → мимо туннеля, остальное → VPN; в sing-box (`route`+`dns`+
      `urltest`/`selector`) и Clash (`proxy-groups`+`rules`+`rule-providers`). Домены/резолверы —
      из `config/routing.ru.json`, ноль хардкода в коде.
- [x] [АНТИ-БЛОК]: много транспортов; URL не привязан к одному домену; авто-обновление
      профиля; per-user изоляция; ноль хардкода.
- [x] Тесты: golden ×3, `sing-box check` в CI, fuzz токенов, инвариант, инвалидация.
- [x] `docs/subscription.md` с примерами ссылок под Happ.

## Что НЕ сделал (осознанно, вне моей зоны / нужен другой агент)

1. **Реальная интеграция с control-plane.** HTTP-адаптер (`internal/controlplane/http.go`)
   написан на замороженные `/v1/users|subscriptions|nodes`, но control-plane их ещё не
   реализовал → сквозной прогон против живого CP не гонялся. Проверено только через
   in-memory адаптер. **Отследить:** когда `casper-control` поднимет endpoint’ы — прогнать
   integration против него.
2. **Постоянное хранилище TokenIndex.** Сейчас in-memory (`controlplane.Memory`). В проде
   token→(user,sub) должен пережить рестарт → нужен Postgres-бэкенд, реализующий
   `controlplane.TokenIndex`. Схема/миграции — TODO (координация с billing, кто наполняет
   индекс через `/internal/tokens`).
3. **AmneziaWG в `sing-box check`.** Mainline sing-box его не парсит → CI-check гоняется на
   mainline-наборе (reality+hy2+ss). Полный AmneziaWG-конфиг проверен только структурно
   (Go-тест). **Отследить:** если CI перейдёт на AmneziaWG-совместимый core — включить
   amnezia-ноду в `cmd/gencheck`.
4. **Петля обратной связи (FieldSignal/HealthEvent).** [АНТИ-БЛОК] п.4 требует
   потреблять/писать сигналы. Приём сигналов — зона `telemetry`; здесь достаточно
   инвалидации по сигналу «нода blocked». Автоматическую пере-приоритизацию транспортов по
   `FieldSignal` (учить каскад) НЕ делал — это будущая фича, зависит от telemetry-API.
5. **Delivery-каналы (Агент D).** Выдача переносима и не привязана к домену, но сам роутинг
   по каналам (DoH/Telegram/GitHub raw/DNS TXT) — зона `casper-deliver`. Мой сервис только
   отдаёт payload; `/d/{channel}/{token}` живёт у delivery.
6. **Пиннинг версии sing-box в CI** зафиксирован как `1.11.11` (env `SINGBOX_VERSION`).
   **Отследить:** если релиза с таким тегом нет — бампнуть на существующий 1.11.x; при
   переходе на 1.12 схема `dns` меняется (legacy `address`/`detour` → новый формат) —
   `config/routing.ru.json` придётся мигрировать.

## Что можно отследить дальше (наблюдаемость / прод-готовность)

- **Rate limiting** на `/sub/{token}` (один бот из 500 юзеров). Не заложено — TODO перед
  прод-выкаткой.
- **Метрики/алерты:** hit/miss кеша, коды ответов, латентность control-plane. Нет —
  зависит от общего стека наблюдаемости.
- **Инвалидация точечная по ноде:** сейчас node-blocked → bump всей версии (пересбор всех
  подписок). Если нод/юзеров много — добавить reverse-index token→nodeIDs для точечного
  purge. Пока проще и корректно.
- **Секреты:** `INTERNAL_TOKEN`/`CONTROL_PLANE_TOKEN` только из env (ок). Проверить, что
  деплой их не логирует.

## Точки входа в код

```
cmd/subscription/main.go        wiring + graceful shutdown
cmd/gencheck/                   генератор конфига для sing-box check
internal/config/                env + RoutingPolicy (routing.ru.json)
internal/controlplane/          порт Provider/TokenIndex + HTTP + memory
internal/resolve/               token → bundle (валидация 401/404/410)
internal/personalize/           фильтры + инвариант диверсити
internal/render/                негоциация + обёртка рендереров + routing + happ
internal/cache/                 кеш + инвалидация
internal/httpapi/               хендлеры
config/routing.ru.json          split-tunnel policy (данные, не код)
config/dev-fixtures.json        dev-сид (не прод)
```
