package audit

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

// FakeAuditor is an in-memory Auditor for use in service tests. Like the
// repository fakes, it really works — Record appends, List filters and
// orders — so tests assert on OUTCOMES (the events that were recorded),
// not on internal call shapes. A second test file may want to assert
// interactions (mock-style); for that, wrap one of these in a mock if
// needed. Most tests want the fake: it answers "did the service emit
// exactly the right audit event when the user deleted their own task?"
//
// Safe for concurrent use; the mutex is cheap and prevents future
// parallel-tests flakiness.
type FakeAuditor struct {
	mu      sync.Mutex
	events  []Entry
	nextSeq int
}

// NewFake returns an empty FakeAuditor.
func NewFake() *FakeAuditor { return &FakeAuditor{} }

func (f *FakeAuditor) Record(ctx context.Context, e Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextSeq++
	// Use sequence as a fake nanosecond offset so OccurredAt is strictly
	// increasing across calls — keeps the "ORDER BY occurred_at DESC"
	// contract honest in tests.
	f.events = append(f.events, Entry{
		ID:         uuid.New(),
		OccurredAt: time.Unix(0, int64(f.nextSeq)),
		Actor:      e.Actor,
		Action:     e.Action,
		Target:     e.Target,
		Outcome:    e.Outcome,
		Detail:     e.Detail,
		RequestID:  e.RequestID,
	})
	return nil
}

func (f *FakeAuditor) List(ctx context.Context, q Query) ([]Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Entry, 0, len(f.events))
	for i := len(f.events) - 1; i >= 0; i-- { // newest first
		e := f.events[i]
		if q.Actor != uuid.Nil && e.Actor != q.Actor {
			continue
		}
		if q.Action != "" && e.Action != q.Action {
			continue
		}
		if q.Target.Kind != "" && e.Target.Kind != q.Target.Kind {
			continue
		}
		if q.Target.ID != uuid.Nil && e.Target.ID != q.Target.ID {
			continue
		}
		out = append(out, e)
		if q.Limit > 0 && len(out) >= q.Limit {
			break
		}
	}
	return out, nil
}

// Events returns a snapshot of every recorded event in insertion order.
// Test-only helper; not part of the Auditor interface. Tests use this to
// make precise assertions like "exactly one delete event was recorded
// with these fields."
func (f *FakeAuditor) Events() []Entry {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Entry, len(f.events))
	copy(out, f.events)
	return out
}
