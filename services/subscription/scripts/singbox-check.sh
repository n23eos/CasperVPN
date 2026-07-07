#!/usr/bin/env bash
# Validate that the subscription service's sing-box output actually parses, by
# running the real `sing-box check` against a rendered config.
#
# The fixture uses the mainline-supported transports (VLESS-REALITY, Hysteria2,
# Shadowsocks-2022) plus the full split-tunnel routing wrapper. AmneziaWG is
# excluded here because mainline sing-box does not implement it (nodes/clients
# run an AmneziaWG-capable core); its presence in the full output is covered by
# the Go structural tests. See docs/subscription.md.
set -euo pipefail

cd "$(dirname "$0")/.."

if ! command -v sing-box >/dev/null 2>&1; then
  echo "singbox-check: sing-box binary not found on PATH" >&2
  exit 127
fi

out="$(mktemp -t singbox-check-XXXXXX).json"
trap 'rm -f "$out"' EXIT

go run ./cmd/gencheck config/routing.ru.json >"$out"
echo "singbox-check: rendered config -> $out"
sing-box check -c "$out"
echo "singbox-check: OK ($(sing-box version | head -n1))"
