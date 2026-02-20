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

run_query_to_file() {
  out_file="$1"
  curl -i -sS -XPOST "$GW_URL/api/v2/query?org=$ORG" \
    -H "Authorization: Bearer $GATEWAY_TOKEN" \
    -H 'Content-Type: application/vnd.flux' \
    -H 'Accept: application/csv' \
    --data-binary "$query" >"$out_file"
}

run_query_with_retry() {
  out_file="$1"
  attempts=3
  i=1
  while :; do
    if run_query_to_file "$out_file"; then
      code="$(awk 'NR==1{print $2}' "$out_file")"
      if [ "$code" = "200" ]; then
        return 0
      fi
    fi

    if [ "$i" -ge "$attempts" ]; then
      return 1
    fi
    i=$((i + 1))
    sleep 2
  done
}

wait_stack_ready

resp1="$(mktemp)"
resp2="$(mktemp)"
trap 'rm -f "$resp1" "$resp2"' EXIT

echo "First request (expect MISS + split):"
if ! run_query_with_retry "$resp1"; then
  echo "first request failed" >&2
  sed -n '1,40p' "$resp1" >&2
  exit 1
fi
sed -n '1,20p' "$resp1"

printf '\nSecond request (expect HIT + split):\n'
if ! run_query_with_retry "$resp2"; then
  echo "second request failed" >&2
  sed -n '1,40p' "$resp2" >&2
  exit 1
fi
sed -n '1,20p' "$resp2"
