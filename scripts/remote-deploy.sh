#!/usr/bin/env bash
# Pull the latest claude-in-box image on the test host and run it.
# Run this script ON the remote host as root (not from your laptop).
#
#   ./remote-deploy.sh
#
# Expects /opt/claude-in-box/{secrets,sessions,workspace,claude-home}
# to already exist (created by the bootstrap step in the calling code).
set -euo pipefail

IMAGE=${CIB_IMAGE:-ghcr.io/jiangmuran/claude-in-box:latest}
HOST_PORT=${CIB_HOST_PORT:-8090}
NAME=${CIB_NAME:-cib-test}
DATA=/opt/claude-in-box

# Pre-allocated host port range for the /api/ports/expose feature
# (lets sessions surface in-container services like vite, fastapi etc.
# without rebuilding the docker run line). Set CIB_PORT_RANGE="" to
# disable; the /api/ports/* endpoints then return 503.
PORT_RANGE=${CIB_PORT_RANGE:-9000-9019}

log() { printf '[remote-deploy] %s\n' "$*" >&2; }

if [[ ! -s "$DATA/secrets/cib.env" ]]; then
    log "ERROR: $DATA/secrets/cib.env missing or empty"
    exit 1
fi

log "pulling $IMAGE"
docker pull "$IMAGE"

log "stopping any previous container"
docker rm -f "$NAME" >/dev/null 2>&1 || true

port_args=( -p "$HOST_PORT:8080" )
env_args=()
if [[ -n "$PORT_RANGE" ]]; then
    log "exposing host port range $PORT_RANGE for /api/ports/expose"
    port_args+=( -p "$PORT_RANGE:$PORT_RANGE" )
    env_args+=( -e "CIB_PORT_RANGE=$PORT_RANGE" )
fi

log "running $NAME on host port :$HOST_PORT (range: ${PORT_RANGE:-none})"
docker run -d \
    --name "$NAME" \
    --restart unless-stopped \
    --cap-add NET_ADMIN \
    "${port_args[@]}" \
    "${env_args[@]}" \
    --env-file "$DATA/secrets/cib.env" \
    -v "$DATA/workspace:/workspace" \
    -v "$DATA/sessions:/var/lib/claude-in-box/sessions" \
    -v "$DATA/claude-home:/home/coder/.claude" \
    "$IMAGE"

log "container started, waiting for health"
for i in $(seq 1 30); do
    if curl -s -o /dev/null -w "%{http_code}" "http://127.0.0.1:$HOST_PORT/api/health" | grep -q 200; then
        log "healthy after ${i}s"
        docker ps --filter "name=$NAME" --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}'
        exit 0
    fi
    sleep 1
done

log "ERROR: never became healthy. recent logs:"
docker logs --tail 50 "$NAME"
exit 1
