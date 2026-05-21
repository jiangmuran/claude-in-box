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

log() { printf '[remote-deploy] %s\n' "$*" >&2; }

if [[ ! -s "$DATA/secrets/cib.env" ]]; then
    log "ERROR: $DATA/secrets/cib.env missing or empty"
    exit 1
fi

log "pulling $IMAGE"
docker pull "$IMAGE"

log "stopping any previous container"
docker rm -f "$NAME" >/dev/null 2>&1 || true

log "running $NAME on host port :$HOST_PORT"
docker run -d \
    --name "$NAME" \
    --restart unless-stopped \
    --cap-add NET_ADMIN \
    -p "$HOST_PORT:8080" \
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
