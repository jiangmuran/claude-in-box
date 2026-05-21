#!/usr/bin/env bash
# claude-in-box container entrypoint.
#
# Order of operations:
#   1. SOCKS5: when CIB_PROXY_URL is set, render redsocks.conf, start redsocks
#      in the background, and install nftables rules that DNAT all outbound
#      TCP (except loopback + the proxy host itself) to the local redsocks
#      port. Tear down on SIGTERM / EXIT.
#   2. Bundled services: if CIB_SERVICES is set, start the listed ones
#      (redis, postgres, nginx, docker) via cib-services.
#   3. Hand off to the CMD — by default `cib`, the control plane.
set -euo pipefail

log() { printf '[entrypoint] %s\n' "$*" >&2; }

REDSOCKS_LOCAL_PORT="${REDSOCKS_LOCAL_PORT:-12345}"
NFT_TABLE="cib_socks5"

teardown_socks() {
    if [[ -n "${REDSOCKS_PID:-}" ]] && kill -0 "$REDSOCKS_PID" 2>/dev/null; then
        kill "$REDSOCKS_PID" 2>/dev/null || true
    fi
    if command -v nft >/dev/null 2>&1; then
        nft delete table ip "$NFT_TABLE" 2>/dev/null || true
    fi
}
trap teardown_socks EXIT TERM INT

setup_socks() {
    local url="$1"
    log "configuring transparent SOCKS5 from \$CIB_PROXY_URL"

    # Parse socks5[h]://[user:pass@]host:port[/]
    if ! [[ "$url" =~ ^socks5h?://(([^:@]+):([^@]+)@)?([^:/]+):([0-9]+)/?$ ]]; then
        log "ERROR: cannot parse CIB_PROXY_URL — expected socks5://[user:pass@]host:port"
        exit 1
    fi
    local UPSTREAM_USER="${BASH_REMATCH[2]:-}"
    local UPSTREAM_PASS="${BASH_REMATCH[3]:-}"
    local UPSTREAM_HOST="${BASH_REMATCH[4]}"
    local UPSTREAM_PORT="${BASH_REMATCH[5]}"

    # Resolve upstream IP so nftables can exclude it; also fail fast on DNS.
    local PROXY_IP
    PROXY_IP="$(getent ahostsv4 "$UPSTREAM_HOST" | awk 'NR==1{print $1}')"
    if [[ -z "$PROXY_IP" ]]; then
        log "ERROR: cannot resolve proxy host $UPSTREAM_HOST"
        exit 1
    fi
    log "upstream=$UPSTREAM_HOST ($PROXY_IP):$UPSTREAM_PORT, local redirect=:$REDSOCKS_LOCAL_PORT"

    # Render redsocks.conf. Bash heredoc, not envsubst — keeps `${VAR:-default}`
    # syntax in the template usable.
    install -d -m 0755 /etc/claude-in-box /var/log/claude-in-box
    cat > /etc/claude-in-box/redsocks.conf <<EOF
base {
    log_debug = off;
    log_info = on;
    log = stderr;
    daemon = off;
    redirector = iptables;
}

redsocks {
    local_ip   = 127.0.0.1;
    local_port = ${REDSOCKS_LOCAL_PORT};
    ip         = ${PROXY_IP};
    port       = ${UPSTREAM_PORT};
    type       = socks5;
    login      = "${UPSTREAM_USER}";
    password   = "${UPSTREAM_PASS}";
}
EOF
    chmod 0640 /etc/claude-in-box/redsocks.conf

    # Start redsocks in foreground mode but background it from this shell.
    redsocks -c /etc/claude-in-box/redsocks.conf \
        >/var/log/claude-in-box/redsocks.log 2>&1 &
    REDSOCKS_PID=$!
    # Give redsocks ~0.3s to fail fast on a bad config; not enough to slow boot.
    sleep 0.3
    if ! kill -0 "$REDSOCKS_PID" 2>/dev/null; then
        log "ERROR: redsocks failed to start"
        tail -20 /var/log/claude-in-box/redsocks.log >&2 || true
        exit 1
    fi

    # nftables: DNAT every outbound TCP (except loopback and the proxy host
    # itself) to the local redsocks port. The `output` hook runs in the same
    # netns; no PREROUTING needed inside a container.
    nft -f - <<NFT
table ip ${NFT_TABLE} {
    chain output {
        type nat hook output priority -100; policy accept;
        oif "lo"                              return
        meta skuid 0 ip daddr 127.0.0.0/8     return
        ip daddr ${PROXY_IP}                  return
        meta l4proto tcp tcp dport ${REDSOCKS_LOCAL_PORT} return
        meta l4proto tcp redirect to :${REDSOCKS_LOCAL_PORT}
    }
}
NFT

    # /etc/resolv.conf inside containers is usually 127.0.0.11 (Docker's
    # embedded DNS). Leave it alone — DNS queries are over UDP and our
    # nftables rules only catch TCP. If the user explicitly wants TCP DNS
    # routed too, they can set CIB_DNS_OVER_TCP=1 later.

    log "transparent SOCKS5 active (redsocks pid=$REDSOCKS_PID)"
}

log "claude-in-box starting (mode=${CIB_MODE:-default})"

if [[ -n "${CIB_PROXY_URL:-}" ]]; then
    setup_socks "$CIB_PROXY_URL"
fi

if [[ -n "${CIB_SERVICES:-}" ]]; then
    log "starting bundled services: ${CIB_SERVICES}"
    /usr/local/bin/cib-services start "${CIB_SERVICES}"
fi

exec "$@"
