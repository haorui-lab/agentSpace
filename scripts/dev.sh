#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
if [ ! -f web/dist/index.html ]; then
  (cd web && npm install && npm run build)
fi
if [ -z "${ADMIN_PASSWORD:-}" ]; then
  echo "请先设置 ADMIN_PASSWORD" >&2
  exit 1
fi
export REDIS_ADDR="${REDIS_ADDR:-127.0.0.1:6380}"
export ADMIN_USERNAME="${ADMIN_USERNAME:-admin}"
exec go run ./cmd/hellocenter
