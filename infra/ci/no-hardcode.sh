#!/usr/bin/env bash
# Guard the [АНТИ-БЛОК] "zero hardcode" rule: no mimicry domain and no node IP may
# be baked into Terraform modules or Ansible transport/firewall templates — they
# must arrive as variables. Fails CI (non-zero) if a literal is found.
#
# Scanned: infra/terraform/modules/** and infra/ansible/roles/**/templates/**.
# NOT scanned: *.example, docs/, inventory/ (operator config, not code).
set -euo pipefail

SELF="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "${SELF}/../.." && pwd)"
cd "${REPO}"

fail=0
ipv4='([0-9]{1,3}\.){3}[0-9]{1,3}'
# Allowed non-routable/loopback/wildcard literals (structural, not a node/mimicry).
allow_ip='0\.0\.0\.0|127\.0\.0\.1|255\.255\.255\.255'

files=()
while IFS= read -r _line; do
  files+=("${_line}")
done < <(find \
  infra/terraform/modules \
  infra/ansible/roles \
  -type f \( -name '*.tf' -o -name '*.tftpl' -o -path '*/templates/*' \) \
  ! -name '*.example' 2>/dev/null | sort)

for f in "${files[@]}"; do
  # Drop comment lines (#, //, ;, and jinja {# #}) before matching.
  body="$(grep -vE '^[[:space:]]*(#|//|;|\{#)' "${f}" || true)"

  # 1. Literal IPv4 (a hardcoded node IP).
  hits_ip="$(printf '%s\n' "${body}" | grep -nE "${ipv4}" | grep -vE "${allow_ip}" || true)"
  if [ -n "${hits_ip}" ]; then
    echo "HARDCODED IPv4 in ${f}:"; printf '%s\n' "${hits_ip}"; fail=1
  fi

  # 2. Literal http(s) URL (a hardcoded mimicry domain) — only in transport templates.
  case "${f}" in
    infra/ansible/roles/transports/templates/*)
      hits_url="$(printf '%s\n' "${body}" | grep -nE 'https?://[A-Za-z0-9.-]+' || true)"
      if [ -n "${hits_url}" ]; then
        echo "HARDCODED URL/domain in ${f}:"; printf '%s\n' "${hits_url}"; fail=1
      fi
      ;;
  esac
done

if [ "${fail}" -ne 0 ]; then
  echo "no-hardcode check FAILED — move the value to a variable." >&2
  exit 1
fi
echo "no-hardcode check passed (${#files[@]} files scanned)."
