#!/usr/bin/env bash
# claude-in-box container entrypoint.
#
# Order of operations:
#   1. SOCKS5: if CIB_PROXY_URL is set, configure redsocks + nftables.
#      (Placeholder until M1.6 lands; for now we just log and move on.)
#   2. Services: if CIB_SERVICES is set (comma-separated list of any of
#      redis, postgres, nginx, docker), start them via cib-services.
#   3. Hand off to the CMD — by default `cib`, the control plane.
set -euo pipefail

log() { printf '[entrypoint] %s\n' "$*" >&2; }

log "claude-in-box starting (mode=${CIB_MODE:-default})"

if [[ -n "${CIB_PROXY_URL:-}" ]]; then
    log "CIB_PROXY_URL is set; transparent SOCKS5 redirect arrives in M1.6."
    log "for now traffic from inside the container goes direct."
fi

if [[ -n "${CIB_SERVICES:-}" ]]; then
    log "starting bundled services: ${CIB_SERVICES}"
    /usr/local/bin/cib-services start "${CIB_SERVICES}"
fi

exec "$@"
