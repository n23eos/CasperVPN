#!/usr/bin/env bash
# Guard the [АНТИ-БЛОК] "zero hardcode" rule: no mimicry domain and no node IP may
# be baked into Terraform modules or Ansible transport/firewall templates — they
# must arrive as variables. Fails CI (non-zero) if a literal is found.
#
# Scanned:
#   - infra/terraform/modules/**  and  infra/ansible/roles/**/templates/**
#       * literal IPv4 (a hardcoded node IP)              -> fail
#       * literal http(s) URL in transport templates      -> fail
#       * literal hostname in a mimicry FIELD (server_name/handshake_server/
#         sni/masquerade/...) — these must be jinja {{ ... }}, a bare hostname
#         (matches a TLD) is a baked mimicry target        -> fail
#   - services/subscription/config/*.json  (dev fixtures + policy)
#       * IPv4 outside the documentation ranges (RFC5737 TEST-NET) or the tiny
#         allowlist of public policy resolvers             -> fail
#       * hostname outside the documentation TLDs (.test/.example/.invalid/
#         .localhost) or the explicit infra allowlist       -> fail
# NOT scanned: *.example, docs/, inventory/ (operator config, not code).
#
# Self-test:  infra/ci/no-hardcode.sh --self-test
set -euo pipefail

SELF="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "${SELF}/../.." && pwd)"
cd "${REPO}"

fail=0
ipv4='([0-9]{1,3}\.){3}[0-9]{1,3}'
# Allowed non-routable/loopback/wildcard literals (structural, not a node/mimicry).
allow_ip='0\.0\.0\.0|127\.0\.0\.1|255\.255\.255\.255'

# Mimicry-carrying JSON keys: their value MUST be a jinja {{ ... }} expression.
# A literal hostname here (the REALITY server_name, handshake target, hysteria2
# masquerade, etc.) is the exact thing an active prober would fingerprint.
mimicry_keys='server_name|server_names|server|handshake_server|sni|tls_sni|masquerade|masquerade_url'
# A literal hostname: one+ labels ending in an alphabetic TLD (>=2 chars).
host_tld='[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}'

# strip_comments <file> — drop comment lines (#, //, ;, jinja {#) before matching.
strip_comments() { grep -vE '^[[:space:]]*(#|//|;|\{#)' "$1" || true; }

# scan_template_domains <body-text> — echo "N:line" for every mimicry-key line
# whose value is a LITERAL hostname (jinja {{ ... }} expansions are stripped
# first, so a properly templated value leaves nothing to match).
scan_template_domains() {
  printf '%s\n' "$1" \
    | grep -nE "\"(${mimicry_keys})\"[[:space:]]*:" \
    | sed -E 's/\{\{[^}]*\}\}//g' \
    | grep -E "${host_tld}" || true
}

# ---------------------------------------------------------------------------
# Self-test: prove the mimicry-domain check catches a literal and passes jinja.
# ---------------------------------------------------------------------------
if [ "${1:-}" = "--self-test" ]; then
  bad='        "server_name": "www.microsoft.com",'
  good='        "server_name": "{{ transports.vless_reality.server_names[0] }}",'
  if [ -z "$(scan_template_domains "${bad}")" ]; then
    echo "SELF-TEST FAIL: literal mimicry domain was NOT caught" >&2; exit 2
  fi
  if [ -n "$(scan_template_domains "${good}")" ]; then
    echo "SELF-TEST FAIL: templated {{ }} value was wrongly flagged" >&2; exit 2
  fi
  echo "self-test passed."
  exit 0
fi

# ---------------------------------------------------------------------------
# 1. Terraform modules + Ansible role templates.
# ---------------------------------------------------------------------------
files=()
while IFS= read -r _line; do
  files+=("${_line}")
done < <(find \
  infra/terraform/modules \
  infra/ansible/roles \
  -type f \( -name '*.tf' -o -name '*.tftpl' -o -path '*/templates/*' \) \
  ! -name '*.example' 2>/dev/null | sort)

for f in "${files[@]}"; do
  body="$(strip_comments "${f}")"

  # 1a. Literal IPv4 (a hardcoded node IP).
  hits_ip="$(printf '%s\n' "${body}" | grep -nE "${ipv4}" | grep -vE "${allow_ip}" || true)"
  if [ -n "${hits_ip}" ]; then
    echo "HARDCODED IPv4 in ${f}:"; printf '%s\n' "${hits_ip}"; fail=1
  fi

  # 1b + 1c. Mimicry literals — only in transport templates.
  case "${f}" in
    infra/ansible/roles/transports/templates/*)
      # Literal http(s) URL (a hardcoded mimicry domain).
      hits_url="$(printf '%s\n' "${body}" | grep -nE 'https?://[A-Za-z0-9.-]+' || true)"
      if [ -n "${hits_url}" ]; then
        echo "HARDCODED URL/domain in ${f}:"; printf '%s\n' "${hits_url}"; fail=1
      fi
      # Bare hostname in a mimicry field (must be jinja {{ ... }}).
      hits_dom="$(scan_template_domains "${body}")"
      if [ -n "${hits_dom}" ]; then
        echo "HARDCODED mimicry hostname in ${f} (use a {{ variable }}):"
        printf '%s\n' "${hits_dom}"; fail=1
      fi
      ;;
  esac
done

# ---------------------------------------------------------------------------
# 2. Subscription config/fixtures — only documentation values may be committed.
# ---------------------------------------------------------------------------
# Documentation IP ranges (RFC5737 TEST-NET-1/2/3) + structural + the tiny set of
# public resolvers the RU split-tunnel policy legitimately points at.
allow_ip_sub="${allow_ip}|192\.0\.2\.|198\.51\.100\.|203\.0\.113\.|1\.1\.1\.1|77\.88\.8\.8"
# Real public endpoints the policy config references (rule-set mirror, probe).
allow_host='raw\.githubusercontent\.com|www\.gstatic\.com'
# Documentation/test TLDs (RFC2606 + reserved) — safe placeholder domains.
doc_tld='\.(test|example|invalid|localhost)$'

# extract_hosts <body-text> — echo one candidate hostname per line, from URL
# authorities and from mimicry-key / dest string values (port + path stripped).
extract_hosts() {
  # `|| true` so a no-match grep (empty file section) doesn't trip set -e/pipefail.
  { printf '%s\n' "$1" | grep -oE 'https?://[A-Za-z0-9._~:-]+' | sed -E 's#https?://##; s#[:/].*##'; } || true
  { printf '%s\n' "$1" \
      | grep -oE "\"(${mimicry_keys}|dest)\"[[:space:]]*:[[:space:]]*(\[[[:space:]]*)?\"[^\"]+\"" \
      | grep -oE '"[^"]*"$' | tr -d '"' | sed -E 's#:[0-9]+$##'; } || true
}

sub_files=()
while IFS= read -r _line; do
  sub_files+=("${_line}")
done < <(find services/subscription/config -type f -name '*.json' 2>/dev/null | sort)

for f in "${sub_files[@]}"; do
  body="$(strip_comments "${f}")"

  # 2a. IPv4 outside the documentation/allowlisted set = a real address leak.
  hits_ip="$(printf '%s\n' "${body}" | grep -nE "${ipv4}" | grep -vE "${allow_ip_sub}" || true)"
  if [ -n "${hits_ip}" ]; then
    echo "NON-DOC IPv4 in ${f} (use RFC5737 TEST-NET):"; printf '%s\n' "${hits_ip}"; fail=1
  fi

  # 2b. Hostname outside the documentation TLDs / infra allowlist = a real domain.
  while IFS= read -r h; do
    [ -z "${h}" ] && continue
    printf '%s' "${h}" | grep -qE "^${ipv4}$" && continue          # IPs handled by 2a
    printf '%s' "${h}" | grep -qE '\.[A-Za-z]{2,}$' || continue    # not a hostname
    printf '%s' "${h}" | grep -qE "${doc_tld}" && continue         # .test/.example/...
    printf '%s' "${h}" | grep -qE "^(${allow_host})$" && continue  # allowlisted infra
    echo "NON-DOC hostname in ${f}: ${h} (use a .test/.example placeholder)"; fail=1
  done < <(extract_hosts "${body}")
done

if [ "${fail}" -ne 0 ]; then
  echo "no-hardcode check FAILED — move the value to a variable / use a doc placeholder." >&2
  exit 1
fi
echo "no-hardcode check passed (${#files[@]} template files + ${#sub_files[@]} config files scanned)."
