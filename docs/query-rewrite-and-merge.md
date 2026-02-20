# Query Rewrite And Merge Contract

## Split decision
- Split only when Flux contains `range(start: ..., stop: now())`.
- `cutoff = truncate(now(), freshnessWindow)`.
- Split/merge is enabled for CSV requests only.

## Rewrite rules
Given:

```flux
from(bucket: "metrics")
  |> range(start: -1h, stop: now())
```

Gateway generates:
- cached segment: `range(start: -1h, stop: time(v: "<cutoff-rfc3339nano>"))`
- live segment: `range(start: time(v: "<cutoff-rfc3339nano>"), stop: now())`

## Merge behavior (CSV)
- Parse both CSV payloads.
- Keep annotation/meta lines from cached segment.
- Use one header line.
- Combine rows from cached + live.
- Deduplicate exact duplicate rows.
- Sort by `_time` ascending.

## Arrow behavior
- Arrow is cacheable for non-split requests.
- `now()` split/merge for Arrow is currently bypassed; Arrow queries are forwarded as a single upstream request.

## Anti-stampede behavior
- On cache miss, gateway acquires `SETNX` lock (`<key>:lock`).
- Lock holder fetches and populates cache.
- Non-holders briefly wait for cache fill (`cacheLockWaitMilliseconds`) before fallback forwarding.

## Safety guards
- If split rewrite cannot identify expected `range(...)`, fallback to normal cache/proxy path.
- If cached segment fails, return cached segment upstream status/body.
- If live segment fails, return live upstream status/body.
