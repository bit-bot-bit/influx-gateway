# Local Testing

## Prerequisites
- Podman + Podman Compose
- curl

## Start stack
```sh
podman compose -f deploy/local/docker-compose.yml up -d --build
```

Monitoring UI is available after startup:
- Grafana: `http://localhost:3000` (`admin` / `admin`)
- Prometheus: `http://localhost:9090`

Telegraf is also started locally:
- Prometheus scrape target: `telegraf:9273`
- Host access: `http://localhost:9273/metrics`
- It writes simultaneously to `influx-a` and `influx-b` and emits internal backpressure/drop metrics.
- ingest endpoint for synthetic writers: `http://localhost:8186/api/v2/write`

A `ministream` container is also started:
- continuously writes predictable `profile=viz` points every 2s
- writes to Telegraf ingest, then Telegraf fans out to both `influx-a` and `influx-b`
- keeps dashboards active even when load tests are not running

## Seed both targets
```sh
./scripts/local-seed.sh
```
`local-seed` now writes to Telegraf ingest by default.

## Validate split + cache
```sh
./scripts/local-test.sh
```

Look for:
- request 1: `X-Cache: MISS`, `X-Now-Split: true`
- request 2: `X-Cache: HIT`, `X-Now-Split: true`

## Validate anti-stampede
```sh
./scripts/local-stampede-test.sh
```

## Validate circuit breaker/failover
```sh
./scripts/local-breaker-test.sh
```

## Heavy ingest (~4 GiB per target)
```sh
./scripts/load-4gb-each.sh

# Optional: increase dashboard-friendly series density (default 0.20)
VIZ_RATIO=0.35 ./scripts/load-4gb-each.sh
```

## Complex query suite
```sh
./scripts/query-suite.sh
```

## Full large suite
```sh
./scripts/large-suite.sh
```

By default `large-suite` self-bootstraps the stack and waits for health.

Useful controls:
- `AUTO_SETUP=0` skip auto startup/waits
- `RESET_STACK=1` run `compose down -v` before suite
- `REBUILD_IMAGES=1` rebuild images during bootstrap
- `STAGE_RETRIES` and `STAGE_RETRY_DELAY`

For heavy-volume guidance and tunables, see `docs/large-testing.md`.
For full suite flow and cardinality model details, see `docs/testing-suite.md`.

## Query failure/slow query catalog logs
Gateway now logs failed queries and successful slow queries with:
- `sig` (query signature)
- `query` (trimmed preview)
- `dur_ms`, `status`, `stage`, `split`

Tunables in gateway env:
- `SLOW_QUERY_THRESHOLD_MILLISECONDS` (default `2000`)
- `QUERY_PREVIEW_CHARS` (default `240`)

Tail gateway logs:
```sh
make logs
```

## Latency benchmark
```sh
./scripts/latency-bench.sh
```

## Manual direct query
```sh
curl -sS -XPOST 'http://localhost:8090/api/v2/query?org=acme' \
  -H 'Authorization: Bearer gateway-reader' \
  -H 'Content-Type: application/vnd.flux' \
  -H 'Accept: application/csv' \
  --data-binary 'from(bucket:"metrics") |> range(start:-5m, stop: now())'
```
