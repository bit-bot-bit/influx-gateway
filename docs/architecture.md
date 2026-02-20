# Architecture

## Components
- `influx-gateway` (stateless HTTP service)
- Redis (query response cache)
- N identical InfluxDB 2.7 targets

## Auth model
- Client -> gateway: single bearer token (`GATEWAY_TOKEN`).
- Gateway -> downstream target: per-target Influx token.

## Routing model
- Weighted round-robin across configured targets.
- Retry on read failures (`FORWARD_RETRIES`).
- Health probing (`/health`) and per-target circuit breaker.

## Cache model
- Key: `igw:v1:<org>:<sha256(org + query)>`
- Anti-stampede lock: `<key>:lock` via Redis `SETNX`.
- `now()` split for CSV into cached + live segments.

## Deployment model
- Helm chart deploys Deployment/Service/Ingress/HPA/ConfigMap/Secret.
- Gateway replicas are stateless and horizontally scalable.
