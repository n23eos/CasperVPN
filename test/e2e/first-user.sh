#!/usr/bin/env bash
# Full self-service path through a local Telegram API fixture and real PostgreSQL.
# Only the randomly named test container and local child processes are cleaned up.
set -euo pipefail
cd "$(dirname "$0")/../.."
make build
exec python3 test/e2e/onboarding.py
