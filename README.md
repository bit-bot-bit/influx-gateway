# influx-bouncer

InfluxDB 2.7 read gateway with:
- single external datasource token (`GATEWAY_TOKEN`)
- multiple downstream Influx targets with per-target tokens
- Redis query caching
- `now()` range split (`cached segment + live tail`) and CSV merge
- target health checks, retries, and circuit breaker
- local monitoring stack (Grafana + Prometheus + blackbox + cAdvisor)

Documentation map:
- `docs/local-testing.md`
- `docs/large-testing.md`
- `docs/testing-suite.md` (full stage flow + load model + cardinality dimensions)
- `docs/monitoring-local.md`

## Quick local run

```sh
podman compose -f deploy/local/docker-compose.yml up -d --build
./scripts/local-seed.sh
./scripts/local-test.sh
```

Gateway endpoint:
- `POST http://localhost:8090/api/v2/query?org=acme`
- `Authorization: Bearer gateway-reader`

Gateway metrics:
- `http://localhost:8090/metrics`

Monitoring endpoints:
- Grafana: `http://localhost:3000` (`admin` / `admin`)
- Prometheus: `http://localhost:9090`
- cAdvisor (optional profile): `http://localhost:8088`
- Telegraf ingest: `http://localhost:8186/api/v2/write`

Grafana includes dashboards:
- `Influx Bouncer Local Fleet`
- `Influx Direct Metrics`
- `Influx Datasource Explorer`

Grafana datasources include `Influx Gateway (Flux)` at `http://gateway:8080`.

## Large suite (includes ~4 GiB per target ingest + perf)

```sh
./scripts/large-suite.sh
```

`large-suite` now self-bootstraps and retries flaky stages by default.
Detailed stage order and tuning: `docs/testing-suite.md`.

## Stampede test

```sh
./scripts/local-stampede-test.sh
```

Expected output is mostly `HIT_WAIT`/`HIT` with at most one `MISS` per cache key refresh window.

## Circuit breaker test

```sh
./scripts/local-breaker-test.sh
```

## Heavy ingest only

```sh
./scripts/load-4gb-each.sh
```

Use env vars to tune volume/speed:
- `TARGET_BYTES` (default 4294967296 bytes per target)
- `WORKERS`
- `MAX_BATCH_BYTES`
- `MAX_RETRIES`
- `RETRY_BACKOFF`
- `INTER_BATCH_SLEEP`
- `VIZ_RATIO`

By default, load writes to Telegraf and Telegraf fans out to both Influx targets.
Override `TARGETS` if you want direct target writes.

Cardinality model details (stress vs viz profile and tag cardinalities):
- `docs/testing-suite.md`

## Latency benchmark

```sh
./scripts/latency-bench.sh
```

Outputs p50/p90/p95/p99, min/max, throughput, error rate, cache-header mix, and target-header distribution.

## Perf suite

```sh
./scripts/perf-suite.sh
```

Runs baseline and high-concurrency latency passes.

## Complex query suite

```sh
./scripts/query-suite.sh
```

## Helm

```sh
helm upgrade --install influx-gateway ./helm/influx-gateway -n observability --create-namespace
```

Tune in `helm/influx-gateway/values.yaml`:
- `gateway.token`
- `gateway.upstreamTimeoutSeconds`
- `redis.addr`
- `targets[]` (url/token/weight)
- `gateway.freshnessWindowSeconds`
- `gateway.cacheTTLSeconds`
- `gateway.cacheLockTTLSeconds`
- `gateway.cacheLockWaitMilliseconds`
- `gateway.forwardRetries`
- `gateway.probeIntervalSeconds`
- `gateway.breakerFailures`
- `gateway.breakerCooldownSeconds`

## Current scope
- Implemented: CSV split/merge, Arrow pass-through with cache for non-split, anti-stampede lock, weighted round robin, retries, health probes, circuit breaking.
- Not implemented yet: Arrow split/merge concat, per-bucket routing rules.

## Container runtime note
- Commands use `podman compose` by default.
- If you prefer Docker, replace `podman compose` with `docker compose`.


## Optional host metrics (cAdvisor)

`cadvisor` is disabled by default for rootless Podman compatibility.

Enable it with:

```sh
podman compose -f deploy/local/docker-compose.yml --profile host-metrics up -d
```
