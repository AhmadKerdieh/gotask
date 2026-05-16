.PHONY: help run build test test-race fmt vet tidy clean \
	db-up db-down db-logs db-psql \
	migrate-up migrate-down migrate-create migrate-version migrate-force

# Default target — `make` with no args prints help.
help: ## Show available targets
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-16s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# ── Config ───────────────────────────────────────────────────────────────
# DATABASE_URL is read from the environment if set, otherwise this default
# matches docker/docker-compose.yml. The migrate CLI and `make db-psql`
# both use it.
DATABASE_URL ?= postgres://gotask:gotask@localhost:5432/gotask?sslmode=disable
COMPOSE := docker compose -f docker/docker-compose.yml
MIGRATIONS_DIR := migrations

# ── App ──────────────────────────────────────────────────────────────────
run: ## Run the API server with live env from .env
	go run ./cmd/api

build: ## Build the API binary into ./bin/api
	@mkdir -p bin
	go build -o bin/api ./cmd/api

test: ## Run unit tests
	go test ./... -count=1

test-race: ## Run tests with the race detector
	go test ./... -count=1 -race -cover

fmt: ## Format all Go source
	go fmt ./...

vet: ## Run go vet
	go vet ./...

tidy: ## Tidy go.mod and go.sum
	go mod tidy

clean: ## Remove build artifacts
	rm -rf bin coverage.html coverage.txt

# ── Database (docker) ────────────────────────────────────────────────────
db-up: ## Start Postgres and wait until it is accepting connections
	$(COMPOSE) up -d
	@echo "waiting for postgres to be healthy..."
	@until [ "$$($(COMPOSE) ps -q postgres | xargs docker inspect -f '{{.State.Health.Status}}' 2>/dev/null)" = "healthy" ]; do \
		sleep 1; printf "."; \
	done; echo " ready."

db-down: ## Stop Postgres (data is preserved in the named volume)
	$(COMPOSE) down

db-logs: ## Tail Postgres logs
	$(COMPOSE) logs -f postgres

db-psql: ## Open a psql shell into the running database
	docker exec -it gotask-postgres psql "$(DATABASE_URL)"

# ── Migrations ───────────────────────────────────────────────────────────
# Migrations are an EXPLICIT operational step, never run automatically by
# the app on startup. This is the production-correct separation: a
# starting process must not silently mutate the schema, because during a
# rolling deploy two instances would race into a half-migrated state.
#
# These targets shell out to the `migrate` CLI:
#   go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
# (the -tags 'postgres' build tag is required or the postgres driver is
#  not compiled in and you get a confusing "unknown driver" error.)

migrate-up: ## Apply all up migrations
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" up

migrate-down: ## Roll back the most recent migration (one step)
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" down 1

migrate-version: ## Print the current migration version
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" version

# Usage: make migrate-create name=add_labels_table
# Creates a matched .up.sql / .down.sql pair with the next sequence number.
migrate-create: ## Scaffold a new migration (name=...)
	@test -n "$(name)" || (echo "usage: make migrate-create name=description"; exit 1)
	migrate create -ext sql -dir $(MIGRATIONS_DIR) -seq $(name)

# Recovery only. If a migration fails partway, the schema_migrations table
# is left "dirty" and migrate refuses to proceed until you assert what
# version the schema is actually at. This is intentionally awkward — it
# forces a human to look. Usage: make migrate-force version=1
migrate-force: ## Clear a dirty migration state (version=N) — recovery only
	@test -n "$(version)" || (echo "usage: make migrate-force version=N"; exit 1)
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" force $(version)
