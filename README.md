# gotask

A minimal Jira-like task tracker, built from scratch across a 10-phase course
in production-grade Go.

## Phase 0 — what's wired up

- Project layout matching the target structure.
- `cmd/api/main.go`: HTTP server with graceful shutdown.
- `internal/config`: Viper-backed loader (`.env` + env vars, with defaults
  and validation).
- `pkg/logger`: `log/slog` with text output in dev, JSON in prod.
- `pkg/response`: a single JSON envelope (`OK`, `Error`, `ValidationError`).
- `static/index.html`: a diagnostic page that pings `GET /health`.
- `Makefile`, `.env.example`, `.gitignore`, `workflow.yaml` placeholder.
- Stubs and `.gitkeep` files for every directory that fills up in later
  phases (database, repository, service, handler, middleware, validator,
  domain, migrations, mocks).

Everything required for later phases is in place — directory-wise and
dependency-wise — without anticipating their implementation.

## Phase 1 — HTTP layer

- **Router**: `github.com/go-chi/chi/v5`. Stdlib-compatible — every chi
  handler is an `http.Handler`.
- **Middleware chain** (order matters, see `internal/server/server.go`):
  ```
  RequestID  →  AccessLog  →  Recover  →  CORS  →  Timeout  →  routes
  ```
  - `RequestID`: accepts `X-Request-ID` or generates a UUIDv4. Echoed in
    response header and stored in `r.Context()`.
  - `AccessLog`: wraps the response writer to capture status, derives a
    request-scoped `*slog.Logger` (with `request_id` attached), logs one
    INFO line per request — method, path, status, duration, remote IP,
    user agent.
  - `Recover`: catches panics, logs them with a stack trace and the
    request ID, writes a 500 response in our envelope shape.
  - `CORS`: strict allowlist driven by `CORS_ALLOWED_ORIGINS` env var. No
    wildcard support — by design.
  - `Timeout`: sets a deadline on `r.Context()`. Handlers must observe
    `ctx.Done()`. See `handler/debug.go` for the pattern.
- **Typed errors** (`internal/apperror`): one `Error` type with a `Kind`
  enum, mapped to HTTP status codes by `apperror.HTTPStatus(kind)`. The
  single translation point is `Handler.respondError` — handlers never
  call `response.Error` directly.
- **API versioning**: routes now live under `/api/v1/`. Static frontend
  is served at `/`.
- **Custom 404 / 405**: chi's defaults are plain text; we override both
  to keep the JSON envelope contract.
- **Debug endpoints** (development only): `/api/v1/debug/panic`,
  `/api/v1/debug/slow?ms=20000`, `/api/v1/debug/error/{kind}`. The
  frontend has a button for each.

### New env vars

```
CORS_ALLOWED_ORIGINS=http://localhost:8080,http://localhost:5173
REQUEST_TIMEOUT=15s
```

### What the frontend can do now

- Live request log with status colour-coding and request ID per row.
- Buttons that hit each debug endpoint so you can watch the middleware
  in action.
- Raw response viewer with JSON syntax highlighting.

## Run it

```bash
# 1. Pull deps (Viper is the only dependency right now).
go mod tidy

# 2. Copy the example env file (or just edit .env directly).
cp .env.example .env

# 3. Start the server.
make run        # or: go run ./cmd/api

# 4. Open the diagnostic page.
open http://localhost:8080
```

You should see:

- Terminal log line: `level=INFO msg="server starting" addr=:8080 env=development …`
- Browser page with a green **healthy** pill, latency, and the raw JSON
  response from `/health`.

## Project layout

```
gotask/
├── cmd/api/main.go              # entry point
├── internal/
│   ├── config/
│   │   ├── config.go            # Viper config loader
│   │   └── workflow.go          # workflow.yaml loader (stub — phase 2)
│   ├── database/database.go     # pgxpool factory     (stub — phase 3)
│   ├── domain/                  # domain models       (phase 2)
│   ├── handler/                 # HTTP handlers       (phase 5)
│   ├── middleware/keycloak.go   # OIDC middleware     (stub — phase 6)
│   ├── repository/              # data access         (phase 3)
│   ├── service/                 # business logic      (phase 4)
│   │   └── mocks/               # generated mocks     (phase 4)
│   └── validator/validator.go   # validator wrapper   (stub — phase 2)
├── migrations/                  # SQL migrations      (phase 3)
├── pkg/
│   ├── logger/logger.go         # slog wrapper
│   └── response/response.go     # JSON envelope
├── static/index.html            # diagnostic frontend
├── docker/docker-compose.yml    # services            (phases 3, 6)
├── workflow.yaml                # task vocabulary
├── .env / .env.example
├── go.mod
└── Makefile
```

## Make targets

```
make run          # go run ./cmd/api
make build        # → ./bin/api
make test         # go test ./... -count=1
make test-race    # go test ./... -count=1 -race -cover
make fmt          # go fmt ./...
make vet          # go vet ./...
make tidy         # go mod tidy
make clean        # rm -rf bin coverage.*
```
