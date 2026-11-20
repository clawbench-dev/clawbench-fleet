#!/usr/bin/env bash
# Build clawbench-fleet: frontend (Vue 3 + Vite) then backend (Go) with the
# frontend embedded into the single binary.
set -euo pipefail
cd "$(dirname "$0")"

FLEET_VERSION="${FLEET_VERSION:-$(git describe --tags --always 2>/dev/null || echo 'dev')}"

echo "==> building frontend"
(cd web && npm ci --no-audit --no-fund 2>/dev/null || npm install --no-audit --no-fund)
(cd web && npm run build)

echo "==> embedding frontend"
rm -rf internal/web/dist
mkdir -p internal/web/dist
cp -r web/dist/. internal/web/dist/
# Keep the embed pattern valid for a clean checkout that has not been built.
touch internal/web/dist/.gitkeep

echo "==> building backend (static, no glibc dependency)"
CGO_ENABLED=0 go build -ldflags "-X clawbench-fleet/internal/version.Version=${FLEET_VERSION}" \
  -o clawbench-fleet ./cmd/fleet

echo "==> done: $(pwd)/clawbench-fleet (version ${FLEET_VERSION})"
