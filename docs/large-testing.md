# Large Test Suite

## Goal
Run a comprehensive suite that includes:
- functional correctness (`now()` split, cache hit path)
- anti-stampede behavior under concurrency
- circuit breaker failover behavior
- high-volume/high-cardinality writes (~4 GiB per target)
- complex query sweep against loaded data
- latency and throughput benchmarks (p50/p95/p99, RPS, error rate)
- deterministic fan-out writes so all targets stay identical

For exact load generator schema/cardinality dimensions and suite stage flow:
- see `docs/testing-suite.md`.

## Prerequisites
- Podman + Podman Compose
- Internet access for pulling `golang:1.23` image used by load/bench tools
- Sufficient local disk (expect well above 8 GiB of persisted data across two targets)

## One-command run
```sh
./scripts/large-suite.sh
```

`large-suite` auto-bootstraps by default:
- starts stack (`compose up -d`)
- waits for health (`influx-a`, `influx-b`, `gateway`)
- retries flaky stages (`STAGE_RETRIES`)

## Heavy ingest only (default 4 GiB per target)
```sh
./scripts/load-4gb-each.sh
```

## Latency benchmark only
```sh
./scripts/latency-bench.sh
```

## Perf suite (baseline + high concurrency)
```sh
./scripts/perf-suite.sh
```

## Useful tuning env vars
- `TARGET_BYTES` (default `4294967296` per target)
- `WORKERS` (default `1`)
- `MAX_BATCH_BYTES` (default `65536`)
- `REPORT_PERIOD` (default `5s`)
- `MAX_RETRIES` (default `40`)
- `RETRY_BACKOFF` (default `3s`)
- `INTER_BATCH_SLEEP` (default `75ms`)
- `VIZ_RATIO` (default `0.20`, fraction of low-cardinality dashboard-friendly series)
- `TARGETS` (default `telegraf=http://localhost:8186`; Telegraf fans out to both Influx targets)
- `CONCURRENCY` (latency bench default `20`)
- `DURATION` (latency bench default `60s`)
- `HIGH_CONCURRENCY` (perf suite default `60`)
- `HIGH_DURATION` (perf suite default `90s`)
- `QUERY_PROFILE` (`viz` by default in query-suite; options: `viz|stress|any`)
- `QUERY_WINDOW` (default `30m`)
- `QUERY_LONG_WINDOW` (default `60m`)
- `QUERY_TIMEOUT_SECONDS` (default `75`)

## Cardinality Notes
The default model is intentionally high-cardinality (`profile=stress`) with large tag domains
for `host`, `pod`, `session`, `customer_id`, `order_id`, and `actor`.
Dashboard-friendly low-cardinality data is `profile=viz`.

The split is controlled by `VIZ_RATIO` (default `0.20`).

Example (faster dev run):
```sh
TARGET_BYTES=$((512*1024*1024)) WORKERS=4 ./scripts/load-4gb-each.sh

# Increase dashboard-friendly series mix (default is 0.20)
VIZ_RATIO=0.35 ./scripts/load-4gb-each.sh

# Query suite defaults tuned for stable post-load execution
QUERY_PROFILE=viz QUERY_WINDOW=30m QUERY_TIMEOUT_SECONDS=75 ./scripts/query-suite.sh

# Heavier stress-path query sweep (more likely to time out on small hosts)
QUERY_PROFILE=stress QUERY_WINDOW=20m QUERY_TIMEOUT_SECONDS=120 ./scripts/query-suite.sh
```

## Suite controls
- `AUTO_SETUP=0` disables auto stack bootstrap
- `RESET_STACK=1` does `compose down -v` before suite
- `REBUILD_IMAGES=1` uses `compose up -d --build`
- `STAGE_RETRIES` (default `2`)
- `STAGE_RETRY_DELAY` (default `5` seconds)
- `SKIP_BASE=1` skips seed/smoke/stampede/breaker checks
- `SKIP_LOAD=1` skips heavy ingest
- `SKIP_QUERY_SUITE=1` skips complex query sweep
- `SKIP_PERF=1` skips latency/perf benchmark stage

Aggressive mode example:
```sh
WORKERS=4 MAX_BATCH_BYTES=262144 INTER_BATCH_SLEEP=0s MAX_RETRIES=20 RETRY_BACKOFF=2s ./scripts/load-4gb-each.sh
```
- `POST_LOAD_SETTLE_SECONDS` (default `45`) waits after heavy load before query/perf stages
