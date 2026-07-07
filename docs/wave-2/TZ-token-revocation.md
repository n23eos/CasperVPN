# ТЗ — Отзыв subscription-токена (сшить revoke + инвалидация кэша)

**Приоритет:** HIGH (аудит B2). **Зона:** `services/control-plane`,
`services/subscription`. Часть слоя интеграции (`TZ-integration.md`).

## Проблема

Отзыв утёкшей subscription-ссылки **не работает на практике**:

- `subscription` отдаёт `/internal/revoke` и `/internal/tokens`
  (`internal/httpapi/internal.go`), но их **никто не вызывает** — ни control-plane,
  ни billing (grep по репо: нет вызовов).
- При бане юзера control-plane меняет статус, но не проталкивает отзыв в индекс
  токенов subscription. Бан ловится только в `resolve.go` при промахе кэша;
  **закэшированный ответ отдаётся до истечения TTL** (кэш при бане не
  инвалидируется).
- Индекс токенов в subscription — всегда in-memory (`memStore`, даже в prod),
  plaintext-ключами, теряется при рестарте (см. также `TZ-persistence.md`).

**Последствие:** утёкшую/скомпрометированную ссылку нельзя быстро отозвать;
забаненный юзер продолжает получать конфиг до TTL.

## Требуемый результат

Бан/ротация/отмена подписки в control-plane немедленно отзывает токен в
subscription и инвалидирует его кэш.

### 1. CP → subscription notifier

- Новый исходящий порт в control-plane: `SubscriptionRevoker` с
  `Revoke(ctx, tokenOrUserID)` + `Register(ctx, token, userID, subID)`.
- HTTP-адаптер бьёт в subscription `/internal/revoke` и `/internal/tokens`
  (bearer `INTERNAL_TOKEN`, constant-time на приёме — уже есть в `internal.go:47`).
- Конфиг: `SUBSCRIPTION_INTERNAL_URL` + токен. **Если не задан — no-op** (dev не
  ломается). Таймаут на исходящий HTTP + circuit breaker (см. `TZ-hardening.md`).

### 2. Хук в usecase

- Бан/suspend юзера (`users.go`), отмена/expire подписки, `RotateSecrets` →
  вызывают `SubscriptionRevoker.Revoke`.
- Выдача новой подписки (billing-активация / create) → `Register` токена.

### 3. Инвалидация кэша subscription

- `/internal/revoke` должен не только убрать токен из индекса, но и **выбросить
  закэшированный bundle** (`internal/cache`) — иначе бан не виден до TTL.
- Хранить в индексе **хеш** токена, не plaintext (согласовать с
  `control-plane` `secret.HashToken`; см. `TZ-persistence.md` для durable-индекса).

### 4. Персистентность индекса

Индекс токенов → durable (Postgres), по хешу. Иначе отзыв не переживает рестарт.
Координируется с `TZ-persistence.md`.

## Критерии приёмки

- Бан юзера → `/sub/{token}` отдаёт 403/404 **сразу**, в т.ч. до истечения TTL
  кэша (integration CP↔subscription).
- Отзыв конкретного токена не трогает другие токены того же юзера (если несколько
  устройств — по политике).
- Индекс переживает рестарт subscription; хранит хеш, не plaintext.
- `make build/vet/test -race` зелёные.
