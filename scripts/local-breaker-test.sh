#!/usr/bin/env sh
set -eu

. ./scripts/lib.sh

ORG="${ORG:-acme}"
GATEWAY_TOKEN="${GATEWAY_TOKEN:-gateway-reader}"
GW_URL="${GW_URL:-http://localhost:8090}"

query="$(cat <<'Q'
from(bucket: "metrics")
  |> range(start: -5m, stop: now())
  |> filter(fn: (r) => r._measurement == "cpu")
Q
)"

restore_influx_a() {
  compose start influx-a >/dev/null 2>&1 || true
}
trap restore_influx_a EXIT INT TERM

wait_stack_ready

echo "Stopping influx-a to induce failures..."
compose stop influx-a >/dev/null

# ensure gateway and remaining target are still up before running assertions
wait_http_ok "http://localhost:8087/health" "$STACK_WAIT_SECONDS" "$STACK_WAIT_INTERVAL"
wait_http_ok "http://localhost:8090/healthz" "$STACK_WAIT_SECONDS" "$STACK_WAIT_INTERVAL"

echo "Issuing queries (watch X-Target* headers):"
for i in 1 2 3 4 5 6; do
  echo "--- request $i ---"
  resp="$(curl -sS -i -XPOST "$GW_URL/api/v2/query?org=$ORG" \
    -H "Authorization: Bearer $GATEWAY_TOKEN" \
    -H 'Content-Type: application/vnd.flux' \
    -H 'Accept: application/csv' \
    --data-binary "$query")"
  code="$(printf '%s' "$resp" | awk 'NR==1{print $2}')"
  printf '%s\n' "$resp" | awk '/^HTTP\// || /^X-Cache:/ || /^X-Target:/ || /^X-Target-Cached:/ || /^X-Target-Live:/'
  if [ "$code" -ne 200 ]; then
    echo "breaker test request failed with status $code" >&2
    exit 1
  fi
  sleep 1
done

echo "Starting influx-a again..."
compose start influx-a >/dev/null
wait_stack_ready

echo "Done"
