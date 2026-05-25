# syntax=docker/dockerfile:1.6
#
# ── Multi-stage build for gotask ────────────────────────────────────
#
# Stage 1 (builder): Go toolchain compiles the binary. ~800MB image,
# discarded after the build.
#
# Stage 2 (runtime): distroless/static-debian12 — Google's minimal
# image with glibc but no shell, no package manager, no debug tools.
# Final image is around 20MB and has effectively zero attack surface.
#
# Why distroless over scratch:
#   - distroless ships CA certificates (needed for our outbound HTTPS
#     to Keycloak's OIDC discovery + JWKS), tzdata, and /etc/passwd
#     with a non-root user pre-configured. scratch has none of these
#     and you'd have to copy them in by hand, which is the kind of
#     thing that breaks silently.
#   - The 5MB size difference vs scratch is negligible compared to
#     what you get: less manual setup, fewer subtle bugs.
#
# Build:
#   docker build -t gotask:dev .
# Run:
#   docker run --rm -p 8080:8080 --env-file .env gotask:dev
#
# ──────────────────────────────────────────────────────────────────────

# ── Stage 1: builder ────────────────────────────────────────────────
FROM golang:1.22-bookworm AS builder

# Pin the toolchain in the image, but also let it be overridden at
# build time for the rare case where you need a newer toolchain than
# the base image ships with. `GOTOOLCHAIN=local` forces use of the
# image's Go, preventing surprise downloads during build.
ENV GOTOOLCHAIN=local

WORKDIR /src

# Copy go.mod and go.sum first, in their own layer. This is the
# single largest Docker-build optimization for Go projects: as long
# as the dependency list doesn't change, the layer that downloads
# them stays cached. Subsequent code changes do NOT re-download deps.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Now copy the rest of the source. The previous layer's cache is
# unaffected by these files.
COPY . .

# Build flags, explained:
#   CGO_ENABLED=0  — pure-Go binary, no dependency on glibc/musl
#                   versions in the runtime image. Required for the
#                   distroless/static target (which has no libc).
#   -ldflags="-s -w" — strip symbol table and DWARF debug info.
#                     Halves the binary size; only loses post-mortem
#                     debugging surface we wouldn't have on distroless
#                     anyway (no /usr/bin/gdb in the runtime).
#   -trimpath      — remove local filesystem paths from the binary.
#                   Both a size win and a small security improvement
#                   (don't leak your home directory in the binary).
#   -mod=readonly  — fail if go.mod is dirty. Belt-and-braces against
#                   accidental mutation during build.
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux go build \
        -mod=readonly \
        -trimpath \
        -ldflags="-s -w" \
        -o /out/gotask \
        ./cmd/api

# ── Stage 2: runtime ────────────────────────────────────────────────
# distroless/static-debian12:nonroot has:
#   - glibc, CA certs, tzdata
#   - /etc/passwd with a `nonroot` user (UID 65532)
#   - NO shell, NO package manager
#
# The `:nonroot` tag (vs the default `:latest`) configures the image
# to run as the non-root user by default. We belt-and-brace that with
# an explicit USER directive below.
FROM gcr.io/distroless/static-debian12:nonroot

# /app is the conventional location. We don't strictly need WORKDIR
# (the binary works from anywhere), but it makes runtime paths
# predictable: relative paths in config resolve from /app.
WORKDIR /app

# Copy the binary + the static assets the binary needs at runtime:
#   migrations/   for golang-migrate to apply (mounted via -v in dev
#                or baked in for production deploys that migrate as
#                part of startup)
#   static/      the diagnostic frontend
#   workflow.yaml the workflow configuration
#
# We deliberately do NOT copy .env — that's environment, not code.
# Mount it at runtime via -v or pass via --env-file.
COPY --from=builder /out/gotask /app/gotask
COPY migrations /app/migrations
COPY static /app/static
COPY workflow.yaml /app/workflow.yaml

# Explicit user even though the base image's default would also be
# nonroot. Defense in depth against a future base-image change.
USER nonroot:nonroot

# Document the port. This is metadata only; the container still needs
# `-p 8080:8080` at runtime to be reachable. Documenting it here lets
# orchestration tools (k8s, compose) read it.
EXPOSE 8080

# Direct binary invocation — no shell to fork, no signal-handling
# wrapper. Go's runtime handles SIGTERM directly, which is what our
# Phase 8 graceful shutdown requires.
ENTRYPOINT ["/app/gotask"]
