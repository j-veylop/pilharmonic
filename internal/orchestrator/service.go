// Package orchestrator owns routing, durable task state, and agent execution.
package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Status string

const (
	Queued      Status = "queued"
	Running     Status = "running"
	Completed   Status = "completed"
	Failed      Status = "failed"
	Canceled    Status = "canceled"
	Interrupted Status = "interrupted"
)

// Trigger records the explicit local request that authorizes a task.
type Trigger struct {
	ID            string    `json:"id"`
	Source        string    `json:"source"`
	Text          string    `json:"text"`
	CreatedAt     time.Time `json:"created_at"`
	Delivery      string    `json:"delivery,omitempty"`
	DeliveryError string    `json:"delivery_error,omitempty"`
}

// Decision records the router's response. Currently only explicit local requests are accepted.
type Decision struct {
	TriggerID     string    `json:"trigger_id"`
	Outcome       string    `json:"outcome"`
	Reason        string    `json:"reason"`
	PolicyVersion string    `json:"policy_version,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}
type Event struct {
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
}
type Thread struct {
	ID        string     `json:"id"`
	Triggers  []Trigger  `json:"triggers"`
	Decisions []Decision `json:"decisions"`
}

// Attempt records one execution separately from the task mandate.
type Attempt struct {
	ID         string    `json:"id"`
	Agent      string    `json:"agent"`
	Status     Status    `json:"status"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Output     string    `json:"output,omitempty"`
	Error      string    `json:"error,omitempty"`
}

type Task struct {
	Isolated           bool           `json:"isolated,omitempty"`
	ExecutionWorkspace string         `json:"execution_workspace,omitempty"`
	IsolationCommit    string         `json:"isolation_commit,omitempty"`
	Activities         []ToolActivity `json:"activities,omitempty"`
	Reports            []Report       `json:"reports,omitempty"`
	ContextPaused      bool           `json:"context_paused,omitempty"`
	ID                 string         `json:"id"`
	ThreadID           string         `json:"thread_id,omitempty"`
	Context            []Trigger      `json:"context,omitempty"`
	Attempts           []Attempt      `json:"attempts,omitempty"`
	ScheduleRequested  bool           `json:"schedule_requested,omitempty"`
	ScheduledAt        time.Time      `json:"scheduled_at,omitempty"`
	Title              string         `json:"title"`
	Prompt             string         `json:"prompt"`
	Workspace          string         `json:"workspace"`
	Status             Status         `json:"status"`
	Agent              string         `json:"agent"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
	Output             string         `json:"output,omitempty"`
	Error              string         `json:"error,omitempty"`
	Trigger            Trigger        `json:"trigger"`
	Decision           Decision       `json:"decision"`
	Events             []Event        `json:"events"`
}
type state struct {
	Tasks   []Task   `json:"tasks"`
	Threads []Thread `json:"threads,omitempty"`
}

// Runner is the extension point for future agent implementations.
type Runner interface {
	Run(context.Context, Task, io.Writer) error
}

// LiveRunner accepts contextual follow-ups for a running execution.
// A successful acknowledgement means the runner accepted the message, not that it was consumed.
type LiveRunner interface {
	RunLive(context.Context, Task, io.Writer, <-chan Trigger, func(string, bool, string)) error
}

type Service struct {
	mu         sync.Mutex
	path       string
	data       state
	runners    map[string]Runner
	cancel     map[string]context.CancelFunc
	wg         sync.WaitGroup
	closing    bool
	persistErr error
	limit      int
	live       map[string]chan Trigger
	pending    map[string]*pendingRequest
}

func Open(path string) (*Service, error) {
	if path == "" {
		return nil, errors.New("state path is required")
	}
	s := &Service{path: path, runners: map[string]Runner{"pi": PiRunner{}}, cancel: map[string]context.CancelFunc{}, limit: defaultConcurrency, live: map[string]chan Trigger{}, pending: map[string]*pendingRequest{}}
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if err = json.Unmarshal(b, &s.data); err != nil {
			return nil, fmt.Errorf("read state: %w", err)
		}
	}
	changed := false
	for i := range s.data.Tasks {
		t := &s.data.Tasks[i]
		for j := range t.Context {
			if t.Context[j].Delivery == "pending" {
				t.Context[j].Delivery = "unknown"
				t.Context[j].DeliveryError = "orchestrator stopped before acknowledgement"
				changed = true
			}
		}
		if t.ThreadID == "" {
			id, idErr := newID()
			if idErr != nil {
				return nil, idErr
			}
			t.ThreadID = id
			s.data.Threads = append(s.data.Threads, Thread{ID: id, Triggers: []Trigger{t.Trigger}, Decisions: []Decision{t.Decision}})
			changed = true
		}
		if len(t.Attempts) == 0 {
			for _, event := range t.Events {
				if event.Kind == "started" {
					id, idErr := newID()
					if idErr != nil {
						return nil, idErr
					}
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
	if changed {
		if err = s.save(); err != nil {
			return nil, err
		}
	}
	return s, nil
}
func (s *Service) RegisterRunner(name string, runner Runner) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runners[name] = runner
}
func newID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func (s *Service) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".state-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, s.path)
}
func clone(t Task) Task {
	t.Activities = cloneActivities(t.Activities)
	t.Events = append([]Event(nil), t.Events...)
	t.Reports = append([]Report(nil), t.Reports...)
	t.Context = append([]Trigger(nil), t.Context...)
	t.Attempts = append([]Attempt(nil), t.Attempts...)
	return t
}
func (s *Service) ListTasks() []Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Task, len(s.data.Tasks))
	for i, t := range s.data.Tasks {
		result[i] = clone(t)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	return result
}
func (s *Service) GetTask(id string) (Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.data.Tasks {
		if t.ID == id {
			return clone(t), true
		}
	}
	return Task{}, false
}

// CreateTask routes an explicit manual request into a ticket with an immutable mandate.
func (s *Service) CreateTask(title, prompt, workspace string) (Task, error) {
	return s.createTask(title, prompt, workspace, "explicit local request", "")
}

// createTask persists an accepted request. Routing details are optional so the
// deterministic router can record its policy version and assignment events in
// the same write as the acceptance decision.
func (s *Service) createTask(title, prompt, workspace, reason, policy string, routedMessages ...string) (Task, error) {
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
	threadID, err := newID()
	if err != nil {
		return Task{}, err
	}
	now := time.Now().UTC()
	events := []Event{{now, "accepted", "Manual request accepted"}}
	for _, message := range routedMessages {
		events = append(events, Event{now, "routed", message})
	}
	t := Task{ID: id, ThreadID: threadID, Title: title, Prompt: prompt, Workspace: abs, Status: Queued, Agent: "pi", CreatedAt: now, UpdatedAt: now, Trigger: Trigger{ID: triggerID, Source: "manual", Text: prompt, CreatedAt: now}, Decision: Decision{TriggerID: triggerID, Outcome: "accept", Reason: reason, PolicyVersion: policy, CreatedAt: now}, Events: events}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Tasks = append(s.data.Tasks, t)
	s.data.Threads = append(s.data.Threads, Thread{ID: threadID, Triggers: []Trigger{t.Trigger}, Decisions: []Decision{t.Decision}})
	if err = s.save(); err != nil {
		s.data.Threads = s.data.Threads[:len(s.data.Threads)-1]
		s.data.Tasks = s.data.Tasks[:len(s.data.Tasks)-1]
		return Task{}, err
	}
	return clone(t), nil
}
func (s *Service) Start(ctx context.Context, id string) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errors.New("service is shutting down")
	}
	for i := range s.data.Tasks {
		t := &s.data.Tasks[i]
		if t.ID != id {
			continue
		}
		if t.Status != Queued {
			return fmt.Errorf("task is %s, not queued", t.Status)
		}
		if t.ScheduleRequested {
			return errors.New("task is already scheduled")
		}
		if _, ok := s.runners[t.Agent]; !ok {
			return fmt.Errorf("agent %q unavailable", t.Agent)
		}
		if len(s.cancel) < s.limit {
			return s.startLocked(ctx, t)
		}
		old := clone(*t)
		t.ScheduleRequested = true
		t.ScheduledAt = time.Now().UTC()
		t.UpdatedAt = t.ScheduledAt
		t.Events = append(t.Events, Event{t.UpdatedAt, "scheduled", "Waiting for agent capacity"})
		if err := s.save(); err != nil {
			*t = old
			return err
		}
		request := &pendingRequest{ctx: ctx, stop: make(chan struct{})}
		s.pending[id] = request
		if ctx.Done() != nil {
			go s.watchPending(id, request)
		}
		return nil
	}
	return errors.New("task not found")
}

const maxOutput = 256 * 1024

type outputWriter struct {
	s  *Service
	id string
}

func (w outputWriter) Write(p []byte) (int, error) {
	w.s.mu.Lock()
	defer w.s.mu.Unlock()
	for i := range w.s.data.Tasks {
		t := &w.s.data.Tasks[i]
		if t.ID == w.id {
			t.Output += string(p)
			if len(t.Output) > maxOutput {
				t.Output = t.Output[len(t.Output)-maxOutput:]
			}
			t.UpdatedAt = time.Now().UTC()
			if len(t.Attempts) > 0 {
				t.Attempts[len(t.Attempts)-1].Output = t.Output
			}
			if err := w.s.save(); err != nil {
				return 0, err
			}
			break
		}
	}
	return len(p), nil
}
func (s *Service) run(ctx context.Context, t Task, r Runner) {
	defer s.wg.Done()
	var err error
	t, err = s.prepareExecution(ctx, t)
	if err == nil {
		if lr, ok := r.(LiveRunner); ok {
			s.mu.Lock()
			live := s.live[t.ID]
			s.mu.Unlock()
			err = lr.RunLive(ctx, t, outputWriter{s, t.ID}, live, func(id string, accepted bool, reason string) { s.contextAck(t.ID, id, accepted, reason) })
		} else {
			err = r.Run(ctx, t, outputWriter{s, t.ID})
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cancel, t.ID)
	delete(s.live, t.ID)
	for i := range s.data.Tasks {
		task := &s.data.Tasks[i]
		if task.ID != t.ID {
			continue
		}
		task.UpdatedAt = time.Now().UTC()
		interruptActivities(task)
		for i := range task.Context {
			if task.Context[i].Delivery == "pending" {
				task.Context[i].Delivery = "unknown"
				task.Context[i].DeliveryError = "execution ended before acknowledgement"
			}
		}
		switch {
		case ctx.Err() != nil:
			task.Status = Canceled
			task.Error = ctx.Err().Error()
		case err != nil:
			task.Status = Failed
			task.Error = err.Error()
		default:
			task.Status = Completed
		}
		if len(task.Attempts) > 0 {
			a := &task.Attempts[len(task.Attempts)-1]
			a.Status = task.Status
			a.FinishedAt = task.UpdatedAt
			a.Error = task.Error
		}
		task.Events = append(task.Events, Event{task.UpdatedAt, string(task.Status), task.Error})
		if err := s.save(); err != nil {
			s.persistErr = err
		} else if !s.closing {
			_ = s.scheduleLocked()
		}
		break
	}
}
func (s *Service) Cancel(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Tasks {
		t := &s.data.Tasks[i]
		if t.ID != id {
			continue
		}
		if t.Status == Queued {
			old := clone(*t)
			t.ScheduleRequested = false
			t.Status = Canceled
			t.UpdatedAt = time.Now().UTC()
			t.Events = append(t.Events, Event{t.UpdatedAt, "canceled", "Canceled before start"})
			if err := s.save(); err != nil {
				*t = old
				return err
			}
			s.removePendingLocked(id)
			return nil
		}
		if cancel, ok := s.cancel[id]; ok {
			cancel()
			return nil
		}
		return fmt.Errorf("task is %s", t.Status)
	}
	return errors.New("task not found")
}

// Shutdown cancels active runs and waits for their processes and final state writes.
func (s *Service) Shutdown() error {
	s.mu.Lock()
	s.closing = true
	for id := range s.pending {
		s.removePendingLocked(id)
	}
	for _, cancel := range s.cancel {
		cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistErr
}

// AddContext records a manual follow-up for a queued task. It does not change
// the original mandate. The trigger ID makes retries idempotent.
func (s *Service) AddContext(taskID, triggerID, text string) error {
	text = strings.TrimSpace(text)
	if text == "" || triggerID == "" {
		return errors.New("trigger ID and context are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errors.New("service is shutting down")
	}
	for ti := range s.data.Threads {
		thread := &s.data.Threads[ti]
		for _, existing := range thread.Triggers {
			if existing.ID == triggerID {
				if existing.Text == text && thread.ID != "" {
					for _, task := range s.data.Tasks {
						if task.ID == taskID && task.ThreadID == thread.ID {
							return nil
						}
					}
				}
				return errors.New("trigger ID already exists")
			}
		}
	}
	for i := range s.data.Tasks {
		task := &s.data.Tasks[i]
		if task.ID != taskID {
			continue
		}
		if task.Status != Queued && task.Status != Running {
			return fmt.Errorf("context can only be added to queued or running tasks; task is %s", task.Status)
		}
		var live chan Trigger
		if task.Status == Running {
			live = s.live[task.ID]
			if live == nil {
				return errors.New("agent does not support live context")
			}
			if len(live) == cap(live) {
				return errors.New("live context queue is full")
			}
		}
		for j := range s.data.Threads {
			thread := &s.data.Threads[j]
			if thread.ID != task.ThreadID {
				continue
			}
			now := time.Now().UTC()
			trigger := Trigger{ID: triggerID, Source: "manual", Text: text, CreatedAt: now}
			decision := Decision{TriggerID: triggerID, Outcome: "context", Reason: "manual follow-up; does not expand mandate", CreatedAt: now}
			if live != nil {
				trigger.Delivery = "pending"
			} else {
				trigger.Delivery = "at_start"
			}
			old := clone(*task)
			task.Context = append(task.Context, trigger)
			task.UpdatedAt = now
			task.Events = append(task.Events, Event{now, "context", "Manual context added"})
			thread.Triggers = append(thread.Triggers, trigger)
			thread.Decisions = append(thread.Decisions, decision)
			if err := s.save(); err != nil {
				*task = old
				thread.Triggers = thread.Triggers[:len(thread.Triggers)-1]
				thread.Decisions = thread.Decisions[:len(thread.Decisions)-1]
				return err
			}
			if live != nil {
				live <- trigger
			}
			return nil
		}
		return errors.New("task thread not found")
	}
	return errors.New("task not found")
}

// AddManualContext creates a unique trigger for a local follow-up.
func (s *Service) AddManualContext(taskID, text string) error {
	id, err := newID()
	if err != nil {
		return err
	}
	return s.AddContext(taskID, id, text)
}

// Retry returns an unsuccessful task to the queue without changing its mandate.
func (s *Service) Retry(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errors.New("service is shutting down")
	}
	for i := range s.data.Tasks {
		t := &s.data.Tasks[i]
		if t.ID != id {
			continue
		}
		if t.Status != Failed && t.Status != Interrupted && t.Status != Canceled {
			return fmt.Errorf("cannot retry task in %s state", t.Status)
		}
		old := clone(*t)
		t.Status = Queued
		t.UpdatedAt = time.Now().UTC()
		t.Error = ""
		t.Events = append(t.Events, Event{t.UpdatedAt, "requeued", "Manual retry requested"})
		if err := s.save(); err != nil {
			*t = old
			return err
		}
		return nil
	}
	return errors.New("task not found")
}

func (s *Service) contextAck(taskID, triggerID string, accepted bool, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Tasks {
		task := &s.data.Tasks[i]
		if task.ID != taskID {
			continue
		}
		for j := range task.Context {
			trigger := &task.Context[j]
			if trigger.ID != triggerID || trigger.Delivery != "pending" {
				continue
			}
			old := clone(*task)
			if accepted {
				trigger.Delivery = "accepted"
			} else {
				trigger.Delivery = "rejected"
				trigger.DeliveryError = reason
			}
			task.Events = append(task.Events, Event{time.Now().UTC(), "context_" + trigger.Delivery, "Context " + trigger.ID + ": " + reason})
			if err := s.save(); err != nil {
				*task = old
				s.persistErr = err
			}
			return
		}
	}
}
