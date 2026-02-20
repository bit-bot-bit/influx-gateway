#!/usr/bin/env sh
set -eu

. ./scripts/lib.sh

ORG="${ORG:-acme}"
GATEWAY_TOKEN="${GATEWAY_TOKEN:-gateway-reader}"
GW_URL="${GW_URL:-http://localhost:8090}"
PARALLEL="${PARALLEL:-12}"

query="$(cat <<'Q'
from(bucket: "metrics")
  |> range(start: -5m, stop: now())
  |> filter(fn: (r) => r._measurement == "cpu")
Q
)"

tmp_file="$(mktemp)"
trap 'rm -f "$tmp_file"' EXIT
printf '%s' "$query" > "$tmp_file"

wait_stack_ready

# Fire a burst of concurrent identical requests.
seq 1 "$PARALLEL" | xargs -I{} -P "$PARALLEL" sh -c '
  curl -sS -i -XPOST "'$GW_URL'/api/v2/query?org='$ORG'" \
    -H "Authorization: Bearer '$GATEWAY_TOKEN'" \
    -H "Content-Type: application/vnd.flux" \
    -H "Accept: application/csv" \
    --data-binary @"$1" | awk "/^HTTP\// || /^X-Cache:/"
' sh "$tmp_file"
