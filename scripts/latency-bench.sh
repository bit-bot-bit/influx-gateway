#!/usr/bin/env sh
set -eu

. ./scripts/lib.sh

GW_URL="${GW_URL:-http://localhost:8090/api/v2/query?org=acme}"
GATEWAY_TOKEN="${GATEWAY_TOKEN:-gateway-reader}"
CONCURRENCY="${CONCURRENCY:-20}"
DURATION="${DURATION:-60s}"
WARMUP="${WARMUP:-5}"
PROGRESS_INTERVAL="${PROGRESS_INTERVAL:-5s}"

QUERY='from(bucket: "metrics") |> range(start: -1h, stop: now()) |> filter(fn: (r) => r._measurement == "http_requests" and r._field == "latency_ms") |> aggregateWindow(every: 1m, fn: mean, createEmpty: false)'

wait_stack_ready

"$CONTAINER_RUNTIME" run --rm --network host \
  -v "$PWD":/src:Z -w /src \
  docker.io/library/golang:1.23 \
  /usr/local/go/bin/go run ./cmd/querybench \
    -url "$GW_URL" \
    -token "$GATEWAY_TOKEN" \
    -query "$QUERY" \
    -concurrency "$CONCURRENCY" \
    -duration "$DURATION" \
    -warmup "$WARMUP" \
    -progress-interval "$PROGRESS_INTERVAL"
