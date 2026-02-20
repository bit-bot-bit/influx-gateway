# Local Monitoring

The local compose stack includes:
- Prometheus (`http://localhost:9090`)
- Grafana (`http://localhost:3000`, default `admin` / `admin`)
- blackbox exporter (`http://localhost:9115`)
- cAdvisor (`http://localhost:8088`, optional `host-metrics` profile)

Grafana is pre-provisioned with:
- Prometheus datasource
- `Influx Bouncer Local Fleet` dashboard
- `Telegraf Ingest & Backpressure` dashboard

Dashboard panels include:
- service probe success and duration for gateway/influx targets
- container CPU usage
- container memory working set

If cAdvisor metrics are empty under rootless Podman, run the stack rootful or adjust cgroup/container mounts for your host configuration.


Enable cAdvisor explicitly:

```sh
podman compose -f deploy/local/docker-compose.yml --profile host-metrics up -d cadvisor
```

If rootless Podman blocks cAdvisor privileges/mounts, keep it disabled and rely on blackbox + app-level latency benchmarks.


Gateway exported metrics include:
- `influx_gateway_cache_results_total{result=...}`
- `influx_gateway_redis_latency_seconds{op=...,ok=...}`
- `influx_gateway_upstream_latency_seconds{target=...,status=...}`

Prometheus scrapes gateway metrics at `gateway:8080/metrics`.


Grafana provisioned datasources:
- `Prometheus`
- `Influx Gateway (Flux)`
- `Influx-A (Flux)`
- `Influx-B (Flux)`

Additional dashboard:
- `Influx Direct Metrics` (queries each Influx directly via Flux).
- `Influx Datasource Explorer` (single-datasource mode with top variables).
: `Telegraf Ingest & Backpressure` (write errors, drops, buffer utilization, write latency, input throughput).
: Use `ds` to switch between Gateway, Influx-A, and Influx-B.
: Use `agg` to control query aggregation interval and pressure.

Load/cardinality reference:
- `docs/testing-suite.md`

Note: `_monitoring` panels depend on internal telemetry availability and may be sparse right after startup.


For query-latency comparison per datasource in Grafana, open a panel -> Query inspector and compare request timings for Gateway vs Influx-A vs Influx-B panels.

Gateway datasource notes:
- Grafana Influx datasource uses `Authorization: Token <gateway-token>`.
- Gateway now supports read proxying for other `/api/v2/*` GET/HEAD endpoints used by datasource discovery (for example buckets/org checks).
