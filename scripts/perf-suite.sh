#!/usr/bin/env sh
set -eu

echo "[perf] baseline latency benchmark"
./scripts/latency-bench.sh

echo "[perf] higher concurrency benchmark"
CONCURRENCY="${HIGH_CONCURRENCY:-60}" DURATION="${HIGH_DURATION:-90s}" ./scripts/latency-bench.sh

echo "[perf] done"
