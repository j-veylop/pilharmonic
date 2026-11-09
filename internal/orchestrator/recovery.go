package orchestrator

import (
	"fmt"
	"time"
)

// migrateStartupState upgrades legacy durable state before recovery interprets
// task lifecycle. A legacy running task regains its attempt first, allowing
// recovery to mark both the task and that attempt interrupted.
func migrateStartupState(data *state) bool {
	changed := false
	for i := range data.Tasks {
		t := &data.Tasks[i]
		if t.ThreadID == "" {
			id := newID()
			t.ThreadID = id
			data.Threads = append(data.Threads, Thread{ID: id, Triggers: []Trigger{t.Trigger}, Decisions: []Decision{t.Decision}})
			changed = true
		}
		if len(t.Attempts) == 0 {
			for _, event := range t.Events {
				if event.Kind != "started" {
					continue
				}
				id := newID()
				attempt := Attempt{ID: id, Agent: t.Agent, Status: t.Status, StartedAt: event.Time, Output: t.Output, Error: t.Error}
				if t.Status != Running {
					attempt.FinishedAt = t.UpdatedAt
				}
				t.Attempts = append(t.Attempts, attempt)
				changed = true
				break
			}
		}
	}
	return changed
}

// recoverStartupState settles interrupted process work and incomplete durable
// operations after legacy state has been migrated.
func recoverStartupState(s *Service) (bool, error) {
	changed := false
	for i := range s.data.Tasks {
		t := &s.data.Tasks[i]
		if t.DiscardPending {
			gone, err := reconcileDiscard(s.path, *t)
			if err != nil {
				return false, fmt.Errorf("recover worktree discard for task %s: %w", t.ID, err)
			}
			t.finishDiscard(gone, "Interrupted discard; worktree retained")
			changed = true
		}
		if t.settleAllPendingDeliveries("orchestrator stopped before acknowledgement", time.Now().UTC()) {
			changed = true
		}
		if t.Status == Running {
			if len(t.Attempts) > 0 {
				a := &t.Attempts[len(t.Attempts)-1]
				a.Status = Interrupted
				a.FinishedAt = time.Now().UTC()
				a.Error = "orchestrator stopped while task was running"
			}
			interruptActivities(t)
			t.Status = Interrupted
			t.UpdatedAt = time.Now().UTC()
			t.Error = "orchestrator stopped while task was running"
			t.Events = append(t.Events, Event{t.UpdatedAt, "interrupted", t.Error})
			changed = true
		}
	}
	return changed, nil
}
