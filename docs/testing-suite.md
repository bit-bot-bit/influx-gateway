# Testing Suite Reference

## Overview
The test stack validates both read-path behavior (gateway/cache/routing) and write-path behavior (ingest/backpressure/high-cardinality pressure).

Main entry points:
- `make test` (`./scripts/local-test.sh`): split + cache smoke
- `make stampede` (`./scripts/local-stampede-test.sh`): anti-stampede lock behavior
- `make breaker` (`./scripts/local-breaker-test.sh`): failover/circuit-breaker behavior
- `make load` (`./scripts/load-4gb-each.sh`): high-volume synthetic ingest
- `make query-suite` (`./scripts/query-suite.sh`): complex Flux query sweep
- `make latency` (`./scripts/latency-bench.sh`): p50/p90/p95/p99 + throughput
- `make perf-suite` (`./scripts/perf-suite.sh`): baseline and high-concurrency benchmark
- `make large-suite` (`./scripts/large-suite.sh`): full end-to-end run

## Stage Order (`large-suite`)
Default execution order:
1. seed data
2. split/cache smoke
3. stampede
4. breaker
5. high-cardinality load (~4 GiB per target by default)
6. settle delay (`POST_LOAD_SETTLE_SECONDS`, default `45`)
7. complex query suite
8. latency/perf benchmark

Controls:
- `SKIP_BASE=1`
- `SKIP_LOAD=1`
- `SKIP_QUERY_SUITE=1`
- `SKIP_PERF=1`
- `AUTO_SETUP=0`
- `RESET_STACK=1`
- `REBUILD_IMAGES=1`

## Ingest Path
By default synthetic writers go through Telegraf ingest:
- `http://localhost:8186/api/v2/write`

Telegraf fans out to both Influx targets (`influx-a`, `influx-b`), so both receive equivalent synthetic streams.

## Load Model
`cmd/loadgen` emits a mixed profile:
- `profile=stress` high-cardinality points
- `profile=viz` low-cardinality dashboard-friendly points

Mix ratio:
- `VIZ_RATIO` / `-viz-ratio` default `0.20` (20% viz, 80% stress)

### Stress Profile Cardinality Dimensions
`cpu` tags:
- `tenant`: 1000
- `host`: 20000
- `region`: 4
- `cluster`: 80
- `pod`: 250000
- `service`: 1200

`http_requests` tags:
- `tenant`: 1000
- `host`: 20000
- `region`: 4
- `service`: 1200
- `endpoint`: 5
- `method`: 4
- `status`: 9
- `session`: 2000000

`orders` tags:
- `tenant`: 1000
- `region`: 4
- `service`: 1200
- `customer_id`: 2500000
- `order_id`: 5000000

`security_events` tags:
- `tenant`: 1000
- `region`: 4
- `host`: 20000
- `service`: 1200
- `actor`: 7000000
- `severity`: 4

Note:
- Actual realized series depend on runtime combinations, but the generator intentionally uses very large tag spaces to drive high cardinality and churn.

### Viz Profile Cardinality Dimensions
Small stable sets for readable dashboards:
- `tenant`: 3
- `host`: 4
- `region`: 3
- `service`: 4
- `endpoint`: 5
- `method`: 3
- `status`: 8

## Load Defaults
From `scripts/load-4gb-each.sh`:
- `TARGET_BYTES=4294967296` per target
- `WORKERS=1`
- `MAX_BATCH_BYTES=65536`
- `MAX_RETRIES=40`
- `RETRY_BACKOFF=3s`
- `INTER_BATCH_SLEEP=75ms`
- `REPORT_PERIOD=5s`

## Query Suite Defaults
From `scripts/query-suite.sh`:
- `QUERY_PROFILE=viz`
- `QUERY_WINDOW=30m`
- `QUERY_LONG_WINDOW=60m`
- `QUERY_AGG=1m`
- `QUERY_TIMEOUT_SECONDS=75`

For heavier checks:
- `QUERY_PROFILE=stress`
- reduce windows (`10m` to `20m`) on smaller hosts to avoid timeout noise.

## Benchmark Defaults
Latency benchmark (`scripts/latency-bench.sh`):
- `CONCURRENCY=20`
- `DURATION=60s`
- `WARMUP=5`
- `PROGRESS_INTERVAL=5s`

Perf suite (`scripts/perf-suite.sh`):
- baseline latency pass
- high pass with `HIGH_CONCURRENCY=60`, `HIGH_DURATION=90s`

## Expected Failure Modes Under Pressure
- `cache-max-memory-size exceeded` from Influx during heavy ingest bursts
- `context canceled` when rapid refreshes supersede in-flight queries
- transient `502` from gateway when upstream requests time out or are canceled
- elevated `MISS_RACE` when many requests contend for same cache key before winner writes cache

These are useful signals during stress testing; they are not always correctness bugs.
