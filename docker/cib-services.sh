#!/usr/bin/env bash
# claude-in-box bundled service starter.
#
# Usage:
#   cib-services start postgres,redis,nginx
#   cib-services status
#   cib-services stop  postgres
#
# Or — and this is the common path — the container entrypoint passes
# whatever is in $CIB_SERVICES to `cib-services start` before exec'ing the
# control plane.
#
# Each service is supervised lazily: started in the background, PID written
# to /var/run/cib/<svc>.pid, logs at /var/log/cib/<svc>.log. No systemd; we
# do not need one for a container of this size.
set -euo pipefail

RUN_DIR=/var/run/cib
LOG_DIR=/var/log/cib
mkdir -p "$RUN_DIR" "$LOG_DIR"

log() { printf '[cib-services] %s\n' "$*" >&2; }

start_redis() {
    if pgrep -x redis-server >/dev/null 2>&1; then
        log "redis already running"
        return
    fi
    log "starting redis-server on :6379"
    redis-server --daemonize yes \
        --logfile "$LOG_DIR/redis.log" \
        --pidfile "$RUN_DIR/redis.pid" \
        --bind 0.0.0.0 \
        --protected-mode no
}

start_postgres() {
    local PG_BIN PG_VER PG_DATA
    PG_BIN=$(ls -d /usr/lib/postgresql/*/bin 2>/dev/null | sort -V | tail -1)
    if [[ -z "${PG_BIN}" ]]; then
        log "postgres binaries not found"
        return 1
    fi
    PG_VER=$(basename "$(dirname "$PG_BIN")")
    PG_DATA=/var/lib/postgresql/${PG_VER}/main

    if [[ ! -s "${PG_DATA}/PG_VERSION" ]]; then
        log "initialising postgres cluster at ${PG_DATA}"
        install -d -o postgres -g postgres -m 0700 "${PG_DATA}"
        su postgres -c "${PG_BIN}/initdb -D '${PG_DATA}' --auth-local=trust --auth-host=md5 --username=postgres"
        # Listen on all interfaces so other containers in the same network
        # can reach it, but keep host-based auth conservative.
        echo "listen_addresses = '*'" >> "${PG_DATA}/postgresql.conf"
        echo "host all all 0.0.0.0/0 md5" >> "${PG_DATA}/pg_hba.conf"
    fi

    log "starting postgres ${PG_VER} on :5432"
    su postgres -c "${PG_BIN}/pg_ctl -D '${PG_DATA}' -l '${LOG_DIR}/postgres.log' -w start"
}

start_nginx() {
    if pgrep -x nginx >/dev/null 2>&1; then
        log "nginx already running"
        return
    fi
    if [[ ! -f /etc/nginx/sites-enabled/cib-default ]]; then
        cp /etc/claude-in-box/nginx.default.conf /etc/nginx/sites-enabled/cib-default
        rm -f /etc/nginx/sites-enabled/default
    fi
    log "starting nginx"
    nginx
}

start_docker() {
    # We bundle docker.io which has both the CLI and dockerd. Auto-starting
    # dockerd requires --privileged on `docker run` and is rarely what the
    # user wants — they normally mount the host /var/run/docker.sock instead.
    # So `cib-services start docker` is opt-in only and may need --privileged.
    if pgrep -x dockerd >/dev/null 2>&1; then
        log "dockerd already running"
        return
    fi
    log "starting dockerd (requires --privileged container)"
    nohup dockerd >"${LOG_DIR}/docker.log" 2>&1 &
    echo $! > "${RUN_DIR}/docker.pid"
}

stop_one() {
    case "$1" in
        redis)    pkill -x redis-server || true ;;
        postgres)
            local PG_BIN
            PG_BIN=$(ls -d /usr/lib/postgresql/*/bin 2>/dev/null | sort -V | tail -1)
            [[ -n "${PG_BIN}" ]] && su postgres -c "${PG_BIN}/pg_ctl -D /var/lib/postgresql/*/main stop -m fast" || true
            ;;
        nginx)    pkill -x nginx || true ;;
        docker)   pkill -x dockerd || true ;;
        *)        log "unknown service: $1"; return 1 ;;
    esac
}

start_one() {
    case "$1" in
        redis)    start_redis ;;
        postgres) start_postgres ;;
        nginx)    start_nginx ;;
        docker)   start_docker ;;
        *)        log "unknown service: $1"; return 1 ;;
    esac
}

cmd=${1:-status}
shift || true

case "$cmd" in
    start)
        list=${1:-}
        if [[ -z "$list" ]]; then
            log "nothing to start"
            exit 0
        fi
        IFS=',' read -r -a svcs <<< "$list"
        for s in "${svcs[@]}"; do
            s=$(echo "$s" | tr -d '[:space:]')
            [[ -z "$s" ]] && continue
            start_one "$s"
        done
        ;;
    stop)
        list=${1:-}
        IFS=',' read -r -a svcs <<< "$list"
        for s in "${svcs[@]}"; do
            s=$(echo "$s" | tr -d '[:space:]')
            [[ -z "$s" ]] && continue
            stop_one "$s"
        done
        ;;
    status)
        for s in redis postgres nginx docker; do
            case "$s" in
                redis)    pgrep -x redis-server >/dev/null && echo "$s: up" || echo "$s: down" ;;
                postgres) pgrep -x postgres     >/dev/null && echo "$s: up" || echo "$s: down" ;;
                nginx)    pgrep -x nginx        >/dev/null && echo "$s: up" || echo "$s: down" ;;
                docker)   pgrep -x dockerd      >/dev/null && echo "$s: up" || echo "$s: down" ;;
            esac
        done
        ;;
    *)
        echo "usage: cib-services {start|stop|status} [svc1,svc2,...]" >&2
        exit 1
        ;;
esac
