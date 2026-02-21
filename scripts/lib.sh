#!/usr/bin/env sh

set -eu

COMPOSE_FILE="${COMPOSE_FILE:-deploy/local/docker-compose.yml}"
STACK_WAIT_SECONDS="${STACK_WAIT_SECONDS:-180}"
STACK_WAIT_INTERVAL="${STACK_WAIT_INTERVAL:-2}"
CONTAINER_RUNTIME="${CONTAINER_RUNTIME:-podman}"

if [ "$CONTAINER_RUNTIME" = "docker" ]; then
  CONTAINER_COMPOSE="docker compose"
else
  CONTAINER_COMPOSE="podman compose"
fi

compose() {
  $CONTAINER_COMPOSE -f "$COMPOSE_FILE" "$@"
}

require_cmd() {
  cmd="$1"
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "missing required command: $cmd" >&2
    exit 1
  fi
}

retry() {
  attempts="$1"
  delay="$2"
  shift 2

  i=1
  while :; do
    if "$@"; then
      return 0
    fi
    if [ "$i" -ge "$attempts" ]; then
      return 1
    fi
    sleep "$delay"
    i=$((i + 1))
  done
}

wait_http_ok() {
  url="$1"
  timeout_s="$2"
  interval_s="$3"

  start_ts="$(date +%s)"
  while :; do
    code="$(curl -sS -o /dev/null -w '%{http_code}' "$url" || true)"
    if [ "$code" -ge 200 ] && [ "$code" -lt 300 ]; then
      return 0
    fi

    now_ts="$(date +%s)"
    if [ $((now_ts - start_ts)) -ge "$timeout_s" ]; then
      echo "timeout waiting for $url (last_code=$code)" >&2
      return 1
    fi
    sleep "$interval_s"
  done
}

ensure_stack_up() {
  rebuild="${1:-0}"
  if [ "$rebuild" = "1" ]; then
    compose up -d --build
  else
    compose up -d
  fi
}

wait_stack_ready() {
  wait_http_ok "http://localhost:8086/health" "$STACK_WAIT_SECONDS" "$STACK_WAIT_INTERVAL"
  wait_http_ok "http://localhost:8087/health" "$STACK_WAIT_SECONDS" "$STACK_WAIT_INTERVAL"
  wait_http_ok "http://localhost:9273/metrics" "$STACK_WAIT_SECONDS" "$STACK_WAIT_INTERVAL"
  wait_http_ok "http://localhost:8090/healthz" "$STACK_WAIT_SECONDS" "$STACK_WAIT_INTERVAL"
}

bootstrap_stack() {
  rebuild="${1:-0}"
  ensure_stack_up "$rebuild"
  wait_stack_ready
}
