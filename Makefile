SHELL := /bin/bash
COMPOSE := docker compose -f deploy/dev/docker-compose.yml

.PHONY: dev dev-down dev-logs test test-db test-web bench fmt

## dev: one-click single-machine environment (Postgres + pgvector, NATS, API, worker, console)
dev:
	$(COMPOSE) up --build

dev-down:
	$(COMPOSE) down -v

dev-logs:
	$(COMPOSE) logs -f

## test-db: integration tests against a disposable pgvector container
test-db:
	docker run --rm -d --name mengpo-pgtest -e POSTGRES_USER=test -e POSTGRES_PASSWORD=test -e POSTGRES_DB=test -p 55432:5432 m.daocloud.io/docker.io/pgvector/pgvector:pg16
	@sleep 4
	MEMORY_TEST_DATABASE_URL='postgres://test:test@127.0.0.1:55432/test?sslmode=disable' go test ./... -count=1; status=$$?; docker stop mengpo-pgtest >/dev/null; exit $$status

test:
	go test ./...

test-web:
	cd web && npm test

bench:
	go test ./internal/application/recall/... -run '^$$' -bench . -benchmem

fmt:
	gofmt -w cmd internal db
	cd web && npm run build