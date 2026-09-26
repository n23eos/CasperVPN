#!/usr/bin/env bash
# Repeatable local acceptance. No cloud calls, real payments, or real Telegram.
set -euo pipefail
cd "$(dirname "$0")/.."
for tool in go docker python3 golangci-lint govulncheck openspec; do
  command -v "$tool" >/dev/null || { echo "Required tool missing: $tool" >&2; exit 1; }
done
go version | grep -q 'go1.27.1 ' || { echo 'Release checks require Go 1.27.1' >&2; exit 1; }
make build vet test lint LINT_STRICT=1
make infra-guards e2e-guards infra-nocode
source infra/versions.sh
for image in "ghcr.io/sagernet/sing-box:v${SINGBOX_VERSION}" caddy:2.11.4-alpine traefik/whoami:latest; do
  docker image inspect "$image" >/dev/null 2>&1 || docker pull "$image"
done
(cd services/subscription && go run ./cmd/gencheck config/routing.ru.json) |
  docker run --rm -i --network none "ghcr.io/sagernet/sing-box:v${SINGBOX_VERSION}" check -c /dev/stdin
make e2e-user-removal
python3 -m unittest discover -s test/launch -p 'test_*.py'
python3 test/e2e/onboarding.py --integration
for module in packages/contracts packages/platform services/control-plane services/subscription services/billing services/delivery services/telemetry services/orchestrator; do
  (cd "$module" && govulncheck ./...)
done
OPENSPEC_TELEMETRY=0 openspec validate launch-readiness --strict --no-interactive
git diff --check
echo 'PASS: local release gate. Live fleet, real provider and target-network acceptance remain separate.'
