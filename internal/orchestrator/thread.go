package orchestrator

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CreateRelatedTask accepts a separate local mandate in an existing task's thread.
// Existing tasks are not given the new mandate as instructions.
func (s *Service) CreateRelatedTask(parentID, title, prompt, workspace string) (Task, error) {
	title = strings.TrimSpace(title)
	prompt = strings.TrimSpace(prompt)
	if title == "" || prompt == "" {
		return Task{}, errors.New("title and prompt are required")
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return Task{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return Task{}, fmt.Errorf("workspace: %w", err)
	}
	if !info.IsDir() {
		return Task{}, errors.New("workspace must be a directory")
	}
	id, err := newID()
	if err != nil {
		return Task{}, err
	}
	triggerID, err := newID()
	if err != nil {
		return Task{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return Task{}, errors.New("service is shutting down")
	}
	var threadID string
	for _, task := range s.data.Tasks {
		if task.ID == parentID {
			threadID = task.ThreadID
			break
		}
	}
	if threadID == "" {
		return Task{}, errors.New("parent task not found")
	}
	for i := range s.data.Threads {
		thread := &s.data.Threads[i]
		if thread.ID != threadID {
			continue
		}
		now := time.Now().UTC()
		trigger := Trigger{ID: triggerID, Source: "manual", Text: prompt, CreatedAt: now}
		decision := Decision{TriggerID: triggerID, Outcome: "accept", Reason: "explicit local request", CreatedAt: now}
		task := Task{ID: id, ThreadID: threadID, Title: title, Prompt: prompt, Workspace: abs, Status: Queued, Agent: "pi", CreatedAt: now, UpdatedAt: now, Trigger: trigger, Decision: decision, Events: []Event{{now, "accepted", "Manual request accepted in existing thread"}}}
		s.data.Tasks = append(s.data.Tasks, task)
		thread.Triggers = append(thread.Triggers, trigger)
		thread.Decisions = append(thread.Decisions, decision)
		if err := s.save(); err != nil {
			s.data.Tasks = s.data.Tasks[:len(s.data.Tasks)-1]
			thread.Triggers = thread.Triggers[:len(thread.Triggers)-1]
			thread.Decisions = thread.Decisions[:len(thread.Decisions)-1]
			return Task{}, err
		}
		return clone(task), nil
	}
	return Task{}, errors.New("task thread not found")
}

// SendThreadContext routes one immutable manual trigger to all queued and running
// tasks subscribed by thread membership. It persists all recipients and outcomes
// before enqueueing live deliveries. A retried trigger never gains new recipients.
func (s *Service) SendThreadContext(taskID, triggerID, text string) error {
	text = strings.TrimSpace(text)
	if text == "" || triggerID == "" {
		return errors.New("trigger ID and context are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errors.New("service is shutting down")
	}
	var threadID string
	for _, task := range s.data.Tasks {
		if task.ID == taskID {
			threadID = task.ThreadID
			break
		}
	}
	if threadID == "" {
		return errors.New("task not found")
	}
	var thread *Thread
	for i := range s.data.Threads {
		t := &s.data.Threads[i]
		for _, existing := range t.Triggers {
			if existing.ID == triggerID {
				if t.ID == threadID && existing.Text == text {
					for _, decision := range t.Decisions {
						if decision.TriggerID == triggerID && decision.Outcome == "context" && decision.Reason == "manual thread follow-up; does not expand mandates" {
							return nil
						}
					}
				}
				return errors.New("trigger ID already exists")
			}
		}
		if t.ID == threadID {
			thread = t
		}
	}
	if thread == nil {
		return errors.New("task thread not found")
	}
	now := time.Now().UTC()
	base := Trigger{ID: triggerID, Source: "manual", Text: text, CreatedAt: now}
	oldTasks := make(map[int]Task)
	type delivery struct {
		ch      chan Trigger
		trigger Trigger
	}
	var deliveries []delivery
	for i := range s.data.Tasks {
		task := &s.data.Tasks[i]
		if task.ContextPaused || task.ThreadID != threadID || (task.Status != Queued && task.Status != Running) {
			continue
		}
		oldTasks[i] = clone(*task)
		trigger := base
		if task.Status == Queued {
			trigger.Delivery = DeliveryAtStart
		} else {
			attemptID := task.currentAttemptID()
			ch := s.live[task.ID]
			switch {
			case ch == nil:
				trigger.Delivery = DeliveryRejected
				trigger.DeliveryError = "agent does not support live context"
				task.appendDelivery(trigger.ID, attemptID, DeliveryRejected, trigger.DeliveryError, now)
			case len(ch) == cap(ch):
				trigger.Delivery = DeliveryRejected
				trigger.DeliveryError = "live context queue is full"
				task.appendDelivery(trigger.ID, attemptID, DeliveryRejected, trigger.DeliveryError, now)
			default:
				trigger.Delivery = DeliveryPending
				task.appendDelivery(trigger.ID, attemptID, DeliveryPending, "", now)
				deliveries = append(deliveries, delivery{ch, trigger})
			}
		}
		task.Context = append(task.Context, trigger)
		task.UpdatedAt = now
		task.Events = append(task.Events, Event{now, "context", "Thread context added (" + trigger.Delivery + ")"})
	}
	if len(oldTasks) == 0 {
		return errors.New("thread has no subscribed queued or running tasks")
	}
	thread.Triggers = append(thread.Triggers, base)
	thread.Decisions = append(thread.Decisions, Decision{TriggerID: triggerID, Outcome: "context", Reason: "manual thread follow-up; does not expand mandates", CreatedAt: now})
	if err := s.save(); err != nil {
		for i, old := range oldTasks {
			s.data.Tasks[i] = old
		}
		thread.Triggers = thread.Triggers[:len(thread.Triggers)-1]
		thread.Decisions = thread.Decisions[:len(thread.Decisions)-1]
		return err
	}
	// The service lock prevents competing sends; all channels were checked above.
	for _, d := range deliveries {
		d.ch <- d.trigger
	}
	return nil
}

func (s *Service) AddManualThreadContext(taskID, text string) error {
	id, err := newID()
	if err != nil {
		return err
	}
	return s.SendThreadContext(taskID, id, text)
}

// SetContextSubscription controls future thread follow-ups. Already recorded
// context and in-flight deliveries remain part of the task history.
func (s *Service) SetContextSubscription(taskID string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errors.New("service is shutting down")
	}
	for i := range s.data.Tasks {
		t := &s.data.Tasks[i]
		if t.ID != taskID {
			continue
		}
		if t.ContextPaused == !enabled {
			return nil
		}
		old := clone(*t)
		t.ContextPaused = !enabled
		t.UpdatedAt = time.Now().UTC()
		message := "Thread context paused"
		if enabled {
			message = "Thread context resumed; earlier inputs are not replayed"
		}
		t.Events = append(t.Events, Event{t.UpdatedAt, "subscription", message})
		if err := s.save(); err != nil {
			*t = old
			return err
		}
		return nil
	}
	return errors.New("task not found")
}
