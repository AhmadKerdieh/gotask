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

## Phase 2 — Domain models & validation

- **Domain types** (`internal/domain/`): `Task`, `Project`, `User`,
  `Comment`. Pure data — no JSON tags, no DB tags, no methods that do
  I/O. The same struct flows between every layer without coupling them.
- **Typed status & priority**: `domain.Status` and `domain.Priority` are
  string aliases with Go constants (`StatusOpen`, `PriorityHigh`, …).
  The constants give IDE autocomplete and stop you passing a Priority
  where a Status is expected. **Legality is enforced at runtime**
  against `workflow.yaml`, not by the compiler.
- **Workflow loader** (`internal/config/workflow.go`): parses
  `workflow.yaml` and runs nine internal-consistency checks
  (terminal_statuses ⊆ statuses, every transition source/destination is
  a known status, defaults exist, no self-transitions, terminal
  statuses have no outgoing transitions, …). Exposes `IsStatus`,
  `IsPriority`, `IsTerminal`, `CanTransition`, `AllowedTransitions`.
- **Validator** (`internal/validator/validator.go`): wraps
  `go-playground/validator/v10`. Reads JSON tags via
  `RegisterTagNameFunc` so errors come back keyed by `title` rather
  than `Title`. Registers two custom rules (`status`, `priority`) that
  consult the loaded workflow. Returns a flat `map[string]string`
  that drops directly into `apperror.Validation()` from Phase 1 — no
  glue code in handlers.
- **DTO package** (`internal/handler/dto/`): created as a skeleton.
  Phase 5 fills it with `CreateTaskRequest`, `TaskResponse`, etc.
  Today's commit just establishes the boundary: domain types stay
  free of JSON/validate tags; wire-shape types live here.
- **New routes**:
  - `GET /api/v1/workflow` — public, read-only. Returns the loaded
    workflow as JSON so the frontend can render dropdowns from a
    single source of truth.
  - `POST /api/v1/debug/validate` (dev only) — validates a JSON body
    shaped like the future `CreateTaskRequest`. Frontend has a
    playground for it.

### New env var

```
WORKFLOW_PATH=./workflow.yaml
```

### workflow.yaml shape

```yaml
statuses:          [open, in_progress, blocked, in_review, done, cancelled]
terminal_statuses: [done, cancelled]
transitions:
  open:        [in_progress, cancelled]
  in_progress: [blocked, in_review, cancelled, open]
  # …
priorities:        [low, medium, high, critical]
default_status:    open
default_priority:  medium
```

Edit this file to change task statuses or transitions; restart to apply.
(Hot reload is intentionally deferred.)

### What the frontend gains

- Workflow viewer card: chips for statuses, terminal markers, priorities,
  defaults, plus a `from → [to, to, …]` table for transitions.
- Validator playground: textarea with five preset payloads
  (valid / missing fields / bad enum / oversized title / unknown field)
  and a field-level error display.

## Phase 3 — Persistence: Postgres, migrations, repositories

- **Driver**: `github.com/jackc/pgx/v5` native (via `pgxpool`), not
  `database/sql`. Trade-off accepted: the repository is coupled to
  Postgres. Justified because we use Postgres-specific features and the
  *interface* (not the driver) is the swap point.
- **Connection pool** (`internal/database/`): explicit `pgxpool` config
  (max/min conns, conn lifetime), a bounded startup `Ping` so an
  unreachable DB fails startup in seconds, and a `Close` that drains the
  pool during graceful shutdown.
- **Migrations** (`migrations/`): `golang-migrate` with plain `.sql`
  files. `000001_init` creates `users`, `projects`, `tasks`, `comments`
  with a shared `updated_at` trigger, FK relationships, and indexes. The
  `down` is the exact tested inverse. Migrations run as an **explicit
  operational step** (`make migrate-up`), never automatically on app
  startup — that separation prevents two instances racing into a
  half-migrated schema during a rolling deploy.
- **Repository layer** (`internal/repository/`): callers depend on the
  `TaskRepository` / `ProjectRepository` *interfaces*, never the
  concrete Postgres types (which are unexported). This is the seam that
  makes the Phase 4 service layer unit-testable in microseconds against
  an in-memory fake.
- **Identity**: the database owns it. `id UUID DEFAULT gen_random_uuid()`;
  inserts use `RETURNING` to read the generated id + timestamps in one
  round-trip. An unpersisted `domain.Task` honestly has `uuid.Nil`.
- **Error translation** (`repository/errors.go`): one function maps
  Postgres SQLSTATE codes to `apperror` — `23505` → Conflict, no rows →
  NotFound, context errors pass through unwrapped so the timeout
  middleware can still produce a 504. Raw pg errors never reach a client.
- **NULL ↔ zero value**: nullable columns (`assignee_id`, `due_date`,
  `edited_at`) map to the domain's zero-value convention
  (`uuid.Nil` / zero `time.Time`). The translation is contained entirely
  in the scan/write helpers.
- **Context finally does real work**: every repository method passes
  `ctx` to pgx. When the Phase 1 timeout fires, pgx aborts the in-flight
  query *server-side*. The whole chain connects: HTTP timeout → context
  cancel → query abort → clean 504.
- **Readiness vs liveness**: new `GET /api/v1/ready` round-trips the DB
  and returns 503 if it's down — distinct from `/health` (liveness).
  Load balancers gate on readiness; orchestrators restart on liveness.
- **New routes** (dev only): `POST /api/v1/debug/db/seed`,
  `GET /api/v1/debug/db/tasks`, `GET /api/v1/debug/db/slow-query`.

### New env vars

```
DATABASE_URL=postgres://gotask:gotask@localhost:5432/gotask?sslmode=disable
DB_MAX_CONNS=10
DB_MIN_CONNS=2
```

`DATABASE_URL` has no default — the app refuses to start without it.

### Bringing up the database

```bash
make db-up         # starts Postgres in docker, waits until healthy
make migrate-up    # applies migrations (requires the migrate CLI — see Make targets)
make run           # start the app
```

### What the frontend gains

- Persistence card: readiness pill, seed (project + task), list tasks,
  and a slow-query button that proves the request context cancels an
  in-flight query server-side (504, not a hung connection).

## Phase 4 — Service layer, dependency injection, unit tests

- **The missing layer.** Phase 3's review exposed a gap: nothing
  enforced business rules — a task could be created with status
  `"banana"`, a `done` task could be illegally reopened. The service
  layer closes it. Rule of thumb for what belongs here: *if gotask were
  a CLI instead of an HTTP API, would this logic still need to exist?*
  If yes, it's a service rule.
- **`TaskService` / `ProjectService`** (`internal/service/`): default
  application (empty status/priority → workflow defaults), value
  legality (status/priority must be in `workflow.yaml` — this is the
  gate that stops `"banana"`), and the centrepiece rule: status
  **transition enforcement** via `workflow.CanTransition`. A terminal
  task is frozen; an illegal jump (`open → done`) is rejected before
  any write.
- **Depends on interfaces, not Postgres.** The service holds
  `repository.TaskRepository` (the Phase 3 interface). Production injects
  the Postgres impl; tests inject an in-memory fake. Identical service
  code both ways — this substitutability is the entire payoff of the
  Phase 3 interface seam.
- **Service-level error vocabulary** (`service/errors.go`):
  `ErrNotFound`, `ErrInvalidTransition`, `ErrConflict`, and a typed
  `ValidationError` carrying a field→message map. The service does
  **not** import `apperror` — it has no concept of HTTP. The handler's
  `respondError` is the single point that maps the service vocabulary →
  apperror → HTTP. Same discipline as the repository's pg-error
  translator.
- **First real unit tests.** `task_service_test.go` runs table-driven
  tests against hand-written in-memory fakes (`fakes_test.go`). No
  database, no Docker — the suite runs in milliseconds. A fake genuinely
  works (stores and retrieves); you assert on outcomes.
- **Fake vs mock, shown side by side.** `task_service_mock_test.go`
  re-verifies one behaviour with a `testify/mock` instead of a fake, to
  make the trade-off concrete: a mock can assert *interactions* a fake
  cannot — here, that an illegal transition is rejected *without the
  repository's `Update` ever being called*. Use a fake when the test
  cares what happened; a mock when it cares that a specific call was (or
  wasn't) made.
- **DI wiring.** The graph grew one layer: repos → services → handlers.
  The dev DB probes were refactored to go through the service, so the
  frontend now exercises the real enforced path. New
  `config.NewWorkflowForTest` lets tests build a workflow without a YAML
  file (runs the same validation as the file loader).

### Running the tests

```bash
make test          # whole suite, no database needed
go test ./internal/service/ -v   # see each business rule pass by name
```

### What the frontend gains

- Transition tester card: paste a task id, pick a target status, and
  watch the service accept a legal move or reject an illegal one (409
  invalid_transition) or an unknown status (422) — a business rule
  rejecting a request, visible from the browser.

## Phase 5 — Tasks REST API & the DTO boundary

- **Real API, debug probes removed (clean cut).** `debug.go`,
  `debug_db.go`, `debug_validate.go` are deleted; their routes are gone.
  The real endpoints supersede them. Handlers no longer hold the raw
  repositories — they go through services exclusively (the readiness
  probe still pings the DB directly, which is legitimate).
- **Endpoints**:
  - `POST /api/v1/projects`, `GET /api/v1/projects`,
    `GET /api/v1/projects/{id}`
  - `POST /api/v1/tasks`, `GET /api/v1/tasks` (filters: `project_id`,
    `status`, `assignee_id`), `GET /api/v1/tasks/{id}`,
    `DELETE /api/v1/tasks/{id}`
  - `PATCH /api/v1/tasks/{id}/status` — status is its own sub-resource
    because it is the one mutation governed by the workflow transition
    rule; isolating it gives that rule a single guarded entry point.
- **The DTO boundary is now physical** (`internal/handler/dto/`):
  `CreateTaskRequest`, `UpdateTaskStatusRequest`, `TaskResponse`,
  `CreateProjectRequest`, `ProjectResponse`. Each handler does
  decode → validate → convert wire→service → call service →
  convert domain→wire → encode. The `reporter_id` / `owner_id`
  divergence is real code: the request DTOs have no such field, so a
  client cannot forge authorship. Until Phase 6 the handler stubs the
  identity to a fixed seeded user — that stub is the visible seam where
  auth slots in.
- **Strict request decoding** (`handler/decode.go`): one shared helper
  enforces `Content-Type: application/json` (415), a 1 MiB body cap
  (413), unknown-field rejection (400), and single-value bodies. Every
  decode failure maps to the standard envelope — no raw Go error reaches
  a client. Same one-translation-point discipline as everywhere else.

### Backlog / hardening (tracked, deferred deliberately)

These are known, intentional deferrals — recorded here so they are
visible decisions, not forgotten gaps:

1. **Integration tests** (deferred to Phase 10). The Phase 4 service
   tests use in-memory fakes — fast, but a fake can never catch a
   malformed SQL string, a drifted column name, or a wrong NULL/zero
   mapping in the repository. `internal/repository/integration_test.go`
   is a skipped, fully-documented placeholder describing exactly what
   Phase 10 will build with `testcontainers-go` (real ephemeral
   Postgres, real migrations, real SQL). It is `t.Skip`-ped so the
   suite stays green while the gap stays honestly marked.
2. **Graceful-shutdown timeout** is hardcoded to 10s in `cmd/api/main.go`
   and is currently *less* than `REQUEST_TIMEOUT` (15s), so a slow
   in-flight request during shutdown is force-killed. Should be derived
   from `RequestTimeout + margin`. Deferred to Phase 9 (production
   hardening), where shutdown-under-load is the topic.
3. **Service↔repository error coupling** uses string matching
   (`mapRepoError`). A small typed error in the repository package
   would be cleaner. Deferred to Phase 9.
4. **Option A: no local users table** (Phase 6 decision, permanent). The
   `users` table was dropped; `reporter_id`/`owner_id` are bare Keycloak
   subjects with no foreign key. Consequence, accepted deliberately:
   `JOIN users` is impossible — any feature that must display a user's
   name/email has to call the Keycloak Admin API, making Keycloak a
   runtime dependency for *reads*, not just auth. This is not a gap to
   fix; it is the chosen architecture, recorded so the trade-off stays
   visible when Phase 7+ wants to show "reporter: Jane Doe".

### What the frontend gains

- A real task board (the probe panels are gone): create projects,
  create tasks (status omitted → service applies the workflow default),
  list/filter tasks, change a task's status through the real
  `PATCH /tasks/{id}/status` and watch the workflow rule reject an
  illegal move with a 409, delete tasks. It only ever calls the real
  API — there are no debug endpoints left to call.

## Phase 6 — Authentication: Keycloak, OIDC & PKCE

The first phase where a mistake is a security hole, not a 500. Every
`/api/v1/projects` and `/api/v1/tasks` route now requires a verified
token; `stubReporterID` is gone.

- **Keycloak** runs in Docker (same compose file as Postgres) as the
  identity provider. The realm — client, PKCE settings, a `demo`/`demo`
  user — is provisioned from a **committed `docker/realm-export.json`**,
  imported on startup. Reproducible, zero manual clicking.
- **Local token verification, not introspection.** The middleware does
  OIDC discovery once at startup, then verifies each token's signature
  locally against Keycloak's cached public keys (`coreos/go-oidc`
  handles JWKS fetch + caching + key-rotation refresh). No Keycloak
  round-trip per request. A token is trusted because the math proves
  Keycloak signed it.
- **The five checks**, none skippable: signature, issuer, **audience**
  (the one people forget — stops a token for another client being
  replayed here), expiry, not-before. `go-oidc`'s verifier enforces all
  five; the code and comments make each explicit.
- **PKCE** (Authorization Code + Proof Key for Code Exchange) in the
  vanilla-JS SPA. No client secret (a secret in JS isn't one). The SPA
  generates a `code_verifier`, sends only `SHA256(verifier)` to
  Keycloak, and proves possession of the verifier at token exchange —
  so an intercepted authorization code is useless to an attacker.
- **Identity through context.** The verified `sub` rides in
  `context.Context` exactly like `request_id` since Phase 1. The Phase 5
  seam (reporter/owner as a parameter, never a wire field) paid off:
  killing the stub was one helper + three call-site edits, zero changes
  to request shapes or service code.
- **401 vs 403 kept precise.** This middleware only ever emits **401**
  ("I don't know who you are"). **403** ("I know you but you may not do
  this") is an *authorization* decision — Phase 7. Conflating them is
  the classic auth mistake; they stay separate phases.
- **Public vs protected.** `/health`, `/ready`, `/api/v1/workflow`, and
  the static page stay open (a load balancer probing `/ready` has no
  token and must not need one). Projects and tasks are wrapped in an
  auth-protected `chi` route group.
- **Schema: Option A (you chose maximum purity).** Migration `000002`
  drops the `users` table and the four foreign keys that referenced it;
  `reporter_id`/`owner_id` become bare UUID = the Keycloak subject. See
  backlog item 4 for the permanent consequence (no `JOIN users`).

### New env vars

```
OIDC_ISSUER=http://localhost:8081/realms/gotask
OIDC_CLIENT_ID=gotask-spa
```

### Bringing up auth

```bash
make deps-up       # starts Postgres AND Keycloak, waits for both healthy
make migrate-up    # applies 000002 (drops users table)
make run           # app does OIDC discovery at startup — Keycloak must be up
```

Keycloak's first start (realm import) takes ~30–60s; `make deps-up`
waits for the realm's OIDC discovery document before returning, so when
it finishes the app's startup discovery will succeed.

### What the frontend gains (Phase 6)

- A **Log in with Keycloak** button driving the full PKCE flow. After
  login, the session card shows who you are; the task you create is
  attributed to your real subject, not a stub. Token is held in memory
  only (not localStorage — XSS-readable). An expired token → next call
  401s → you're bounced back to login. Log out hits Keycloak's
  end-session endpoint.

## Phase 7 — Authorization, roles & audit

The **403** half of the auth story (Phase 6 did the 401 half). This phase
introduces a real authorization layer and an audit log; the Option A
consequence becomes concrete.

- **Three realm roles**: `member` (default — create/own), `manager`
  (can act on others' tasks), `admin` (can list everyone's audit). The
  hierarchy `admin → manager → member` is encoded once, in
  `authz.HasRole`. The realm export grants all three to the `demo` user;
  to exercise the negative paths, create a second user in the Keycloak
  UI with only `member`.
- **A new `internal/authz/` package**: pure authorization predicates —
  no I/O, no HTTP — composable, exhaustively tested. The service calls
  `authz.CanDeleteTask(claims, task)`; it does not open-code rules.
- **The two-layer rule, in code**:
    - **Middleware** for *route-coarse* checks (a planned admin-only
      endpoint would use `RequireRole("admin")`). Middleware never
      loads a resource — it has nothing to load it with.
    - **Service** for *resource-fine* checks. The service loads the
      task, calls the predicate, mutates, audits. Authorization,
      business rules, and audit live in the SAME layer because they
      are the same kind of decision: "given this resource and this
      actor, may this operation proceed?"
- **`UserClaims` moved to the authz package** (with a backwards-compat
  alias in middleware). The architectural reason: the service must
  depend on the claims type, and a middleware import would drag
  `net/http` into the service via transitive dependency — breaking the
  "service has no HTTP knowledge" invariant. Putting Claims in authz
  fixes the layering cleanly.
- **Audit log in Postgres** (`audit_log` table, migration `000003`).
  Recorded by the SERVICE after each authorization-relevant operation
  — **not** by middleware. Middleware sees HTTP, not outcomes; an
  audit row claiming a delete succeeded when it actually failed mid-
  transaction would be worse than no audit. The audit interface is
  injected through `service.Deps`; tests use `audit.NewFake()`.
- **Denied attempts are audited** with `outcome='denied'`. Security
  review wants this as much as successes.
- **The Option A consequence, made concrete** —
  `internal/keycloakadmin/`: a small Admin API client (with TTL cache
  + service-account token management) resolves Keycloak subs to
  names/emails for audit responses. There is no local `users` table to
  JOIN; this is the cost you signed up for in Phase 6, paid here. If
  the admin client isn't configured, the app falls back to
  `keycloakadmin.NoopLookup` and audit responses degrade gracefully to
  subject-only.

### New env vars

```
KEYCLOAK_ADMIN_BASE_URL=http://localhost:8081
KEYCLOAK_REALM=gotask
KEYCLOAK_ADMIN_CLIENT_ID=gotask-backend
KEYCLOAK_ADMIN_CLIENT_SECRET=gotask-backend-secret
```

### Bringing up Phase 7

The realm now defines three roles and a second client (`gotask-backend`)
for the Admin API lookup. Keycloak must re-import the realm:

```bash
# Stop everything AND wipe the Keycloak DB so the realm import re-runs:
docker compose -f docker/docker-compose.yml down -v
make deps-up
make migrate-up    # applies 000003 (audit_log)
make run
```

The `-v` flag wipes both Postgres and Keycloak data. Postgres data loss
in dev is acceptable; Keycloak data loss is *required* because an
already-imported realm is not re-imported on restart, so the new roles
and `gotask-backend` client only appear after a clean Keycloak volume.

### What the frontend gains (Phase 7)

- Role-aware action buttons: a `delete` button on a task you can't
  delete simply doesn't render. The server is still the source of
  truth (it returns 403), but the UI mirrors `authz.CanX` for clarity.
- An **audit panel** showing recent activity. Members and managers see
  their own history; admins get an actor filter input. Actor cells
  show real names when the Admin API lookup is available, subjects
  otherwise — the Option A trade-off visible at a glance.

## Phase 8 — Concurrency: transactions, outbox & graceful shutdown

The Go-specific phase. By the end the app has a real, principled
approach to concurrency: every long-lived goroutine has documented
answers to the four questions (who starts it, who stops it, what
happens to in-flight work at shutdown, what happens on panic), and the
audit pipeline has a durability property no in-process design can give:
**a successful business write implies an audit row exists, atomically**.

You chose **Path A** in the design questions: the durable outbox
pattern with transactions throughout. The trade-off was deliberate:
~200 extra lines and a new table in exchange for atomicity that
survives process crashes, kernel panics, and network cuts mid-write.

- **Transactions at the service layer.** `internal/database/tx.go`
  introduces `Queryer` (an interface satisfied by both `*pgxpool.Pool`
  and `pgx.Tx`) and `WithTx`, a helper that encapsulates
  begin/rollback-on-error/rollback-on-panic/commit correctly. Every
  mutating service method (`Create`, `UpdateStatus`, `Delete` on tasks;
  `Create` on projects) now wraps its work in `WithTx`.
- **Repositories accept a Queryer, not a pool.** Each Postgres
  repository now holds a `Queryer` (default: the pool); new
  `NewTaskRepositoryTx(tx)` / `NewProjectRepositoryTx(tx)` constructors
  return tx-scoped repos that share the same interface. No method
  signatures changed.
- **The audit outbox** (migration `000004_audit_outbox`). The Recorder
  no longer writes to `audit_log` directly; it writes to
  `audit_outbox` using the SAME transaction as the business write.
  Atomicity is enforced by Postgres — both rows live or both die.
- **The drainer goroutine** (`internal/audit/drainer.go`). Background
  worker that polls the outbox, claims rows with
  `FOR UPDATE SKIP LOCKED`, INSERTs to `audit_log` with
  `ON CONFLICT (id) DO NOTHING` (idempotent — duplicate drains are
  no-ops), DELETEs from the outbox. All in one transaction per batch.
  Documented in detail because it's the headline goroutine of the
  phase.
- **errgroup-based lifecycle.** `cmd/api/main.go` is restructured
  around `golang.org/x/sync/errgroup`. The signal handler cancels a
  root context; the drainer observes it via `gctx.Done()`. Shutdown
  is **strictly sequential** (your other Phase 8 choice): HTTP first
  (stops accepting new requests → no new outbox rows), then drain
  remaining outbox, then close the DB pool.
- **`SHUTDOWN_TIMEOUT` resolved.** Phase 5 backlog item 2 ("graceful-
  shutdown timeout 10s < REQUEST_TIMEOUT 15s") is fixed: shutdown is
  configurable, defaults to 30s, and config validation requires it to
  exceed `REQUEST_TIMEOUT`.
- **Audit-as-hard-dependency.** Phase 7's audit-failure-is-best-effort
  policy is reversed under Path A: an audit write failure now rolls
  back the business write. This is what the durability promise costs
  — when audit is degraded, mutations fail with 5xx rather than
  succeeding silently with no audit record.

### New env vars

```
AUDIT_DRAIN_INTERVAL=1s
AUDIT_DRAIN_BATCH_SIZE=100
SHUTDOWN_TIMEOUT=30s
```

### Bringing up Phase 8

```bash
make migrate-up         # applies 000004 (audit_outbox table)
make run                # logs "audit drainer started" alongside server starting
```

You should see new startup lines:

```
msg="audit drainer started" poll_interval=1s batch_size=100
msg="server starting" ...
```

On Ctrl+C or SIGTERM you'll see the three-step sequential shutdown:

```
msg="shutdown signal received" signal=interrupt
msg="step 1/3: stopping HTTP server (draining in-flight requests)"
msg="HTTP server stopped cleanly"
msg="step 2/3: waiting for audit drainer to finish"
msg="audit drainer stopping" reason="context canceled"
msg="audit drainer stopped cleanly"
msg="step 3/3: closing DB pool"
msg="shutdown complete"
```

That ordering is the architectural promise made physical. Each step
prints because each step matters — if shutdown ever stalls in
production, the last log line tells you where.

### Backlog after Phase 8

The Phase 5 backlog is now mostly paid down. Remaining tracked items:

1. ~~Integration tests (deferred to Phase 10).~~ Still pending.
2. ~~Graceful-shutdown timeout hardcoded.~~ **Resolved in Phase 8.**
3. **Service↔repository error coupling** uses string matching
   (`mapRepoError`). Still deferred — Phase 9.
4. **Option A: no local users table** (Phase 6 decision, permanent).
   Not a defect; the trade-off remains explicit.
5. **NEW: token-cache cleanup worker** for `keycloakadmin.Client`.
   The cache uses TTL-checked reads (expired entries are invisible),
   but expired map entries are never evicted, so memory grows slowly
   over months. Add a small ticker-driven sweeper. Deferred to
   Phase 9 because it's a memory-hygiene polish, not a correctness
   bug.
6. **NEW: outbox observability.** A growing `audit_outbox` row count
   means the drainer is falling behind. Expose this as a metric in
   Phase 9 (observability) so operators can alarm on it.

## Phase 9 — Observability & hardening

The production-polish phase. Phase 8 made the system *correct* under
concurrency; Phase 9 makes it **operable** — answerable to operators
who weren't around when it was built.

You chose **Path C** (Prometheus-native) in the design questions. The
`/metrics` endpoint shows real, human-readable output on first curl —
no Collector, no backend, no SigNoz stack to bring up. Operationally
smaller than the OpenTelemetry path; pedagogically the lessons (four
golden signals, cardinality discipline, histogram vs counter vs gauge)
are identical regardless of which protocol you'd choose long-term.

- **Prometheus metrics on `/metrics`.** New `internal/metrics` package
  declares all collectors in one place — the discipline of
  centralising naming, label sets, and cardinality decisions. Includes
  the four golden signals (latency, traffic, errors, saturation), the
  audit-events counter, the **outbox-pending gauge** (the alerting
  metric Phase 8 named), drainer iteration histogram, DB pool stats,
  and Go runtime metrics for free via the client library.
- **HTTP metrics middleware.** Uses `chi.RouteContext().RoutePattern()`
  for low-cardinality route labels — `/api/v1/tasks/{id}`, not
  `/api/v1/tasks/abc-123`. The single thing that distinguishes
  "production-ready" from "kills Prometheus in a week".
- **Request-id propagation through the outbox.** The `request_id`
  column added in Phase 7 finally gets populated. Service stamps it
  onto `audit.Event` via the new `internal/reqctx` package
  (deliberately small, no HTTP, importable from the service without
  breaking layering). The drainer logs include the request_ids of
  drained rows — grep an incident's request_id and you see both the
  HTTP access log line AND the drain log line for the same
  operation, even though they live in different goroutines.
- **Typed errors at the repository↔service boundary.** The Phase 4-8
  string-matching wart in `mapRepoError` is finally gone. Repository
  exports `ErrNotFound`, `ErrConflict`, `ErrConstraintViolation`
  sentinels; the service uses `errors.Is`. The `repoError` struct
  carries both the sentinel (for the service) AND an apperror cause
  (for the handler's `respondError`) — each layer matches on its own
  vocabulary, no string parsing anywhere.
- **Keycloakadmin cache cleanup worker.** Phase 8 backlog item 5
  resolved. A small ticker-driven goroutine evicts expired cache
  entries from `keycloakadmin.Client`. Started under the same errgroup
  as the drainer. This is the second long-lived goroutine in the
  codebase — same four-questions discipline, simpler work, deliberate
  pedagogical contrast.
- **`/debug/pprof/*` gated by `PPROF_ENABLED`.** Off by default; flip
  to true when an incident needs profiling. Zero runtime cost when
  disabled. Standard `go tool pprof` workflow when on.

### What `/metrics` looks like

```bash
curl http://localhost:8080/metrics | head -50
```

You'll see, among ~50 distinct metrics:

```
# HELP http_requests_total Total HTTP requests. Labels are route (chi pattern), method, status class.
# TYPE http_requests_total counter
http_requests_total{method="GET",route="/api/v1/tasks",status="2xx"} 12
http_requests_total{method="POST",route="/api/v1/tasks",status="2xx"} 5

# HELP audit_outbox_pending Current number of unclaimed rows in audit_outbox. A growing value means the drainer is falling behind.
# TYPE audit_outbox_pending gauge
audit_outbox_pending 0

# HELP audit_events_total Total audit events recorded, by action and outcome.
# TYPE audit_events_total counter
audit_events_total{action="task.created",outcome="success"} 5
audit_events_total{action="task.deleted",outcome="denied"} 1
```

The format is human-readable on purpose — Prometheus's wire format is
literally a text file, which is why curling it works.

### New env vars

```
PPROF_ENABLED=false   # set to true to mount /debug/pprof/*
```

### Backlog after Phase 9

Items 3, 5, and 6 from Phase 8 are now resolved. Remaining:

1. **Integration tests with testcontainers-go** — still deferred. The
   long-standing Phase 5 item; Phase 10 is where it lands.
2. ~~Graceful-shutdown timeout hardcoded.~~ **Resolved in Phase 8.**
3. ~~Service↔repository error coupling uses string matching.~~
   **Resolved in Phase 9** via typed sentinels.
4. **Option A: no local users table** (Phase 6 decision, permanent).
   Not a defect; the trade-off remains explicit.
5. ~~Token-cache cleanup worker.~~ **Resolved in Phase 9.**
6. ~~Outbox observability.~~ **Resolved in Phase 9** via the
   `audit_outbox_pending` gauge.

Phase 10 closes the course: testing, Docker, CI.

## Phase 10 — Integration tests, Docker, GitHub Actions

The closing phase. After nine phases of building features, this one
adds three things that turn "a project on my laptop" into "a project
anyone can verify, build, and deploy":

- **Integration tests with testcontainers-go.** The Phase 5 placeholder
  is replaced with real integration tests against real Postgres. The
  per-suite testcontainers fixture (chosen deliberately for speed)
  starts one container, applies every migration, and shares the pool
  across all tests in a package. Tests truncate between runs via a
  cleanup hook. The result: ~10 integration tests across the
  repository and audit packages, running in ~10 seconds total.

- **A Dockerfile.** Multi-stage build: `golang:1.22-bookworm` builder
  → `gcr.io/distroless/static-debian12:nonroot` runtime. Final image
  ~20MB, no shell, no package manager, non-root user, fully static
  binary. The build is fully reproducible; the runtime is hardened
  by default.

- **GitHub Actions CI.** Four jobs: `lint` (go vet + staticcheck),
  `test-unit`, `test-integration`, and `docker-build`. PR merges
  blocked unless all green. Concurrency-controlled so newer commits
  cancel older runs. Cache-aware so subsequent runs reuse Go modules
  and Docker layers.

### What the integration tests verify

The unit tests since Phase 4 verified business rules using fakes. The
integration tests in this phase verify infrastructure the fakes were
pretending to be:

- Migrations apply cleanly to a fresh database in order
  (`TestMigrationsApplied`).
- Real SQL parses against real Postgres — column names, placeholders,
  types (`TestTaskRepository_Create_RoundTrips`).
- The Phase 9 dual-tagged `repoError` works on real `pgx.ErrNoRows`
  and real unique-violation errors — both `errors.Is` (sentinel) and
  `errors.As` (apperror) succeed on the same value
  (`TestTaskRepository_GetByID_NotFound_DualTagged`,
  `TestProjectRepository_Create_DuplicateKey_DualTagged`).
- The Phase 8 `WithTx` helper commits on success
  (`TestWithTx_CommitsOnSuccess`), rolls back on error
  (`TestWithTx_RollsBackOnError`), and rolls back on panic
  (`TestWithTx_RollsBackOnPanic`).
- The audit Recorder writes to outbox including `request_id`
  (`TestRecord_WritesToOutbox`).
- The Drainer moves rows from outbox to audit_log with request_id
  preserved (`TestDrainer_MovesOutboxRowsToAuditLog`).
- The Drainer is idempotent under at-least-once delivery —
  re-processing a row that's already in audit_log does not produce a
  duplicate (`TestDrainer_IsIdempotent`).
- The Recorder is transactionally atomic — when the caller's tx rolls
  back, the audit row does not exist
  (`TestRecorder_TransactionalAtomicity`).

### Building and running

```bash
# Run unit tests (fast, no Docker):
make test

# Run integration tests (needs Docker):
make integration-test

# Run everything CI runs, locally:
make ci

# Build the production image:
make docker-build

# Run the image (needs .env in the project root):
make docker-run
```

The Dockerfile is what you'd actually ship: multi-stage so the
toolchain doesn't bloat the runtime, distroless so there's nothing to
attack, non-root so a container escape doesn't grant root on the
host, static binary so the runtime has no glibc-version risk.

### Backlog after Phase 10

| # | Item | Status |
|---|------|--------|
| 1 | Integration tests with testcontainers-go | **Resolved in Phase 10** |
| 2 | Graceful-shutdown timeout < REQUEST_TIMEOUT | ~~Resolved in Phase 8~~ |
| 3 | Service↔repository error coupling uses strings | ~~Resolved in Phase 9~~ |
| 4 | Option A: no local users table | Permanent (Phase 6 decision) |
| 5 | Token-cache cleanup worker | ~~Resolved in Phase 9~~ |
| 6 | Outbox observability metric | ~~Resolved in Phase 9~~ |

The backlog is empty of actionable items. Item 4 is the explicit,
documented architectural trade-off from Phase 6 — by design, not a
defect.

## The finished system — a final tour

After ten phases the repository is a coherent system worth touring as
a whole. Here's the map:

### Packages (twelve)

```
internal/apperror     HTTP error vocabulary with status code categories
internal/audit        outbox-pattern audit log + drainer goroutine
internal/authz        pure authorization predicates (no I/O, no HTTP)
internal/config       Viper-backed config with validation
internal/database     pool + WithTx helper (the Queryer abstraction)
internal/domain       core types (Task, Project, Status) and inputs
internal/handler      HTTP handlers + DTO conversion
internal/keycloakadmin Keycloak Admin API client with TTL cache + cleanup worker
internal/metrics      Prometheus collectors with cardinality discipline
internal/middleware   HTTP middleware: RequestID, AccessLog, Recover, CORS, Timeout, Metrics, Auth
internal/repository   storage interfaces + Postgres impls + typed errors
internal/reqctx       cross-layer context keys (no HTTP, so service can import)
internal/server       chi router + route registration
internal/service      business rules; the one layer that knows everything
internal/validator    request-body validation
```

Plus `pkg/logger` (slog setup), `pkg/response` (JSON envelope), `cmd/api` (main), `test/testutil` (testcontainers fixture).

### Background goroutines (four)

1. **The HTTP server** (`http.Server` in `main.go`) — runs in its own
   goroutine, shuts down via `Shutdown(ctx)`.
2. **The audit drainer** (`audit.Drainer.Run` in `cmd/api/main.go`) —
   runs under `errgroup`, observes the root context, drains outbox
   on a 1-second poll.
3. **The keycloakadmin cache cleanup worker** (`keycloakadmin.Client.Run`)
   — runs under the same errgroup, evicts expired cache entries every
   5 minutes.
4. **The signal handler** — converts SIGTERM/SIGINT into context
   cancellation; the trigger for all three above.

Each one has documented answers to the four questions: who starts it,
who stops it, what happens to in-flight work at shutdown, what happens
on panic. They're not just code; they're disciplined code.

### The architectural properties — verified one last time

```bash
# Service has no HTTP knowledge
grep -rn '"net/http"' internal/service/ --include='*.go' | grep -v '//\|test'   # empty

# Service has no apperror knowledge
grep -rn '"gotask/internal/apperror"' internal/service/ --include='*.go' | grep -v '//\|test'   # empty

# Audit has no HTTP knowledge
grep -rn '"net/http"' internal/audit/ --include='*.go' | grep -v '//\|test'     # empty

# Authz is pure (no I/O)
grep -rn '"net/http"\|"context"' internal/authz/authz.go | grep -v '//'         # empty

# reqctx is pure (no HTTP, importable from service)
grep -rn '"net/http"' internal/reqctx/ --include='*.go'                         # empty
```

Five invariants. All five hold. They held when the codebase was 500
lines (Phase 1) and they hold now at several thousand. The discipline
encoded in package boundaries is what made every phase additive — no
Phase ever required undoing decisions from an earlier one.

### The cumulative validation: a single command

```bash
make ci
```

Runs in order: `go vet ./...`, the unit tests with `-race`, the
integration tests against real Postgres, the Docker build. If all
four pass, the codebase is in the same state every other commit on
main has been in.

This is the discipline the course was building toward all along:
**the system proves itself**. You don't have to trust the README; you
don't have to remember to run any particular check. One command,
exit code zero or non-zero. That's what a complete project looks
like.

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

make db-up        # start Postgres (docker), wait until healthy
make db-down      # stop Postgres (volume preserved)
make db-psql      # psql shell into the running database
make db-logs      # tail Postgres logs

make migrate-up       # apply all up migrations
make migrate-down     # roll back one migration
make migrate-version  # print current schema version
make migrate-create name=add_foo   # scaffold a new migration pair
make migrate-force version=N       # clear a dirty state (recovery only)
```

The migration targets require the `migrate` CLI. Install it once with
the postgres build tag (omitting the tag yields a confusing
"unknown driver" error):

```bash
go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
```

