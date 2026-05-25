.PHONY: help run build test test-race fmt vet tidy clean \
	db-up db-down db-logs db-psql \
	kc-up kc-down kc-logs deps-up deps-down \
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
db-up: ## Start Postgres only and wait until it is accepting connections
	$(COMPOSE) up -d postgres
	@echo "waiting for postgres to be healthy..."
	@until [ "$$($(COMPOSE) ps -q postgres | xargs docker inspect -f '{{.State.Health.Status}}' 2>/dev/null)" = "healthy" ]; do \
		sleep 1; printf "."; \
	done; echo " ready."

db-down: ## Stop Postgres only (data preserved in the named volume)
	$(COMPOSE) stop postgres

db-logs: ## Tail Postgres logs
	$(COMPOSE) logs -f postgres

db-psql: ## Open a psql shell into the running database
	docker exec -it gotask-postgres psql "$(DATABASE_URL)"

# ── Keycloak (docker) ────────────────────────────────────────────────────
kc-up: ## Start Keycloak and wait until the realm's OIDC discovery is live
	$(COMPOSE) up -d keycloak
	@echo "waiting for keycloak realm to be importable & discoverable..."
	@until [ "$$($(COMPOSE) ps -q keycloak | xargs docker inspect -f '{{.State.Health.Status}}' 2>/dev/null)" = "healthy" ]; do \
		sleep 2; printf "."; \
	done; echo " ready."

kc-down: ## Stop Keycloak only
	$(COMPOSE) stop keycloak

kc-logs: ## Tail Keycloak logs
	$(COMPOSE) logs -f keycloak

# ── All dependencies together ────────────────────────────────────────────
deps-up: ## Start Postgres + Keycloak and wait until BOTH are healthy
	$(COMPOSE) up -d
	@echo "waiting for postgres..."
	@i=0; until [ "$$($(COMPOSE) ps -q postgres | xargs docker inspect -f '{{.State.Health.Status}}' 2>/dev/null)" = "healthy" ] || [ $$i -ge 60 ]; do sleep 1; i=$$((i+1)); printf "."; done; \
		[ $$i -lt 60 ] && echo " pg ready." || (echo " FAILED — postgres not healthy in 60s. Run: $(COMPOSE) logs postgres"; exit 1)
	@echo "waiting for keycloak (realm import + discovery, ~30-60s on first run)..."
	@i=0; until [ "$$($(COMPOSE) ps -q keycloak | xargs docker inspect -f '{{.State.Health.Status}}' 2>/dev/null)" = "healthy" ] || [ $$i -ge 120 ]; do sleep 2; i=$$((i+1)); printf "."; done; \
		[ $$i -lt 120 ] && echo " kc ready." || (echo " FAILED — keycloak not healthy in 240s. Run: $(COMPOSE) logs keycloak"; exit 1)

deps-down: ## Stop Postgres + Keycloak (Postgres data preserved). Use `$(COMPOSE) down -v` to wipe.
	$(COMPOSE) down

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

# ── Phase 10: testing, Docker, CI ───────────────────────────────────
#
# Integration tests require Docker (testcontainers-go spins up real
# Postgres). They live behind the `integration` build tag so they
# don't run with the normal `make test`. Use `make integration-test`
# explicitly when you want them.
#
# The Docker build is the multi-stage production-grade image; the
# `make ci` target runs every check CI runs, locally, in order.

integration-test: ## Run integration tests against real Postgres (requires Docker)
	go test -tags=integration -count=1 ./...

docker-build: ## Build the production Docker image (multi-stage; ~20MB final)
	docker build -t gotask:dev .

docker-run: ## Run the built image (expects .env in the project root)
	docker run --rm -p 8080:8080 --env-file .env gotask:dev

# `make ci` mirrors the GitHub Actions pipeline so you can run the
# same checks locally before pushing. Failing here means CI will fail
# too; passing here means the change is likely (not certainly) green.
ci: ## Run the same checks CI runs, in order
	go vet ./...
	go test -race -count=1 ./...
	go test -tags=integration -count=1 ./...
	docker build -t gotask:ci .

# staticcheck is the de facto Go linter. We don't bundle it as a hard
# `make ci` step because it requires a separate install; instead this
# is the convenience target a developer can run before pushing.
staticcheck: ## Run the staticcheck linter (install first with: go install honnef.co/go/tools/cmd/staticcheck@latest)
	staticcheck ./...
