# CasperVPN

CasperVPN is a Go backend for a subscription VPN service. A private Telegram
bot handles onboarding, payment links and a persistent subscription URL.
The supported launch profile gives each user personal VLESS REALITY and
Hysteria2 credentials. Entry and exit nodes are separate; Shadowsocks-2022
connects them internally. Clients are external apps, with no custom app shipped.

**Launch preparation:** [operator runbook](docs/LAUNCH.md),
[verification and limits](docs/LAUNCH-VERIFICATION.md),
[OpenSpec change](openspec/changes/launch-readiness/).
The local acceptance path is implemented. A controlled live pilot is required
before public paid use; resistance to particular networks or DPI systems has
not been established by local tests.

[Русская версия](README.ru.md) | [Website](https://n23eos.github.io/CasperVPN/)

## Supported launch behavior

- Personal VLESS and Hysteria2 access, including expiry, grace and revocation.
- Stable subscription links after restart, renewal and database restoration.
- Durable invoice credit and versioned billing delivery to prevent repeated
  webhooks, retries and delayed responses from granting duplicate periods.
- Private Telegram `/start`, `/pay`, `/get` with durable update deduplication.
- Access snapshots refreshed every two minutes, with a node watchdog that
  stops access when a lease expires (at most five minutes).
- Fleet actions rely on authenticated infrastructure checks. Public client
  reports are advisory and cannot independently authorize node actions.

Generated sing-box JSON and node configs are tested with **sing-box 1.14.2**.
This JSON does not support the old 1.11.11 DNS schema. Base64 and Clash remain
available; verify the actual client app before the pilot. AmneziaWG is not
part of the supported public launch profile. Plan metadata does not enforce
traffic, speed or device quotas.

## Services

Eight Go workspace modules: contracts, platform and six services.

| Service | Dev port | Role |
|---------|----------|------|
| control-plane | 8081 | Accounts, entitlement, credentials, fleet and guarded activation |
| subscription | 8082 | Authorized profile rendering and subscription links |
| delivery | 8083 | Telegram onboarding and subscription delivery |
| billing | 8084 | BTCPay invoices, durable credit and expiry |
| telemetry | 8085 | Client observations and authenticated health events |
| orchestrator | 8086 | Terraform/Ansible node lifecycle and replacement |

The orchestrator needs a Linux host with Terraform/Ansible and persistent
manifests. Its HTTP Docker image alone cannot execute cloud operations.
Contracts are changed additively in Go, JSON Schema and OpenAPI together.
Historical architecture and decisions are in [architecture.md](architecture.md)
and [docs/decisions](docs/decisions/); use the launch runbook for current operation.

## Build and verify

Use **Go 1.27.1**. The full gate also needs Docker, Compose, Python 3,
golangci-lint 2.14.0, govulncheck 1.8.0, OpenSpec 1.13.0, jq and openssl.

```sh
make build
make vet
make test             # All eight modules, race detector
make lint LINT_STRICT=1
make release-check    # Local integration, real transports, restore, security checks
```

The release gate uses local test providers. It creates no cloud resources and
sends no real Telegram messages or payments. See the runbook for configuration,
private startup, fleet acceptance, publication, backup and rollback.
Development helpers `make up` and `make down` use `docker-compose.dev.yml`.

## Security and license

Secrets belong in environment files or a secret manager. Generated launch
settings, backups and private `!notes` are ignored by Git. Development
credentials are for local use only. The public edge exposes subscription
retrieval and the exact BTCPay webhook; internal APIs stay private.

Dual-licensed under [AGPLv3](LICENSE) and a [commercial license](COMMERCIAL.md).
See those files for terms. Repository working instructions: [CLAUDE.md](CLAUDE.md).
