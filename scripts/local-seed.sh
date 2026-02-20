#!/usr/bin/env sh
set -eu

. ./scripts/lib.sh

write_points() {
  ingest_url="${INGEST_URL:-http://localhost:8186/api/v2/write}"

  now_ns="$(date +%s%N)"
  old_ns="$((now_ns - 120000000000))"
  recent_ns="$((now_ns - 5000000000))"

  curl -sS -XPOST "${ingest_url}?org=${ORG:-acme}&bucket=${BUCKET:-metrics}&precision=ns" \
    -H "Authorization: Token ${TOKEN:-admin-token}" \
    -H "Content-Type: text/plain; charset=utf-8" \
    --data-binary "cpu,host=edge usage=0.42 $old_ns
cpu,host=edge usage=0.99 $recent_ns
cpu,host=edge usage=1.02 $now_ns
cpu,profile=viz,host=edge-a,region=us-east,service=api,tenant=tenant-a usage_user=38.2,usage_sys=20.1 $old_ns
cpu,profile=viz,host=edge-a,region=us-east,service=api,tenant=tenant-a usage_user=56.7,usage_sys=31.4 $recent_ns
cpu,profile=viz,host=edge-a,region=us-east,service=api,tenant=tenant-a usage_user=48.3,usage_sys=24.2 $now_ns
cpu,profile=stress,host=host-4821,region=us-west,service=svc-114,tenant=tenant-52 usage_user=71.9,usage_sys=33.8 $recent_ns
cpu,profile=stress,host=host-9982,region=eu-west,service=svc-221,tenant=tenant-77 usage_user=63.4,usage_sys=28.1 $now_ns
http_requests,profile=viz,host=edge-a,region=us-east,service=api,tenant=tenant-a,endpoint=/api/search,method=GET,status=200 latency_ms=132.4,bytes_out=14200i,retry=0i $old_ns
http_requests,profile=viz,host=edge-a,region=us-east,service=api,tenant=tenant-a,endpoint=/api/search,method=GET,status=200 latency_ms=148.9,bytes_out=12100i,retry=1i $recent_ns
http_requests,profile=viz,host=edge-a,region=us-east,service=api,tenant=tenant-a,endpoint=/api/search,method=GET,status=200 latency_ms=118.7,bytes_out=16300i,retry=0i $now_ns
http_requests,profile=stress,host=host-7711,region=us-west,service=svc-902,tenant=tenant-191,endpoint=/api/orders,method=POST,status=500 latency_ms=842.1,bytes_out=9200i,retry=2i $recent_ns
http_requests,profile=stress,host=host-1142,region=ap-south,service=svc-411,tenant=tenant-817,endpoint=/api/report,method=GET,status=429 latency_ms=620.7,bytes_out=6700i,retry=1i $now_ns
security_events,profile=viz,host=edge-a,region=us-east,service=auth,tenant=tenant-a,severity=medium attempts=2i,blocked=false,score=41.8 $old_ns
security_events,profile=viz,host=edge-a,region=us-east,service=auth,tenant=tenant-a,severity=high attempts=3i,blocked=true,score=76.3 $recent_ns
security_events,profile=viz,host=edge-a,region=us-east,service=auth,tenant=tenant-a,severity=low attempts=1i,blocked=false,score=35.5 $now_ns
security_events,profile=stress,host=host-9172,region=eu-west,service=svc-555,tenant=tenant-444,severity=critical attempts=7i,blocked=true,score=93.2 $recent_ns
security_events,profile=stress,host=host-1288,region=us-east,service=svc-088,tenant=tenant-633,severity=high attempts=4i,blocked=false,score=81.4 $now_ns" >/dev/null
}

wait_stack_ready
retry 3 2 write_points

echo "Seeded via telegraf ingest"
