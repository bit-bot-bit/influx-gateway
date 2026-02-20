#!/usr/bin/env sh
set -eu

. ./scripts/lib.sh

GW_URL="${GW_URL:-http://localhost:8090}"
ORG="${ORG:-acme}"
TOKEN="${GATEWAY_TOKEN:-gateway-reader}"
QUERY_PROFILE="${QUERY_PROFILE:-viz}"   # viz|stress|any
QUERY_WINDOW="${QUERY_WINDOW:-30m}"
QUERY_LONG_WINDOW="${QUERY_LONG_WINDOW:-60m}"
QUERY_TIMEOUT_SECONDS="${QUERY_TIMEOUT_SECONDS:-75}"
QUERY_AGG="${QUERY_AGG:-1m}"

if [ "$QUERY_PROFILE" = "any" ]; then
  PROFILE_FILTER="true"
else
  PROFILE_FILTER="r.profile == \"$QUERY_PROFILE\""
fi

run_query() {
  name="$1"
  query="$2"

  run_once() {
    phase="$1"
    out="$(curl -sS -o /tmp/influx_bouncer_query_body.txt -w '%{http_code} %{time_total}' \
      --max-time "$QUERY_TIMEOUT_SECONDS" \
      -XPOST "$GW_URL/api/v2/query?org=$ORG" \
      -H "Authorization: Bearer $TOKEN" \
      -H 'Content-Type: application/vnd.flux' \
      -H 'Accept: application/csv' \
      --data-binary "$query")"
    code="$(printf '%s' "$out" | awk '{print $1}')"
    total="$(printf '%s' "$out" | awk '{print $2}')"
    echo "=== $name ($phase) ==="
    echo "status=$code total=${total}s"
    if [ "$code" -lt 200 ] || [ "$code" -ge 300 ]; then
      echo "query failed: $name ($phase)"
      sed -n '1,80p' /tmp/influx_bouncer_query_body.txt
      exit 1
    fi
  }

  run_once "cold"
  run_once "warm"
}

wait_stack_ready

q1="from(bucket: \"metrics\") |> range(start: -$QUERY_WINDOW, stop: now()) |> filter(fn: (r) => r._measurement == \"http_requests\" and r._field == \"latency_ms\" and ($PROFILE_FILTER)) |> aggregateWindow(every: $QUERY_AGG, fn: mean, createEmpty: false)"
q2="from(bucket: \"metrics\") |> range(start: -$QUERY_WINDOW, stop: now()) |> filter(fn: (r) => r._measurement == \"cpu\" and (r._field == \"usage_user\" or r._field == \"usage_sys\") and ($PROFILE_FILTER)) |> aggregateWindow(every: $QUERY_AGG, fn: mean, createEmpty: false) |> pivot(rowKey:[\"_time\"], columnKey:[\"_field\"], valueColumn:\"_value\")"
q3="from(bucket: \"metrics\") |> range(start: -$QUERY_LONG_WINDOW, stop: now()) |> filter(fn: (r) => r._measurement == \"orders\" and r._field == \"total\" and ($PROFILE_FILTER)) |> group(columns:[\"tenant\"]) |> top(n: 5, columns:[\"_value\"])"
q4="from(bucket: \"metrics\") |> range(start: -$QUERY_WINDOW, stop: now()) |> filter(fn: (r) => r._measurement == \"security_events\" and r._field == \"score\" and ($PROFILE_FILTER)) |> window(every: $QUERY_AGG) |> quantile(q: 0.99, method: \"estimate_tdigest\") |> duplicate(column: \"_stop\", as: \"_time\") |> window(every: inf)"
q5="from(bucket: \"metrics\") |> range(start: -$QUERY_WINDOW, stop: now()) |> filter(fn: (r) => r._measurement == \"http_requests\" and ($PROFILE_FILTER) and r.endpoint =~ /api\\/(search|orders|report)/) |> group(columns:[\"endpoint\"]) |> count()"
q6="from(bucket: \"metrics\") |> range(start: -$QUERY_WINDOW, stop: now()) |> filter(fn: (r) => (r._measurement == \"http_requests\" and r._field == \"latency_ms\" and ($PROFILE_FILTER)) or (r._measurement == \"cpu\" and r._field == \"usage_user\" and ($PROFILE_FILTER))) |> aggregateWindow(every: $QUERY_AGG, fn: mean, createEmpty: false) |> pivot(rowKey:[\"_time\",\"host\"], columnKey:[\"_field\"], valueColumn:\"_value\")"

if [ "$QUERY_PROFILE" = "stress" ]; then
  # Stress profile has very high host/session cardinality. Keep "complex" queries realistic
  # but avoid pathological host-pivot explosions that frequently exceed timeout on local rigs.
  q2="from(bucket: \"metrics\") |> range(start: -$QUERY_WINDOW, stop: now()) |> filter(fn: (r) => r._measurement == \"cpu\" and (r._field == \"usage_user\" or r._field == \"usage_sys\") and ($PROFILE_FILTER)) |> group(columns:[\"service\",\"_field\"]) |> aggregateWindow(every: $QUERY_AGG, fn: mean, createEmpty: false)"
  q3="from(bucket: \"metrics\") |> range(start: -$QUERY_LONG_WINDOW, stop: now()) |> filter(fn: (r) => r._measurement == \"orders\" and r._field == \"total\" and ($PROFILE_FILTER)) |> aggregateWindow(every: $QUERY_AGG, fn: mean, createEmpty: false) |> group(columns:[\"tenant\"]) |> top(n: 5, columns:[\"_value\"])"
  q6="from(bucket: \"metrics\") |> range(start: -$QUERY_WINDOW, stop: now()) |> filter(fn: (r) => ((r._measurement == \"http_requests\" and r._field == \"latency_ms\") or (r._measurement == \"cpu\" and r._field == \"usage_user\")) and ($PROFILE_FILTER)) |> keep(columns:[\"_time\",\"_measurement\",\"_field\",\"_value\",\"service\"]) |> aggregateWindow(every: $QUERY_AGG, fn: mean, createEmpty: false) |> pivot(rowKey:[\"_time\",\"service\"], columnKey:[\"_field\"], valueColumn:\"_value\")"
fi

run_query "http mean latency window" "$q1"
run_query "cpu pivot user+sys" "$q2"
run_query "top order totals by tenant" "$q3"
run_query "security p99" "$q4"
run_query "endpoint regex counts" "$q5"
run_query "join latency and cpu" "$q6"
