#!/usr/bin/env sh
set -eu

. ./scripts/lib.sh

TARGETS="${TARGETS:-telegraf=http://localhost:8186}"
ORG="${ORG:-acme}"
BUCKET="${BUCKET:-metrics}"
TOKEN="${TOKEN:-admin-token}"
TARGET_BYTES="${TARGET_BYTES:-4294967296}"
WORKERS="${WORKERS:-1}"
MAX_BATCH_BYTES="${MAX_BATCH_BYTES:-65536}"
REPORT_PERIOD="${REPORT_PERIOD:-5s}"
MAX_RETRIES="${MAX_RETRIES:-40}"
RETRY_BACKOFF="${RETRY_BACKOFF:-3s}"
INTER_BATCH_SLEEP="${INTER_BATCH_SLEEP:-75ms}"
VIZ_RATIO="${VIZ_RATIO:-0.20}"

wait_stack_ready

# Uses a disposable Go container so host Go is not required.
podman run --rm --network host \
  -v "$PWD":/src:Z -w /src \
  docker.io/library/golang:1.23 \
  /usr/local/go/bin/go run ./cmd/loadgen \
    -targets "$TARGETS" \
    -org "$ORG" \
    -bucket "$BUCKET" \
    -token "$TOKEN" \
    -target-bytes "$TARGET_BYTES" \
    -workers "$WORKERS" \
    -max-batch-bytes "$MAX_BATCH_BYTES" \
    -report-period "$REPORT_PERIOD" \
    -max-retries "$MAX_RETRIES" \
    -retry-backoff "$RETRY_BACKOFF" \
    -inter-batch-sleep "$INTER_BATCH_SLEEP" \
    -viz-ratio "$VIZ_RATIO"
