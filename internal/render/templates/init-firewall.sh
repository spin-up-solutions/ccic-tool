#!/usr/bin/env bash
#
# ccic egress firewall — default-deny outbound with a resolved allowlist.
#
# Scope of protection: this is a speed bump against accidents and casual
# exfiltration, NOT a boundary against a hostile repository. Two honest caveats:
#
#   1. The allowlist is built from DNS A records captured at apply time. CDN
#      addresses rotate, so a long-running container can lose access to an
#      allowed domain (re-run `ccic firewall on` to refresh).
#   2. DNS still leaves the container. Direct UDP/53 to an arbitrary resolver is
#      blocked, which is the improvement over the common reference script, but
#      queries still reach Docker's embedded resolver on 127.0.0.11 and are
#      forwarded upstream from there. That narrows DNS tunnelling; it does not
#      close it.
#
# Usage: ccic-firewall [on|off|status|allow <domain|ip|cidr>...]

set -euo pipefail

SET_NAME="ccic-allow"
CHAIN="CCIC_OUT"

log() { printf 'ccic-firewall: %s\n' "$*" >&2; }

# Domains ccic itself needs. Project-specific additions arrive via
# CCIC_FIREWALL_ALLOW (whitespace- or comma-separated).
DEFAULT_DOMAINS="
  api.anthropic.com
  console.anthropic.com
  claude.ai
  registry.npmjs.org
  rubygems.org
  index.rubygems.org
  pypi.org
  files.pythonhosted.org
  static.crates.io
  index.crates.io
  mise.jdx.dev
  github.com
  api.github.com
  codeload.github.com
  objects.githubusercontent.com
  raw.githubusercontent.com
  release-assets.githubusercontent.com
"

teardown() {
  iptables -P OUTPUT ACCEPT 2>/dev/null || true
  iptables -D OUTPUT -j "${CHAIN}" 2>/dev/null || true
  iptables -F "${CHAIN}" 2>/dev/null || true
  iptables -X "${CHAIN}" 2>/dev/null || true
  ipset destroy "${SET_NAME}" 2>/dev/null || true
}

status() {
  if iptables -S OUTPUT 2>/dev/null | grep -q -- "-j ${CHAIN}"; then
    local n
    n="$(ipset list "${SET_NAME}" 2>/dev/null | grep -c '^[0-9]' || true)"
    echo "on (${n} allowed entries)"
  else
    echo "off"
  fi
}

# Add destinations to the live allowlist without a restart. Reading third-party
# documentation is a daily activity and WebFetch resolves from inside the
# container, so an all-or-nothing toggle would get switched off and left off.
allow_more() {
  if ! iptables -S OUTPUT 2>/dev/null | grep -q -- "-j ${CHAIN}"; then
    log "firewall is off — nothing to allow"
    return 0
  fi
  local added=0
  for target in "$@"; do
    [ -n "${target}" ] || continue
    # A literal address or CIDR goes straight in; anything else is resolved.
    if printf '%s' "${target}" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+(/[0-9]{1,2})?$'; then
      ipset add "${SET_NAME}" "${target}" 2>/dev/null || true
      log "allowed ${target}"
      added=$((added + 1))
      continue
    fi
    local ips
    ips="$(dig +short +time=2 +tries=2 A "${target}" 2>/dev/null | grep -E '^[0-9]+\.' || true)"
    if [ -z "${ips}" ]; then
      log "could not resolve ${target}"
      continue
    fi
    for ip in ${ips}; do
      ipset add "${SET_NAME}" "${ip}" 2>/dev/null || true
      added=$((added + 1))
    done
    log "allowed ${target} ($(echo "${ips}" | tr '\n' ' '))"
  done
  log "${added} destination(s) added — this lasts until the container restarts;"
  log "use [firewall] allow in .ccic.conf to make it permanent"
}

apply() {
  # Start from a clean, permissive state so DNS resolution below can succeed.
  teardown

  ipset create "${SET_NAME}" hash:net family inet

  local domains="${DEFAULT_DOMAINS} ${CCIC_FIREWALL_ALLOW:-}"
  local resolved=0 failed=""

  for domain in ${domains//,/ }; do
    [ -n "${domain}" ] || continue
    local ips
    ips="$(dig +short +time=2 +tries=2 A "${domain}" 2>/dev/null | grep -E '^[0-9]+\.' || true)"
    if [ -z "${ips}" ]; then
      failed="${failed} ${domain}"
      continue
    fi
    for ip in ${ips}; do
      ipset add "${SET_NAME}" "${ip}" 2>/dev/null || true
      resolved=$((resolved + 1))
    done
  done

  # GitHub publishes its egress ranges; git/api/web cover clone, gh and
  # release downloads, which is how mise fetches most toolchains.
  local gh
  if gh="$(curl -fsS --max-time 10 https://api.github.com/meta 2>/dev/null)"; then
    for cidr in $(echo "${gh}" | jq -r '(.git + .api + .web)[]' 2>/dev/null | grep -v ':'); do
      ipset add "${SET_NAME}" "${cidr}" 2>/dev/null || true
      resolved=$((resolved + 1))
    done
  else
    log "could not fetch github.com/meta ranges — falling back to resolved A records"
  fi

  [ -n "${failed}" ] && log "unresolved (skipped):${failed}"

  iptables -N "${CHAIN}"

  # Loopback, including Docker's embedded resolver at 127.0.0.11.
  iptables -A "${CHAIN}" -o lo -j ACCEPT

  # Replies to connections we already allowed.
  iptables -A "${CHAIN}" -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT

  # The container's own attached Docker networks — this is what keeps postgres
  # and redis reachable on the internal network.
  for net in $(ip route show | awk '/proto kernel/ {print $1}'); do
    iptables -A "${CHAIN}" -d "${net}" -j ACCEPT
    log "allowing local network ${net}"
  done

  iptables -A "${CHAIN}" -m set --match-set "${SET_NAME}" dst -j ACCEPT
  iptables -A "${CHAIN}" -j REJECT --reject-with icmp-port-unreachable

  iptables -A OUTPUT -j "${CHAIN}"
  iptables -P OUTPUT DROP

  log "active — $(ipset list "${SET_NAME}" | grep -c '^[0-9]') allowed destinations"
}

case "${1:-on}" in
  on)     apply ;;
  off)    teardown; log "disabled — all egress permitted" ;;
  status) status ;;
  allow)  shift; [ $# -gt 0 ] || { echo "usage: ccic-firewall allow <domain|ip|cidr>..." >&2; exit 2; }; allow_more "$@" ;;
  *)      echo "usage: ccic-firewall [on|off|status|allow <domain|ip|cidr>...]" >&2; exit 2 ;;
esac
