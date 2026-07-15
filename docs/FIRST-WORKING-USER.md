# FIRST WORKING USER — сквозной сценарий первого живого пользователя

Единственный критерий, что CasperVPN стал продуктом, а не набором микросервисов:
**один реальный человек проходит путь от регистрации до работающего VPN на
реальной ноде**. Пока в таблице есть 🔴 — инфраструктурной полировкой
(метрики, circuit breaker, leader election, k8s) не занимаемся.

Статусы: 🟢 код готов и проверен · 🟡 код есть, живьём не прогонялось ·
🔴 отсутствует / заглушка.

| # | Шаг | Статус | Что есть / что делать |
|---|-----|:------:|----------------------|
| 1 | Прод-Postgres поднят, схемы применены | 🟡 | Процедура доказана локально (`test/e2e/first-user.sh`: control-plane мигрирует сам через advisory lock, schema.sql billing/subscription — две psql-команды), но продовая БД ещё не поднята. |
| 2 | Секреты и токены сервисов заданы | 🟡 | Все читаются из env, вне dev fail-closed. ⚠️ Исключение: telemetry без `TELEMETRY_INTERNAL_TOKEN` стартует открытой (только warning) — сделать fail-fast. |
| 3 | Куплен VPS: Hetzner API-токен + SSH-ключ | 🔴 | Руками оператора (OPERATOR-CHECKLIST §1). Разблокирует шаги 4–5. |
| 4 | `make node-up REGION=hel1 CLOUD=hetzner` — sing-box поднялся | 🔴 | Terraform+Ansible написаны; идемпотентная генерация REALITY-пары на ноде добавлена (`roles/transports/tasks/keygen.yml`), node_up.sh закрыл разрывы (node_role, mimicry, exit_endpoint, upsert реального pubkey). **Живого apply на VPS ещё не было** — роли проверены синтаксисом + infra CI (molecule). Сверить версию sing-box (infra 1.11.4 vs CI subscription 1.11.11). |
| 5 | Криптошов: реальная x25519-пара, клиент реально подключается | 🟢 | **Доказано локально** `make e2e-real-node` (opt-in, `REALITY_DEST`/`REALITY_SERVER_NAME`): пара генерится на ноде, CP/подписка несут только public key, оплаченный юзер allow-list'ится → sing-box-клиент проходит REALITY-хендшейк → HTTP 200 через туннель; ротация когерентна (новый pbk работает, старый исчез); приватник не доходит до клиента/API/логов. Merge-ротация покрыта регрессом `make e2e-sync-merge`. Модель доверия: приватник транзитит control-host (Ansible controller) — часть доверенной границы, но не наружные поверхности. Плейсхолдер `User.PrivateKey` к REALITY-пути не относится. |
| 5б | **entry→exit чейнинг не подключён** | 🔴 | `node_up.sh` регистрирует entry с клиентским REALITY, exit — как метаданные без клиентского транспорта (иначе subscription отдал бы outbound с пустым server). Реальный entry→exit data-plane (SS2022-inbound на exit с паролем/портом 8388 + outbound на entry) — отдельная непроверенная задача. Сейчас entry терминирует трафик сам (entry==exit egress), что нарушает принцип entry≠exit из architecture.md — закрыть до массового роста. |
| 5в | **CP→node reconciler allow-list юзеров отсутствует** | 🔴 | Синхронизация UUID/short-id юзера в серверный `reality_users` живёт ТОЛЬКО в `real-node.sh` (стенд-ин reconciler'а). Реальные `node_up.sh`/`node_rotate.sh` конверджат ноду с `reality_users:[]` → она не аутентифицирует ни одного подписчика. Поэтому нода регистрируется как **`provisioning`** (subscription отдаёт только `status='active'`), в клиентские бандлы не попадает. Нужен reusable reconciler: тянет subscription-set юзеров ноды из CP → пушит uuid/short-id на ноду → reload sing-box → флипает в `active`. **Без него ни одна нода не обслуживает юзеров.** |
| 5г | **Мульти-транспорт на ноде: пока монокультура** | 🔴 | `reality_sync` регистрирует один VLESS-REALITY транспорт; ansible defaults тоже включают только его. Правило репо [АНТИ-БЛОК] требует ≥2 транспорта одновременно. Reconciler (5в) обязан гейтить `active` на наличие ≥2 клиентских транспортов (например vless-reality + hysteria2). До этого нода остаётся `provisioning`. |
| 6 | Пользователь создан: `POST /v1/users` | 🟢 | Подтверждено e2e (`make e2e-first-user`). |
| 7 | Mock-оплата: `POST /v1/invoices` → вебхук mock-гейтвея | 🟢 | Подтверждено e2e, включая идемпотентность: повтор того же вебхука не удлиняет подписку. |
| 8 | Billing активирует подписку: `PATCH /v1/subscriptions/{id}` | 🟢 | Подтверждено e2e против живых CP+Postgres (блокер A.1 из MASTER-REPORT закрыт). |
| 9 | `GET /sub/{token}` отдаёт конфиг (base64 / sing-box / clash) | 🟢 | Подтверждено e2e: персональный uuid в vless, ≥2 реальных транспорта (vless+hysteria2), служебные direct/urltest/selector не считаются. |
| 9а | **Выдача ссылки пользователю — продуктово не решена** | 🔴 | Плейнтекст-токен CP отдаёт один раз при create/rotate; billing его отбрасывает (`activator.go:87-91`), «получи мою ссылку»-эндпоинта нет ни у кого. E2e обходит через admin `rotate-token`, но каждая ротация отзывает старую ссылку (второе устройство/старый импорт умирают). Нужно решение: где живёт выдача ссылки (бот через rotate? отдельный endpoint?). |
| 10 | Telegram-бот: юзер получает ссылку через `/get` | 🔴 | Бот — библиотека без розетки: `HandleUpdate` нигде не вызывается (нет long-poll/webhook цикла), нет реализации `SubscriptionProvider` (клиента к subscription), нет регистрации/оплаты в боте. Самый большой кусок работы Этапа 1. |
| 11 | Happ импортирует ссылку (deep-link) | 🟡 | Рендер Happ deep-link + заголовки готовы; проверка — руками на телефоне. |
| 12 | VPN работает: HTTP через туннель | 🟡 | **Локально доказано** (`make e2e-real-node`: HTTP 200 через REALITY-туннель на реальную x25519-пару). Остаётся на живой VPS: `node-up` apply + импорт в Happ на телефоне. |
| 13 | Нода шлёт health в telemetry (простой POST) | 🟡 | Ansible-роль `health_metrics` есть, telemetry ingest есть. Достаточно ping'а — без FieldSignal-аналитики на этом этапе. |

## Порядок работ (2 недели)

1. ✅ **Локальный e2e без ноды** (шаги 1–2, 6–9): `make e2e-first-user` —
   изолированный стек (`-p caspervpn-e2e`, альтернативные порты), user →
   mock-инвойс → HMAC-вебхук → активация через живой CP → `/sub/{token}` →
   валидный sing-box. Зелёный с чистой БД, воспроизводимо.
2. **Опасные дефекты данных** (параллельно, всё локальное):
   - транзакция вокруг создания подписки (`usecase/subscriptions.go:80-95`);
   - fail-fast без `DATABASE_URL` в billing/subscription/telemetry;
   - обязательный `TELEMETRY_INTERNAL_TOKEN` вне dev;
   - `SetMaxOpenConns` в subscription/telemetry;
   - эвикция в subscription-кеше (`cache/cache.go`).
3. **Реальная нода** (шаги 3–5, 12): Hetzner → `make node-up` → реальная
   деривация ключей CP↔нода → подключение клиентом → YouTube.
4. **Бот** (шаг 10): poll-цикл + `SubscriptionProvider` + минимальная
   регистрация (`/start` создаёт юзера через billing/CP, `/pay` — mock).
5. **Закрытая бета 10–20 человек.**

## Чего НЕ делаем, пока есть 🔴

Метрики/Prometheus/Grafana, circuit breaker, retry-обвязка, leader election
(оркестратор просто запускаем в одном экземпляре), Kubernetes, multi-region,
полная FieldSignal-аналитика telemetry, web/admin. Всё это — после первых
живых пользователей.
