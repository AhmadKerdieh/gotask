package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"gotask/internal/domain"
	"gotask/internal/repository"
	"gotask/internal/service/mocks"
)

// This file demonstrates the SAME service behaviour as
// task_service_test.go, but verified with a generated-style mock instead
// of a hand-written fake. Read both and the trade-off becomes concrete.
//
// What the mock buys you that the fake does not: precise interaction
// assertions. Below we assert that an illegal transition is rejected
// WITHOUT the repository's Update ever being called. A fake cannot prove
// "Update was never called" — it would just silently not be called. The
// mock makes that absence an explicit, checked assertion. That is the
// situation where mocks earn their extra machinery.
func TestTaskService_UpdateStatus_IllegalTransition_DoesNotWrite(t *testing.T) {
	taskMock := new(mocks.TaskRepository)

	id := uuid.New()

	// Program GetByID to return a task already in the terminal `done`
	// state. The .Once() makes the expected call count explicit.
	taskMock.
		On("GetByID", mock.Anything, id).
		Return(domain.Task{ID: id, Status: domain.Status("done")}, nil).
		Once()

	// Deliberately program NO expectation for Update. With testify, a
	// call to an unexpected method fails the test. Combined with
	// AssertExpectations below, this asserts Update is never invoked.

	svc := NewTaskService(Deps{
		Tasks:    taskMock,
		Projects: &noopProjectRepo{}, // Create path unused in this test
		Workflow: testWorkflow(t),
	})

	_, err := svc.UpdateStatus(context.Background(), managerClaims(), id, domain.Status("in_progress"))

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidTransition)

	// This is the assertion a fake cannot express: every programmed
	// expectation was met AND no un-programmed method (i.e. Update) was
	// called. The business rule "reject before writing" is now proven,
	// not merely assumed.
	taskMock.AssertExpectations(t)
	taskMock.AssertNotCalled(t, "Update", mock.Anything, mock.Anything, mock.Anything)
}

// noopProjectRepo is a tiny stand-in so the TaskService constructor has a
// ProjectRepository; the project path is not exercised by this test.
type noopProjectRepo struct{}

var _ repository.ProjectRepository = (*noopProjectRepo)(nil)

func (noopProjectRepo) Create(context.Context, domain.NewProjectInput) (domain.Project, error) {
	return domain.Project{}, nil
}
func (noopProjectRepo) GetByID(context.Context, uuid.UUID) (domain.Project, error) {
	return domain.Project{}, nil
}
func (noopProjectRepo) List(context.Context) ([]domain.Project, error) { return nil, nil }
func (noopProjectRepo) Delete(context.Context, uuid.UUID) error        { return nil }
