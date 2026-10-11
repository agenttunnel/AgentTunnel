#!/usr/bin/env bash
#
# Builds the AgentTunnel desktop app.
#
# `wails3 build` caches its native link step and does not watch the parent module
# (../internal, ../pkg), so a server-side Go change can leave bin/agenttunnel
# stale. This builds the frontend, then always relinks the binary from source.
#
# Usage: ./build.sh
set -euo pipefail

cd "$(dirname "$0")"

# Keep npm from pruning devDependencies, which would remove vite.
export NODE_ENV=development

echo "› building frontend"
( cd frontend && npm run build )

echo "› linking bin/agenttunnel"
go build -tags production -trimpath -buildvcs=false -ldflags="-w -s" -o bin/agenttunnel .

echo "› done: $(cd bin && pwd)/agenttunnel"
