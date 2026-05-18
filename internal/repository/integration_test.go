package repository

import "testing"

// This file is a deliberate, tracked placeholder. Phase 5 chose to defer
// integration tests to Phase 10; this keeps that decision VISIBLE in the
// codebase rather than leaving it as an unwritten intention.
//
// Why integration tests matter and why a unit test cannot replace them:
//
// The Phase 4 service tests run against hand-written in-memory fakes.
// Those prove the BUSINESS LOGIC is correct, fast, with no database. But
// a fake can never catch a defect in the actual SQL: a malformed query
// string, a column name that drifted from the migration, a NULL/zero
// mapping that is wrong, an injection-unsafe concatenation, a missing
// index causing a sequential scan. The fake faithfully implements the
// interface — including faithfully NOT noticing that taskPostgres.List
// builds bad SQL, because the fake does not run SQL at all.
//
// An integration test closes that gap: it stands up a REAL, ephemeral
// Postgres, runs the REAL migrations against it, and exercises the REAL
// repository implementations. It is slow (seconds, not microseconds) and
// needs Docker, which is exactly why you want MANY unit tests and FEW
// integration tests — the testing pyramid.
//
// Phase 10 will implement this with testcontainers-go:
//
//   - spin up postgres:16.3 in a container scoped to the test run
//   - run ./migrations against it (the same files production uses)
//   - construct NewTaskRepository / NewProjectRepository on the real pool
//   - assert Create/Get/List/Update/Delete round-trip correctly,
//     including the NULL<->zero mapping and the dynamic List query that
//     no unit test can verify
//   - tear the container down regardless of pass/fail
//
// Until then this test is skipped so `go test ./...` stays green while
// the gap remains honestly marked. Tracked in README under the
// hardening/backlog section.
func TestRepository_Integration_Placeholder(t *testing.T) {
	t.Skip("integration tests deferred to Phase 10 (testcontainers-go) — see comment above")
}
