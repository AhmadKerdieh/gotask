//go:build integration
// +build integration

// Integration tests for the repository layer.
//
// These tests run only with `go test -tags=integration ./...`. They
// require Docker and take seconds (not milliseconds) because they
// boot a real Postgres container. They answer questions the unit
// tests cannot:
//
//   - Does our SQL actually parse against the real Postgres? Column
//     names, placeholder counts, type conversions — fakes ignore
//     all of these; only the real DB tells the truth.
//
//   - Do our migrations apply cleanly to a fresh database in order?
//     This is the schema-evolution sanity check the project's been
//     missing since Phase 1.
//
//   - Does translateError actually produce the dual-tagged repoError
//     that satisfies BOTH errors.Is(repository.ErrNotFound) AND
//     errors.As(&apperror.Error{})? The fakes had to be updated
//     manually to match this in Phase 9; here we verify the REAL
//     repo produces it.
//
//   - Does the WithTx rollback discipline actually roll back? Only
//     a real DB has transactional semantics to verify against.
package repository_test

import (
	"context"
	"errors"
	"log"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gotask/internal/apperror"
	"gotask/internal/database"
	"gotask/internal/domain"
	"gotask/internal/repository"
	"gotask/test/testutil"
)

// sharedDB is the per-suite Postgres fixture. TestMain populates it;
// every test reads it via the helper below.
var sharedDB *testutil.PostgresFixture

func TestMain(m *testing.M) {
	// One container for the whole suite. The startup cost is paid
	// here exactly once. The container is torn down even if tests
	// panic, because os.Exit only runs after m.Run returns.
	fixture, err := testutil.StartPostgres(context.Background())
	if err != nil {
		log.Fatalf("integration: start postgres: %v", err)
	}
	sharedDB = fixture

	code := m.Run()

	// Tear down before exit. We deliberately use context.Background
	// (not a derived context) because the test run is over; we don't
	// want a cancelled-context to short-circuit termination.
	sharedDB.Terminate(context.Background())
	os.Exit(code)
}

// fixture is the standard test setup: returns the shared fixture,
// schedules a truncate cleanup. Every test calls this; nothing else
// exists for setup.
func fixture(t *testing.T) *testutil.PostgresFixture {
	t.Helper()
	t.Cleanup(func() { sharedDB.Truncate(t) })
	return sharedDB
}

// TestMigrationsApplied is a sanity check: TestMain ran the migrations,
// so the expected tables must exist. If this fails, every other test
// in the file will too — having it as the first assertion makes the
// real cause obvious.
func TestMigrationsApplied(t *testing.T) {
	f := fixture(t)
	for _, table := range []string{"projects", "tasks", "audit_log", "audit_outbox"} {
		var exists bool
		err := f.Pool.QueryRow(context.Background(),
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`,
			table,
		).Scan(&exists)
		require.NoError(t, err, "checking table %s", table)
		assert.True(t, exists, "table %s should exist after migrations", table)
	}
}

// TestTaskRepository_Create_RoundTrips covers the happy path: insert,
// read back. The thing the unit tests can't tell us is whether the
// INSERT statement's columns/placeholders are correct against the
// REAL schema. They are if and only if this test passes.
func TestTaskRepository_Create_RoundTrips(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	projects := repository.NewProjectRepository(f.Pool)
	tasks := repository.NewTaskRepository(f.Pool)

	proj, err := projects.Create(ctx, domain.NewProjectInput{
		Key:     "DEMO",
		Name:    "Demo Project",
		OwnerID: uuid.New(),
	})
	require.NoError(t, err)

	created, err := tasks.Create(ctx, domain.NewTaskInput{
		ProjectID:  proj.ID,
		Title:      "First task",
		Status:     "open",
		Priority:   "med",
		ReporterID: uuid.New(),
	})
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, created.ID)

	roundTrip, err := tasks.GetByID(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "First task", roundTrip.Title)
	assert.Equal(t, proj.ID, roundTrip.ProjectID)
}

// TestTaskRepository_GetByID_NotFound_DualTagged produces the dual-
// tagged error the Phase 9 typed-error story depends on. errors.Is
// matches the repository sentinel (used by service.mapRepoError);
// errors.As finds the apperror cause (used by handler.respondError).
// Both succeed on the SAME error value — that's the dual-tagging
// contract verified against the real translateError, not the fake.
func TestTaskRepository_GetByID_NotFound_DualTagged(t *testing.T) {
	f := fixture(t)
	tasks := repository.NewTaskRepository(f.Pool)

	_, err := tasks.GetByID(context.Background(), uuid.New())
	require.Error(t, err)

	// Service-side check:
	assert.True(t, errors.Is(err, repository.ErrNotFound),
		"expected errors.Is(err, repository.ErrNotFound); got %v", err)

	// Handler-side check, on the same error:
	var ae *apperror.Error
	assert.True(t, errors.As(err, &ae),
		"expected errors.As(err, &apperror.Error{}); got %v", err)
	if ae != nil {
		assert.Equal(t, apperror.KindNotFound, ae.Kind)
	}
}

// TestProjectRepository_Create_DuplicateKey_DualTagged: the same
// dual-tagging discipline for a different code path.
func TestProjectRepository_Create_DuplicateKey_DualTagged(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	projects := repository.NewProjectRepository(f.Pool)

	_, err := projects.Create(ctx, domain.NewProjectInput{
		Key: "DUP", Name: "First", OwnerID: uuid.New(),
	})
	require.NoError(t, err)

	_, err = projects.Create(ctx, domain.NewProjectInput{
		Key: "DUP", Name: "Second", OwnerID: uuid.New(),
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, repository.ErrConflict),
		"expected errors.Is(err, repository.ErrConflict); got %v", err)
}

// TestWithTx_CommitsOnSuccess: the happy path of the transaction
// helper. A write inside the closure is visible afterward.
func TestWithTx_CommitsOnSuccess(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	db := &database.DB{Pool: f.Pool}

	var projID uuid.UUID
	err := db.WithTx(ctx, func(tx pgx.Tx) error {
		projects := repository.NewProjectRepositoryTx(tx)
		p, err := projects.Create(ctx, domain.NewProjectInput{
			Key: "TX1", Name: "Tx Project", OwnerID: uuid.New(),
		})
		if err != nil {
			return err
		}
		projID = p.ID
		return nil
	})
	require.NoError(t, err)

	roundTrip, err := repository.NewProjectRepository(f.Pool).GetByID(ctx, projID)
	require.NoError(t, err)
	assert.Equal(t, "TX1", roundTrip.Key)
}

// TestWithTx_RollsBackOnError: the central correctness property of
// Phase 8's transaction helper. If the closure returns an error,
// EVERY write inside it must be invisible afterward.
func TestWithTx_RollsBackOnError(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	db := &database.DB{Pool: f.Pool}

	wantErr := errors.New("simulated mid-transaction failure")

	err := db.WithTx(ctx, func(tx pgx.Tx) error {
		projects := repository.NewProjectRepositoryTx(tx)
		_, err := projects.Create(ctx, domain.NewProjectInput{
			Key: "TX2", Name: "Should Not Persist", OwnerID: uuid.New(),
		})
		if err != nil {
			return err
		}
		return wantErr
	})
	require.ErrorIs(t, err, wantErr)

	all, err := repository.NewProjectRepository(f.Pool).List(ctx)
	require.NoError(t, err)
	for _, p := range all {
		assert.NotEqual(t, "TX2", p.Key, "rolled-back project should not be visible")
	}
}

// TestWithTx_RollsBackOnPanic: a deferred Rollback in the helper must
// also fire on panic. If this regresses, transactions leak — the kind
// of bug that doesn't show up until production load.
func TestWithTx_RollsBackOnPanic(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	db := &database.DB{Pool: f.Pool}

	require.Panics(t, func() {
		_ = db.WithTx(ctx, func(tx pgx.Tx) error {
			projects := repository.NewProjectRepositoryTx(tx)
			_, _ = projects.Create(ctx, domain.NewProjectInput{
				Key: "TX3", Name: "Panic Project", OwnerID: uuid.New(),
			})
			panic("simulated mid-transaction panic")
		})
	})

	all, err := repository.NewProjectRepository(f.Pool).List(ctx)
	require.NoError(t, err)
	for _, p := range all {
		assert.NotEqual(t, "TX3", p.Key, "panic-rolled-back project should not be visible")
	}
}
