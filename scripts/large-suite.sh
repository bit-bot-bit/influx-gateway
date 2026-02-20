#!/usr/bin/env sh
set -eu

. ./scripts/lib.sh

SKIP_BASE="${SKIP_BASE:-0}"
SKIP_LOAD="${SKIP_LOAD:-0}"
SKIP_QUERY_SUITE="${SKIP_QUERY_SUITE:-0}"
SKIP_PERF="${SKIP_PERF:-0}"
AUTO_SETUP="${AUTO_SETUP:-1}"
RESET_STACK="${RESET_STACK:-0}"
REBUILD_IMAGES="${REBUILD_IMAGES:-0}"
STAGE_RETRIES="${STAGE_RETRIES:-2}"
STAGE_RETRY_DELAY="${STAGE_RETRY_DELAY:-5}"
POST_LOAD_SETTLE_SECONDS="${POST_LOAD_SETTLE_SECONDS:-45}"

run_stage() {
  name="$1"
  shift
  echo "[suite] $name"
  if retry "$STAGE_RETRIES" "$STAGE_RETRY_DELAY" "$@"; then
    return 0
  fi
  echo "[suite] stage failed after retries: $name" >&2
  return 1
}

echo "[suite] starting large suite"
require_cmd podman
require_cmd curl

if [ "$AUTO_SETUP" = "1" ]; then
  if [ "$RESET_STACK" = "1" ]; then
    echo "[suite] resetting stack"
    compose down -v || true
  fi
  echo "[suite] bootstrapping stack"
  bootstrap_stack "$REBUILD_IMAGES"
fi

if [ "$SKIP_BASE" != "1" ]; then
  run_stage "seed data" ./scripts/local-seed.sh
  run_stage "split/cache smoke" ./scripts/local-test.sh
  run_stage "stampede" ./scripts/local-stampede-test.sh
  run_stage "breaker" ./scripts/local-breaker-test.sh
fi

if [ "$SKIP_LOAD" != "1" ]; then
  run_stage "high-cardinality load (~4GiB per target by default)" ./scripts/load-4gb-each.sh
  if [ "$POST_LOAD_SETTLE_SECONDS" -gt 0 ]; then
    echo "[suite] settling after load for ${POST_LOAD_SETTLE_SECONDS}s"
    sleep "$POST_LOAD_SETTLE_SECONDS"
    wait_stack_ready
  fi
fi

if [ "$SKIP_QUERY_SUITE" != "1" ]; then
  run_stage "complex query suite" ./scripts/query-suite.sh
fi

if [ "$SKIP_PERF" != "1" ]; then
  run_stage "latency/performance benchmark" ./scripts/perf-suite.sh
fi

echo "[suite] complete"
