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

# When the host's docker CLI has a `proxies` block in ~/.docker/config.json,
# `docker run` auto-injects HTTP_PROXY / HTTPS_PROXY / etc into the container
# env. Node fetch (used by `claude`) cannot parse `socks5://` URLs in those
# vars and crashes with `UnsupportedProxyProtocol`. We route via transparent
# SOCKS5 at the network layer (redsocks + nftables) — apps must NOT see the
# proxy at the env layer. Strip the auto-injected ones early so every child
# we spawn from here is clean.
unset HTTP_PROXY  HTTPS_PROXY  FTP_PROXY  ALL_PROXY  NO_PROXY
unset http_proxy  https_proxy  ftp_proxy  all_proxy  no_proxy

if [[ -n "${CIB_PROXY_URL:-}" ]]; then
    setup_socks "$CIB_PROXY_URL"
fi

if [[ -n "${CIB_SERVICES:-}" ]]; then
    log "starting bundled services: ${CIB_SERVICES}"
    /usr/local/bin/cib-services start "${CIB_SERVICES}"
fi

# Claude Code refuses --dangerously-skip-permissions under root for safety,
# so the control plane (which spawns Claude) must run unprivileged. Network
# setup above needed root (nftables / NET_ADMIN). Hand off to the
# unprivileged `coder` user now. The bind-mounted volumes are chowned so
# Claude Code can write its credentials + transcripts there.
if [[ "$(id -u)" -eq 0 ]] && [[ "${CIB_RUN_AS_ROOT:-0}" != "1" ]]; then
    for d in /workspace /var/lib/claude-in-box /home/coder/.claude; do
        if [[ -d "$d" ]]; then
            chown -R coder:coder "$d" 2>/dev/null || true
        fi
    done
    # Skip Claude Code's first-run theme picker by setting
    # `hasCompletedOnboarding: true` at the TOP LEVEL of ~/.claude.json
    # — verified against the v2.1.146 binary: that is the only key that
    # gates the picker render. It lives in the user's global STATE file
    # (~/.claude.json), NOT in ~/.claude/settings.json (different schema).
    #
    # Idempotent merge with jq so we never clobber oauthAccount /
    # mcpServers / projects that might already be there from a prior
    # login. Creates the file if absent.
    install -d -o coder -g coder -m 0700 /home/coder/.claude
    if [[ ! -e /home/coder/.claude.json ]]; then
        echo '{}' > /home/coder/.claude.json
    fi
    # Both flags must be true for a brand-new session to land in the REPL
    # straight away. Confirmed by `strings` against the v2.1.146 binary:
    #   hasCompletedOnboarding          → gates the theme picker
    #   bypassPermissionsModeAccepted   → gates the
    #     --dangerously-skip-permissions confirmation screen
    tmp=$(mktemp)
    jq '. + {
            hasCompletedOnboarding: true,
            lastOnboardingVersion: "2.1.146",
            bypassPermissionsModeAccepted: true
        }' /home/coder/.claude.json > "$tmp" && mv "$tmp" /home/coder/.claude.json
    chown coder:coder /home/coder/.claude.json
    chmod 0600 /home/coder/.claude.json

    log "dropping privileges → coder"
    # IMPORTANT: PAM's `pam_env` session module on Debian can RE-APPLY
    # proxy env from /etc/environment and /etc/security/pam_env.conf on
    # the runuser session boundary, undoing the unset we did above. Wrap
    # the child in a bash that unsets the proxy vars *inside* the new
    # session and only then execs the real command. Same applies to the
    # case where Docker's daemon proxies leaked into the container env in
    # the first place — they must NOT reach claude or its child node
    # fetch (which crashes on `socks5://` URLs).
    exec runuser -u coder -- bash -c '
        unset HTTP_PROXY HTTPS_PROXY FTP_PROXY ALL_PROXY NO_PROXY
        unset http_proxy https_proxy ftp_proxy all_proxy no_proxy
        exec "$@"
    ' -- "$@"
fi

# Even when we stay root (CIB_RUN_AS_ROOT=1) the proxy strip from earlier
# must still hold for any child of cib.
unset HTTP_PROXY HTTPS_PROXY FTP_PROXY ALL_PROXY NO_PROXY
unset http_proxy https_proxy ftp_proxy all_proxy no_proxy
exec "$@"
