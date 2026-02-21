CONTAINER_RUNTIME ?= podman
export CONTAINER_RUNTIME

ifeq ($(CONTAINER_RUNTIME),docker)
	COMPOSE_CMD := docker compose
else
	COMPOSE_CMD := podman compose
endif

.PHONY: up down seed test stampede breaker load query-suite latency perf-suite large-suite logs

up:
	$(COMPOSE_CMD) -f deploy/local/docker-compose.yml up -d --build

down:
	$(COMPOSE_CMD) -f deploy/local/docker-compose.yml down -v

seed:
	./scripts/local-seed.sh

test:
	./scripts/local-test.sh

stampede:
	./scripts/local-stampede-test.sh

breaker:
	./scripts/local-breaker-test.sh

load:
	./scripts/load-4gb-each.sh

query-suite:
	./scripts/query-suite.sh

latency:
	./scripts/latency-bench.sh

perf-suite:
	./scripts/perf-suite.sh

large-suite:
	./scripts/large-suite.sh

logs:
	$(COMPOSE_CMD) -f deploy/local/docker-compose.yml logs -f gateway
