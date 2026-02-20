#!/usr/bin/env sh
set -eu

ORG="${ORG:-acme}"
BUCKET="${BUCKET:-metrics}"
TOKEN="${TOKEN:-admin-token}"
INTERVAL_SECONDS="${INTERVAL_SECONDS:-2}"

TELEGRAF_INGEST_URL="${TELEGRAF_INGEST_URL:-http://telegraf:8186/api/v2/write}"

post_points() {
  ingest_url="$1"
  payload="$2"
  curl -sS -m 5 -XPOST "${ingest_url}?org=${ORG}&bucket=${BUCKET}&precision=s" \
    -H "Authorization: Token ${TOKEN}" \
    -H "Content-Type: text/plain; charset=utf-8" \
    --data-binary "${payload}" >/dev/null
}

i=0
while :; do
  ts="$(date +%s)"

  cpu_user=$((35 + (i % 30)))
  cpu_sys=$((18 + (i % 20)))
  latency_ms=$((90 + (i % 40) * 3))
  req_bytes=$((8000 + (i % 20) * 220))
  sec_score=$((40 + (i % 45)))
  blocked="false"
  if [ $((i % 11)) -eq 0 ]; then
    blocked="true"
  fi

  payload="$(cat <<EOF
cpu,profile=viz,source=ministream,host=edge-live,region=us-east,service=api,tenant=tenant-a usage_user=${cpu_user}.0,usage_sys=${cpu_sys}.0 ${ts}
http_requests,profile=viz,source=ministream,host=edge-live,region=us-east,service=api,tenant=tenant-a,endpoint=/api/search,method=GET,status=200 latency_ms=${latency_ms}.0,bytes_out=${req_bytes}i,retry=0i ${ts}
security_events,profile=viz,source=ministream,host=edge-live,region=us-east,service=auth,tenant=tenant-a,severity=medium attempts=2i,blocked=${blocked},score=${sec_score}.0 ${ts}
EOF
)"

  if ! post_points "${TELEGRAF_INGEST_URL}" "${payload}"; then
    echo "ministream write failed target=telegraf ts=${ts}" >&2
  fi

  i=$((i + 1))
  sleep "${INTERVAL_SECONDS}"
done
