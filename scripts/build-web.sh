#!/usr/bin/env bash
# Builds the Svelte UI and stages it into internal/server/webdist/ so the
# next `go build` produces a binary with the UI embedded. Run from the repo
# root.
set -euo pipefail

cd "$(dirname "$0")/.."

if ! command -v npm >/dev/null 2>&1; then
    echo "npm not found on PATH" >&2
    exit 1
fi

npm --prefix web ci --no-audit --no-fund --prefer-offline
npm --prefix web run build

mkdir -p internal/server/webdist
# Wipe stale assets, keep .gitkeep.
find internal/server/webdist/ -mindepth 1 -name '.gitkeep' -prune -o -exec rm -rf {} +
cp -r web/dist/* internal/server/webdist/

echo "web bundle staged at internal/server/webdist/"
ls -la internal/server/webdist/
