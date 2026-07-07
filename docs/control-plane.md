# Control Plane

The brain of CasperVPN: the authoritative store and API for **Node**, **User**,
**Transport** and **Subscription**, the issuer of per-user isolation secrets, the
consumer of the telemetry feedback loop, and the engine that hands other services
a fresh, working config the moment a node is blocked.

Owns `services/control-plane/`. Go + Postgres. Implements the frozen
[`packages/contracts/openapi/control-plane.yaml`](../packages/contracts/openapi/control-plane.yaml)
plus a few additive internal endpoints (see below). It is the source of data for
Agents C (subscription), E (billing) and F.

## Architecture (clean / ports-and-adapters)

```
cmd/control-plane            wiring + graceful shutdown
internal/
  domain/                    entities-glue, ports (interfaces), sentinel errors
  usecase/                   business logic (no framework/DB imports)
    nodes / users / subscriptions / bundle / signals
  adapters/
    postgres/                pgx repositories (one store type per aggregate)
    httpapi/                 chi handlers, auth middleware, DTOs  (frozen OpenAPI)
    rebuild/                 async "issue a fresh config" queue
    memory/                  in-memory repos (contract tests, no-DB runs)
  authz/ config/ migrate/ secret/ seed/   infrastructure
```

Dependencies point inward: `usecase` depends only on `domain`; adapters depend on
`usecase`/`domain`; nothing in `domain`/`usecase` imports a database or HTTP.

## Data model

Migrations live in `internal/migrate/migrations/` and run at startup under a
Postgres **advisory lock** (two instances deploying at once cannot race).

| Table | Purpose |
|-------|---------|
| `users` | account + **personal** isolation secrets (`reality_short_id`, `vless_uuid`, `private_key`) |
| `subscriptions` | entitlement; **only the token hash** is stored — never the plaintext, no card/payment data |
| `nodes` | dynamic fleet registry (role, status, entry/exit split, rotation fields) |
| `transports` | many per node (diversity, not monoculture); variant params as JSONB |
| `node_rotation_history` | audit of register / rotate / retire / status transitions |
| `user_secret_rotations` | audit of per-user secret rotations |
| `subscription_sets` | built per-user Node×Transport set (source for Agent C + cache) |
| `node_signal_aggregates` | FieldSignal aggregates + derived verdict (feedback loop) |

`quota/used/traffic_limit` are stored as `BIGINT` (int64) and mapped to the
contract `uint64`; realistic byte counts fit comfortably.

## Endpoints

Frozen OpenAPI (implemented verbatim):

- `GET /healthz`
- `GET|POST /v1/nodes`, `GET|PATCH|DELETE /v1/nodes/{id}`
- `POST /v1/users`, `GET|PATCH /v1/users/{id}`, `POST /v1/users/{id}/rotate-secrets`
- `POST /v1/subscriptions`, `GET /v1/subscriptions/{id}`

Additive internal endpoints (backward-compatible new paths — allowed by the
contract-freeze rules):

- `POST /v1/nodes/{id}/rotate` — advertise a new ingress IP (fast entry rotation).
- `GET  /v1/users/{id}/subscription-set` — the structured Node×Transport set for
  Agent C. Returns the requesting user's secrets and the current **active** nodes.
  We do **not** render the subscription here — that is Agent C's job.
- `POST /v1/signals/aggregate` — telemetry pushes FieldSignal aggregates; the
  control plane marks nodes `degraded`/`blocked` and triggers rebuilds.

Subscription **create** returns the plaintext token exactly once (in
`Subscription.token`); **get** never exposes it.

## AuthN/Z

Every `/v1` route requires a service-to-service **bearer token**; `/healthz` is
public. Tokens map to roles via `CONTROL_PLANE_TOKENS` (env, never hardcoded).

| Role | May do |
|------|--------|
| `admin` | everything |
| `orchestrator` | node register / update / rotate / retire |
| `telemetry` | `POST /v1/signals/aggregate` |
| `subscription` | read accounts/subs + `GET .../subscription-set` |
| `billing` | create/update users & subscriptions |

Reads of the node registry are open to any authenticated service. Tokens are
compared in constant time.

## Anti-block flow (acceptance criteria)

1. **Dynamic list.** Nodes come only from the DB; no IP/domain/node is hardcoded.
   `BundleService.Build` assembles the set from `status = active` nodes.
2. **Block → automatic replacement.** A `blocked` mark (from a FieldSignal
   aggregate or a `PATCH`) records history and enqueues a rebuild. The `rebuild`
   worker invalidates the cache for every user holding that node and recomputes
   their set onto the remaining active fleet — no manual action.
3. **Per-user isolation.** A user's set carries only *their* `reality_short_id` /
   `uuid` / key. Blocking or rotating one user never exposes another.
4. **Feedback loop.** Aggregates are classified: `block_ratio ≥ 0.5 → blocked`,
   else `failure_ratio ≥ 0.3 → degraded`, else `healthy`. A block is never
   auto-cleared from a single healthy window (recovery is an orchestrator call).
5. **No payments here.** Billing is a separate service (Agent E).

## Running locally

```bash
make up          # postgres + services; control-plane seeds dev data (SEED=true)
curl -s localhost:8081/healthz
curl -s -H "Authorization: Bearer dev-admin-token" localhost:8081/v1/nodes | jq
```

Config (env): `PORT` (8081), `DATABASE_URL`, `ENV` (dev/prod),
`CONTROL_PLANE_TOKENS` (`token:role,...`; required outside dev), `SEED`,
`REBUILD_WORKERS`, `REBUILD_BUFFER`.

## Testing

```bash
go test ./...                     # unit (usecase) + contract (vs frozen OpenAPI)

# Integration against a real Postgres (build tag `integration`):
TEST_DATABASE_URL=postgres://caspervpn:dev_only_change_me@localhost:5432/caspervpn?sslmode=disable \
  go test -tags integration ./internal/adapters/postgres/...
```

- **Unit** — usecases against in-memory fakes (secret issuance, hashing, verdict
  thresholds, block→enqueue, isolation).
- **Contract** — loads the frozen OpenAPI and asserts every operation is routed
  and secured (401 without auth), plus RBAC and happy-path flows.
- **Integration** — the Postgres adapters end to end, including the
  block→async-rebuild acceptance path. Point `TEST_DATABASE_URL` at a disposable
  Postgres (docker-compose, a throwaway container, or testcontainers in CI).

> Note: integration tests use a plain `TEST_DATABASE_URL` rather than an embedded
> testcontainers dependency, because the current Go 1.20 toolchain floor predates
> the Go version required by recent `testcontainers-go` releases. CI can supply
> the DSN from a testcontainers-managed instance without changing the tests.
