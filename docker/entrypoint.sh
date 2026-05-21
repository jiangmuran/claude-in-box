#!/usr/bin/env bash
# claude-in-box container entrypoint.
#
# Responsibilities (M1.1 scope):
#   - announce mode + version on startup,
#   - exec the control-plane binary (or whatever is passed as CMD).
#
# Responsibilities arriving later:
#   - M1.6: bring up the transparent SOCKS5 redirect when CIB_PROXY_URL is set
#     (redsocks + nftables in the `nat` table, idempotent, clean teardown).
set -euo pipefail

log() { printf '[entrypoint] %s\n' "$*" >&2; }

log "claude-in-box starting (mode=${CIB_MODE:-default})"

if [[ -n "${CIB_PROXY_URL:-}" ]]; then
    log "CIB_PROXY_URL is set; transparent SOCKS5 redirect arrives in M1.6."
    log "for now traffic from inside the container goes direct."
fi

exec "$@"
