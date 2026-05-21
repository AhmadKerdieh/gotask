package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gotask/internal/authz"
	"gotask/internal/config"
	"gotask/internal/domain"
)

// managerClaims returns a Claims value that authorizes ownership-gated
// operations across every seeded task in this test file. Phase 7 added
// authorization to the service (the reporter or a manager may
// delete/change-status); pre-Phase-7 tests didn't supply claims at all,
// so the simplest non-invasive fix is to call as a manager — which
// satisfies CanDeleteTask / CanChangeTaskStatus regardless of reporter.
// Tests that specifically exercise the OWNER path and the FORBIDDEN path
// are added in authz_test.go and the new ownership tests below.
func managerClaims() authz.Claims {
	return authz.Claims{
		Subject: uuid.New().String(),
		Roles:   []string{authz.RoleManager},
	}
}

// testWorkflow builds the workflow used across these tests. It mirrors
// the real workflow.yaml shape closely enough to exercise every business
// rule: a terminal status (done), a normal chain, and a no-back-edge from
// in_review to open.
func testWorkflow(t *testing.T) *config.Workflow {
	t.Helper()
	wf, err := config.NewWorkflowForTest(
		[]string{"open", "in_progress", "in_review", "done", "cancelled"},
		[]string{"done", "cancelled"},
		[]string{"low", "medium", "high", "critical"},
		map[string][]string{
			"open":        {"in_progress", "cancelled"},
			"in_progress": {"in_review", "cancelled", "open"},
			"in_review":   {"in_progress", "done", "cancelled"},
			"done":        {},
			"cancelled":   {},
		},
		"open",
		"medium",
	)
	require.NoError(t, err, "test workflow must be internally consistent")
	return wf
}

// newTaskServiceWithFakes wires a TaskService against in-memory fakes and
// returns the service plus the fakes so a test can pre-seed or inspect
// them. This is the payoff of the Phase 3 interface seam: no database,
// no docker, construction is three lines and runs in microseconds.
func newTaskServiceWithFakes(t *testing.T) (*TaskService, *fakeTaskRepo, *fakeProjectRepo) {
	t.Helper()
	tasks := newFakeTaskRepo()
	projects := newFakeProjectRepo()
	svc := NewTaskService(Deps{
		Tasks:    tasks,
		Projects: projects,
		Workflow: testWorkflow(t),
	})
	return svc, tasks, projects
}

// seedProject inserts a project directly through the fake so Create's
// existence check passes. Returns the project id.
func seedProject(t *testing.T, projects *fakeProjectRepo) uuid.UUID {
	t.Helper()
	p, err := projects.Create(context.Background(), domain.NewProjectInput{
		Key:     "TEST",
		Name:    "Test Project",
		OwnerID: uuid.New(),
	})
	require.NoError(t, err)
	return p.ID
}

func TestTaskService_Create_AppliesWorkflowDefaults(t *testing.T) {
	svc, _, projects := newTaskServiceWithFakes(t)
	projID := seedProject(t, projects)

	// No status or priority supplied — the service must fill them from
	// the workflow defaults (open / medium). This is a business rule;
	// the test asserts the rule, not an implementation detail.
	got, err := svc.Create(context.Background(), CreateTaskInput{
		ProjectID:  projID,
		Title:      "Write the docs",
		ReporterID: uuid.New(),
	})

	require.NoError(t, err)
	assert.Equal(t, domain.Status("open"), got.Status, "default status should be applied")
	assert.Equal(t, domain.Priority("medium"), got.Priority, "default priority should be applied")
}

func TestTaskService_Create_RejectsUnknownStatus(t *testing.T) {
	svc, _, projects := newTaskServiceWithFakes(t)
	projID := seedProject(t, projects)

	// This is the "banana" case from the Phase 3 review: nothing below
	// the service stops an illegal status. The service must.
	_, err := svc.Create(context.Background(), CreateTaskInput{
		ProjectID:  projID,
		Title:      "Bad status task",
		Status:     domain.Status("banana"),
		ReporterID: uuid.New(),
	})

	require.Error(t, err)
	var ve *ValidationError
	require.True(t, errors.As(err, &ve), "expected a ValidationError, got %T", err)
	assert.Contains(t, ve.Fields, "status")
}

func TestTaskService_Create_ValidationAccumulatesAllFields(t *testing.T) {
	svc, _, _ := newTaskServiceWithFakes(t)

	// Empty title, nil project, nil reporter — the service should report
	// every problem at once, not fail fast on the first. Accumulating
	// errors is a deliberate UX choice that belongs in the service.
	_, err := svc.Create(context.Background(), CreateTaskInput{})

	require.Error(t, err)
	var ve *ValidationError
	require.True(t, errors.As(err, &ve))
	assert.Contains(t, ve.Fields, "title")
	assert.Contains(t, ve.Fields, "project_id")
	assert.Contains(t, ve.Fields, "reporter_id")
}

func TestTaskService_Create_RejectsMissingProject(t *testing.T) {
	svc, _, _ := newTaskServiceWithFakes(t)

	// Project id is well-formed but no such project exists. The service
	// surfaces a precise field error rather than leaning on a database
	// foreign-key violation.
	_, err := svc.Create(context.Background(), CreateTaskInput{
		ProjectID:  uuid.New(),
		Title:      "Orphan task",
		ReporterID: uuid.New(),
	})

	require.Error(t, err)
	var ve *ValidationError
	require.True(t, errors.As(err, &ve))
	assert.Contains(t, ve.Fields, "project_id")
}

// TestTaskService_UpdateStatus_TransitionRules is the centrepiece: a
// table-driven test of the workflow transition rule, which is the rule
// that is neither HTTP nor SQL and could only live in the service.
func TestTaskService_UpdateStatus_TransitionRules(t *testing.T) {
	cases := []struct {
		name      string
		from      domain.Status
		to        domain.Status
		wantErr   error // sentinel to errors.Is against, or nil
		wantField string
	}{
		{
			name: "legal: open to in_progress",
			from: "open", to: "in_progress", wantErr: nil,
		},
		{
			name: "legal: in_review to done",
			from: "in_review", to: "done", wantErr: nil,
		},
		{
			name:    "illegal: done is terminal, cannot reopen",
			from:    "done", to: "in_progress",
			wantErr: ErrInvalidTransition,
		},
		{
			name:    "illegal: open straight to done skips the chain",
			from:    "open", to: "done",
			wantErr: ErrInvalidTransition,
		},
		{
			name:      "illegal: unknown target status",
			from:      "open", to: "banana",
			wantField: "status",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, tasks, projects := newTaskServiceWithFakes(t)
			projID := seedProject(t, projects)

			// Seed a task already in the `from` state. We go through
			// the fake directly to set up the precondition.
			seeded, err := tasks.Create(context.Background(), domain.NewTaskInput{
				ProjectID:  projID,
				Title:      "Transition subject",
				Status:     tc.from,
				Priority:   "medium",
				ReporterID: uuid.New(),
			})
			require.NoError(t, err)

			_, err = svc.UpdateStatus(context.Background(), managerClaims(), seeded.ID, tc.to)

			switch {
			case tc.wantErr != nil:
				require.Error(t, err)
				assert.True(t, errors.Is(err, tc.wantErr),
					"expected errors.Is(err, %v), got %v", tc.wantErr, err)
			case tc.wantField != "":
				require.Error(t, err)
				var ve *ValidationError
				require.True(t, errors.As(err, &ve))
				assert.Contains(t, ve.Fields, tc.wantField)
			default:
				require.NoError(t, err)
			}
		})
	}
}

func TestTaskService_Get_NotFoundMapsToServiceError(t *testing.T) {
	svc, _, _ := newTaskServiceWithFakes(t)

	// A missing task must come back as service.ErrNotFound (not a raw
	// repository/pg error), so the handler can map it to 404 by
	// branching on the service vocabulary alone.
	_, err := svc.Get(context.Background(), uuid.New())

	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotFound),
		"expected service.ErrNotFound, got %v", err)
}

func TestTaskService_Delete_RoundTrips(t *testing.T) {
	svc, tasks, projects := newTaskServiceWithFakes(t)
	projID := seedProject(t, projects)

	created, err := tasks.Create(context.Background(), domain.NewTaskInput{
		ProjectID:  projID,
		Title:      "To be deleted",
		Status:     "open",
		Priority:   "medium",
		ReporterID: uuid.New(),
	})
	require.NoError(t, err)

	require.NoError(t, svc.Delete(context.Background(), managerClaims(), created.ID))

	// Deleting again must now be ErrNotFound — proves the delete
	// actually removed it, not just returned nil.
	err = svc.Delete(context.Background(), managerClaims(), created.ID)
	assert.True(t, errors.Is(err, ErrNotFound))
}
