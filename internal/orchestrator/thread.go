package orchestrator

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const threadContextReason = "manual thread follow-up; does not expand mandates"

// Older state records thread-wide delivery in the decision reason.
func (thread Thread) isThreadContext(triggerID string) bool {
	for _, d := range thread.Decisions {
		if d.TriggerID == triggerID && d.Outcome == "context" && d.Reason == threadContextReason {
			return true
		}
	}
	return false
}

func (s *Service) findTriggerLocked(id string) (*Thread, Trigger, bool) {
	for i := range s.data.Threads {
		for _, trigger := range s.data.Threads[i].Triggers {
			if trigger.ID == id {
				return &s.data.Threads[i], trigger, true
			}
		}
	}
	return nil, Trigger{}, false
}

// liveChannelLocked returns a running task's live context queue, or why it
// cannot take input now.
func (s *Service) liveChannelLocked(taskID string) (chan Trigger, string) {
	ch := s.live[taskID]
	switch {
	case ch == nil:
		return nil, "agent does not support live context"
	case len(ch) == cap(ch):
		return nil, "live context queue is full"
	}
	return ch, ""
}

// AddContext records a manual follow-up for a queued or running task. It does
// not change the original mandate. The trigger ID makes retries idempotent.
func (s *Service) AddContext(taskID, triggerID, text string) error {
	text = strings.TrimSpace(text)
	if text == "" || triggerID == "" {
		return errors.New("trigger ID and context are required")
	}
	return s.whileOpen(func() error { return s.addContextLocked(taskID, triggerID, text) })
}

// addContextLocked records one validated context input. Caller holds mu.
func (s *Service) addContextLocked(taskID, triggerID, text string) error {
	task := s.taskLocked(taskID)
	if task == nil {
		return errTaskNotFound
	}
	// A retry must name the same recipient. A mandate or a thread-wide trigger
	// can have the same text, but came through a different route.
	if thread, existing, ok := s.findTriggerLocked(triggerID); ok {
		if thread.ID == task.ThreadID && existing.Text == text && !thread.isThreadContext(triggerID) {
			for _, c := range task.Context {
				if c.ID == triggerID {
					return nil
				}
			}
		}
		return errors.New("trigger ID already exists")
	}
	if task.Status != Queued && task.Status != Running {
		return fmt.Errorf("context can only be added to queued or running tasks; task is %s", task.Status)
	}
	var live chan Trigger
	if task.Status == Running {
		var refusal string
		if live, refusal = s.liveChannelLocked(task.ID); refusal != "" {
			return errors.New(refusal)
		}
	}
	thread := s.threadLocked(task.ThreadID)
	if thread == nil {
		return errors.New("task thread not found")
	}
	now := time.Now().UTC()
	trigger := Trigger{ID: triggerID, Source: "manual", Text: text, CreatedAt: now, Delivery: DeliveryAtStart}
	snapshot := s.snapshotLocked()
	if live != nil {
		trigger.Delivery = DeliveryPending
		task.appendDelivery(triggerID, task.currentAttemptID(), DeliveryPending, "", now)
	}
	task.Context = append(task.Context, trigger)
	task.UpdatedAt = now
	task.Events = append(task.Events, Event{now, "context", "Manual context added"})
	thread.Triggers = append(thread.Triggers, trigger)
	thread.Decisions = append(thread.Decisions, Decision{TriggerID: triggerID, Outcome: "context", Reason: "manual follow-up; does not expand mandate", CreatedAt: now})
	if err := s.commitLocked(snapshot); err != nil {
		return err
	}
	if live != nil {
		live <- trigger
	}
	return nil
}

// SendThreadContext routes one immutable manual trigger to all queued and
// running tasks subscribed by thread membership. It persists all recipients
// and outcomes before enqueueing live deliveries. A retried trigger never
// gains new recipients.
func (s *Service) SendThreadContext(taskID, triggerID, text string) error {
	text = strings.TrimSpace(text)
	if text == "" || triggerID == "" {
		return errors.New("trigger ID and context are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	thread, err := s.contextThreadLocked(taskID, triggerID, text)
	if thread == nil || err != nil {
		return err
	}
	threadID := thread.ID
	now := time.Now().UTC()
	base := Trigger{ID: triggerID, Source: "manual", Text: text, CreatedAt: now}
	type delivery struct {
		ch        chan Trigger
		trigger   Trigger
		taskID    string
		attemptID string
	}
	var deliveries []delivery
	recipients := 0
	snapshot := s.snapshotLocked()
	for i := range s.data.Tasks {
		task := &s.data.Tasks[i]
		if task.ContextPaused || task.ThreadID != threadID || (task.Status != Queued && task.Status != Running) {
			continue
		}
		recipients++
		trigger := base
		trigger.Delivery = DeliveryAtStart
		if task.Status == Running {
			attemptID := task.currentAttemptID()
			ch, refusal := s.liveChannelLocked(task.ID)
			if refusal != "" {
				trigger.Delivery, trigger.DeliveryError = DeliveryRejected, refusal
			} else {
				trigger.Delivery = DeliveryPending
				deliveries = append(deliveries, delivery{ch: ch, trigger: trigger, taskID: task.ID, attemptID: attemptID})
			}
			task.appendDelivery(trigger.ID, attemptID, trigger.Delivery, trigger.DeliveryError, now)
		}
		task.Context = append(task.Context, trigger)
		task.UpdatedAt = now
		task.Events = append(task.Events, Event{now, "context", "Thread context added (" + trigger.Delivery + ")"})
	}
	if recipients == 0 {
		return errors.New("thread has no subscribed queued or running tasks")
	}
	thread.Triggers = append(thread.Triggers, base)
	thread.Decisions = append(thread.Decisions, Decision{TriggerID: triggerID, Outcome: "context", Reason: threadContextReason, CreatedAt: now})
	if err = s.commitLocked(snapshot); err != nil {
		return err
	}
	// Capacity was checked above, so a full buffer means the live reader is
	// gone; the durable record must then report the hand-off as rejected.
	for _, d := range deliveries {
		select {
		case d.ch <- d.trigger:
		default:
			const reason = "live context queue is full"
			if err := s.settleDeliveryLocked(s.taskLocked(d.taskID), d.attemptID, d.trigger.ID, DeliveryRejected, reason, reason, now); err != nil {
				s.persistErr = err
			}
		}
	}
	return nil
}

// contextThreadLocked returns the thread of the task for a new thread-wide
// trigger, or nil when the trigger was already recorded identically.
func (s *Service) contextThreadLocked(taskID, triggerID, text string) (*Thread, error) {
	if s.closing {
		return nil, errShuttingDown
	}
	source := s.taskLocked(taskID)
	if source == nil || source.ThreadID == "" {
		return nil, errTaskNotFound
	}
	if thread, existing, ok := s.findTriggerLocked(triggerID); ok {
		if thread.ID == source.ThreadID && existing.Text == text && thread.isThreadContext(triggerID) {
			return nil, nil
		}
		return nil, errors.New("trigger ID already exists")
	}
	if thread := s.threadLocked(source.ThreadID); thread != nil {
		return thread, nil
	}
	return nil, errors.New("task thread not found")
}

func (s *Service) AddManualThreadContext(taskID, text string) error {
	return s.SendThreadContext(taskID, newID(), text)
}

// FollowUp records one manual follow-up for a selected task. Queued and
// running tasks receive it as context. An inactive task is requeued with the
// follow-up as at-start context and needs a new start; its mandate and
// attempts are not changed. The result reports whether it was requeued.
func (s *Service) FollowUp(taskID, text string) (bool, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return false, errors.New("follow-up text is required")
	}
	requeued := false
	err := s.withTask(taskID, func(t *Task) error {
		if t.DiscardPending {
			return errors.New("worktree discard is pending; wait before following up")
		}
		if t.Status == Queued || t.Status == Running {
			return s.addContextLocked(taskID, newID(), text)
		}
		if err := s.checkRequeueIntegrationLocked(*t); err != nil {
			return err
		}
		switch t.Status {
		case Completed, Failed, Canceled, Interrupted:
		default:
			return fmt.Errorf("cannot follow up task in %s state", t.Status)
		}
		// Requeueing clears the review decision, so a prepared commit awaiting
		// the operator must be settled first or the approval would be lost.
		if t.AwaitingReview() {
			return errors.New("task has prepared work awaiting review; approve or reject it first")
		}
		thread := s.threadLocked(t.ThreadID)
		if thread == nil {
			return errors.New("task thread not found")
		}
		id := newID()
		now := time.Now().UTC()
		trigger := Trigger{ID: id, Source: "manual", Text: text, CreatedAt: now, Delivery: DeliveryAtStart}
		snapshot := s.snapshotLocked()
		t.Context = append(t.Context, trigger)
		t.requeue(now)
		t.Events = append(t.Events, Event{now, "followup", "Follow-up context recorded"}, Event{now, "requeued", "Task requeued for follow-up"})
		thread.Triggers = append(thread.Triggers, trigger)
		thread.Decisions = append(thread.Decisions, Decision{TriggerID: id, Outcome: "context", Reason: "manual follow-up; requeues the task without changing its mandate", CreatedAt: now})
		if err := s.commitLocked(snapshot); err != nil {
			return err
		}
		requeued = true
		return nil
	})
	return requeued, err
}

// SetContextSubscription controls future thread follow-ups. Recorded context
// and in-flight deliveries remain part of the task history.
func (s *Service) SetContextSubscription(taskID string, enabled bool) error {
	return s.withTask(taskID, func(t *Task) error {
		if t.ContextPaused == !enabled {
			return nil
		}
		return s.mutateTaskLocked(t, func(t *Task) error {
			t.ContextPaused = !enabled
			t.UpdatedAt = time.Now().UTC()
			message := "Thread context paused"
			if enabled {
				message = "Thread context resumed; earlier inputs are not replayed"
			}
			t.Events = append(t.Events, Event{t.UpdatedAt, "subscription", message})
			return nil
		})
	})
}
