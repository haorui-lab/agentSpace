#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
export AGENT_SPACE_TEST_REDIS="${AGENT_SPACE_TEST_REDIS:-127.0.0.1:6380}"
if command -v redis-cli >/dev/null 2>&1; then
  version="$(redis-cli -h "${AGENT_SPACE_TEST_REDIS%%:*}" -p "${AGENT_SPACE_TEST_REDIS##*:}" INFO server | awk -F: '/^redis_version:/{gsub(/\r/,"",$2); print $2}')"
  major="${version%%.*}"
  case "$major" in
    ''|*[!0-9]*)
      echo "无法确认 Redis 版本，当前是 ${version:-无法连接 $AGENT_SPACE_TEST_REDIS}" >&2
      exit 1
      ;;
  esac
  if [ "$major" -lt 4 ]; then
    echo "测试 Redis 最低是 4.0，当前是 $version" >&2
    echo "docker run -d --name as-redis4 -p 6380:6379 redis:4.0 redis-server --appendonly yes --appendfsync everysec" >&2
    exit 1
  fi
fi
go test ./internal/as ./cmd/agentspace ./tests -count=1 -timeout 180s
