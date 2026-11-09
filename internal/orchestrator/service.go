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
	"net"
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

// Decision records the router's response and the authorization evidence behind
// it. PolicyID, PolicyVersion, Source, Actor and InputDigest make the decision
// auditable without storing a second copy of the mandate.
type Decision struct {
	TriggerID     string    `json:"trigger_id"`
	Outcome       string    `json:"outcome"`
	Reason        string    `json:"reason"`
	PolicyID      string    `json:"policy_id,omitempty"`
	PolicyVersion string    `json:"policy_version,omitempty"`
	Source        string    `json:"source,omitempty"`
	Actor         string    `json:"actor,omitempty"`
	InputDigest   string    `json:"input_digest,omitempty"`
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
	Profile    string    `json:"profile,omitempty"`
	Status     Status    `json:"status"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Output     string    `json:"output,omitempty"`
	Error      string    `json:"error,omitempty"`
}

type Task struct {
	DiscardPending     bool           `json:"discard_pending,omitempty"`
	Isolated           bool           `json:"isolated,omitempty"`
	ExecutionWorkspace string         `json:"execution_workspace,omitempty"`
	IsolationCommit    string         `json:"isolation_commit,omitempty"`
	MergeStatus        string         `json:"merge_status,omitempty"`
	MergeError         string         `json:"merge_error,omitempty"`
	MergeCommit        string         `json:"merge_commit,omitempty"`
	WorktreeCommit     string         `json:"worktree_commit,omitempty"`
	MergedAt           time.Time      `json:"merged_at,omitempty"`
	ReviewStatus       string         `json:"review_status,omitempty"`
	ReviewCommit       string         `json:"review_commit,omitempty"`
	ReviewedBy         string         `json:"reviewed_by,omitempty"`
	ReviewedAt         time.Time      `json:"reviewed_at,omitempty"`
	ReviewNote         string         `json:"review_note,omitempty"`
	Activities         []ToolActivity `json:"activities,omitempty"`
	Reports            []Report       `json:"reports,omitempty"`
	Deliveries         []Delivery     `json:"deliveries,omitempty"`
	ContextPaused      bool           `json:"context_paused,omitempty"`
	ID                 string         `json:"id"`
	ThreadID           string         `json:"thread_id,omitempty"`
	ParentID           string         `json:"parent_id,omitempty"`
	Depth              int            `json:"depth,omitempty"`
	Context            []Trigger      `json:"context,omitempty"`
	Attempts           []Attempt      `json:"attempts,omitempty"`
	ScheduleRequested  bool           `json:"schedule_requested,omitempty"`
	ScheduledAt        time.Time      `json:"scheduled_at,omitempty"`
	Title              string         `json:"title"`
	Prompt             string         `json:"prompt"`
	Workspace          string         `json:"workspace"`
	Status             Status         `json:"status"`
	Agent              string         `json:"agent"`
	// ProfileName records the chosen profile and ProfileSpec freezes the
	// definition accepted with the task, so later catalog edits cannot change it.
	ProfileName string    `json:"profile,omitempty"`
	ProfileSpec *Profile  `json:"profile_spec,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Output      string    `json:"output,omitempty"`
	Error       string    `json:"error,omitempty"`
	Trigger     Trigger   `json:"trigger"`
	Decision    Decision  `json:"decision"`
	Events      []Event   `json:"events"`

	// Control credentials are process-local: they are never written to the state
	// file and are cleared when the attempt ends.
	integrating     bool // guards this retained worktree while Git runs outside mu
	controlSocket   string
	controlToken    string
	controlGuidance string
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
	mu       sync.Mutex
	mergeMu  sync.Mutex
	path     string
	data     state
	runners  map[string]Runner
	profiles map[string]Profile
	// routeProfile is the profile the local router assigns, and the fallback for
	// tasks created without an explicit choice.
	routeProfile      string
	cancel            map[string]context.CancelFunc
	discarding        map[string]bool
	wg                sync.WaitGroup
	closing           bool
	persistErr        error
	limit             int
	live              map[string]chan Trigger
	pending           map[string]*pendingRequest
	policies          map[string]Policy
	agentDescriptions map[string]string

	defaultIsolation bool

	subagentMaxDepth    int
	subagentMaxChildren int
	controlPath         string
	controlListener     net.Listener
	controlCancel       context.CancelFunc
	controlConns        map[net.Conn]struct{}
	controlWG           sync.WaitGroup
}

func Open(path string) (*Service, error) {
	if path == "" {
		return nil, errors.New("state path is required")
	}
	s := &Service{
		path:              path,
		runners:           map[string]Runner{DefaultRouteAgent: PiRunner{}},
		profiles:          builtinProfiles(),
		routeProfile:      DefaultProfileName,
		cancel:            make(map[string]context.CancelFunc),
		discarding:        make(map[string]bool),
		limit:             defaultConcurrency,
		live:              make(map[string]chan Trigger),
		pending:           make(map[string]*pendingRequest),
		defaultIsolation:  true,
		agentDescriptions: map[string]string{DefaultRouteAgent: "General-purpose coding agent."},
		policies: map[string]Policy{
			LocalPolicySource:    localPolicy{},
			SubagentPolicySource: subagentPolicy{},
		},
		subagentMaxDepth:    DefaultMaxSubagentDepth,
		subagentMaxChildren: DefaultMaxSubagents,
		controlConns:        make(map[net.Conn]struct{}),
	}
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
		if t.DiscardPending {
			gone, err := reconcileDiscard(path, *t)
			if err != nil {
				return nil, fmt.Errorf("recover worktree discard for task %s: %w", t.ID, err)
			}
			t.finishDiscard(gone, "Interrupted discard; worktree retained")
			changed = true
		}
		if t.settleAllPendingDeliveries("orchestrator stopped before acknowledgement", time.Now().UTC()) {
			changed = true
		}
		if t.ThreadID == "" {
			id := newID()
			t.ThreadID = id
			s.data.Threads = append(s.data.Threads, Thread{ID: id, Triggers: []Trigger{t.Trigger}, Decisions: []Decision{t.Decision}})
			changed = true
		}
		if len(t.Attempts) == 0 {
			for _, event := range t.Events {
				if event.Kind == "started" {
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
func newID() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
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
	t.ProfileSpec = cloneProfileRef(t.ProfileSpec)
	t.Activities = cloneActivities(t.Activities)
	t.Events = append([]Event(nil), t.Events...)
	t.Reports = append([]Report(nil), t.Reports...)
	t.Deliveries = append([]Delivery(nil), t.Deliveries...)
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

// SetDefaultIsolation controls whether newly created tasks use a dedicated
// worktree when their workspace supports isolation. It does not change tasks
// that already exist or tasks whose workspace cannot be isolated.
func (s *Service) SetDefaultIsolation(enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errors.New("service is shutting down")
	}
	s.defaultIsolation = enabled
	return nil
}

func (s *Service) defaultTaskIsolation(workspace string) bool {
	s.mu.Lock()
	enabled := s.defaultIsolation
	s.mu.Unlock()
	return enabled && workspaceIsolatable(workspace)
}

// CreateTask routes an explicit manual request into a ticket with an immutable mandate.
func (s *Service) CreateTask(title, prompt, workspace string) (Task, error) {
	return s.CreateTaskWithProfile(title, prompt, workspace, "")
}

// CreateTaskWithProfile creates a manual request assigned to a named profile. An
// empty name uses the routing default. The resolved profile is snapshotted.
func (s *Service) CreateTaskWithProfile(title, prompt, workspace, profile string) (Task, error) {
	return s.createTaskWithIsolation(title, prompt, workspace, s.defaultTaskIsolation(workspace),
		AuthorizationRequest{Source: LocalPolicySource, Actor: LocalActor, Purpose: PurposeManual}, s.selectAgent(prompt), profile)
}

// createTaskWithIsolation persists an accepted request after the source's
// authorization policy approves it. Routing details are optional so the
// deterministic router can record its assignment events in the same write as
// the decision.
func (s *Service) createTaskWithIsolation(title, prompt, workspace string, isolated bool, auth AuthorizationRequest, agent, profile string, routedMessages ...string) (Task, error) {
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
	id := newID()
	triggerID := newID()
	threadID := newID()
	now := time.Now().UTC()
	auth.TriggerID, auth.ThreadID, auth.Text, auth.Workspace = triggerID, threadID, prompt, abs
	decision, err := s.authorize(auth)
	if err != nil {
		return Task{}, err
	}
	s.mu.Lock()
	profileName, profileSpec, err := s.resolveProfileLocked(profile)
	s.mu.Unlock()
	if err != nil {
		return Task{}, err
	}
	decision.CreatedAt = now
	events := []Event{{now, "accepted", "Manual request accepted"}}
	for _, message := range routedMessages {
		events = append(events, Event{now, "routed", message})
	}
	if isolated {
		events = append(events, Event{now, "workspace", "Isolated committed-HEAD worktree selected by default; uncommitted files are excluded"})
	}
	t := Task{Isolated: isolated, ID: id, ThreadID: threadID, Title: title, Prompt: prompt, Workspace: abs, Status: Queued, Agent: agent, ProfileName: profileName, ProfileSpec: profileSpec, CreatedAt: now, UpdatedAt: now, Trigger: Trigger{ID: triggerID, Source: auth.Source, Text: prompt, CreatedAt: now}, Decision: decision, Events: events}
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
		if t.DiscardPending {
			return errors.New("worktree is being discarded; wait before starting the task")
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
		profile, err := s.profileForTaskLocked(*t)
		if err != nil {
			return err
		}
		if err = validateProfileForRunner(s.runners[t.Agent], profile); err != nil {
			return err
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
	s         *Service
	taskID    string
	attemptID string
}

// taskLocked resolves the attempt that owns this sink. An adapter retaining a
// callback after Run returns must never be able to modify a later attempt.
func (w outputWriter) taskLocked() (*Task, error) {
	for i := range w.s.data.Tasks {
		t := &w.s.data.Tasks[i]
		if t.ID != w.taskID {
			continue
		}
		if t.Status != Running || len(t.Attempts) == 0 || t.currentAttemptID() != w.attemptID {
			return nil, errors.New("execution attempt is no longer running")
		}
		return t, nil
	}
	return nil, errors.New("task not found")
}

func (w outputWriter) Write(p []byte) (int, error) {
	w.s.mu.Lock()
	defer w.s.mu.Unlock()
	t, err := w.taskLocked()
	if err != nil {
		return 0, err
	}
	attempt := &t.Attempts[len(t.Attempts)-1]
	oldOutput, oldAttemptOutput, oldUpdatedAt := t.Output, attempt.Output, t.UpdatedAt
	t.Output += string(p)
	if len(t.Output) > maxOutput {
		t.Output = t.Output[len(t.Output)-maxOutput:]
	}
	t.UpdatedAt = time.Now().UTC()
	attempt.Output = t.Output
	if err := w.s.save(); err != nil {
		t.Output, attempt.Output, t.UpdatedAt = oldOutput, oldAttemptOutput, oldUpdatedAt
		return 0, err
	}
	return len(p), nil
}

func (s *Service) run(ctx context.Context, t Task, r Runner) {
	defer s.wg.Done()
	var err error
	t, err = s.prepareExecution(ctx, t)
	if err == nil {
		writer := outputWriter{s: s, taskID: t.ID, attemptID: t.currentAttemptID()}
		if lr, ok := r.(LiveRunner); ok {
			s.mu.Lock()
			live := s.live[t.ID]
			s.mu.Unlock()
			err = lr.RunLive(ctx, t, writer, live, writer.contextAck)
		} else {
			err = r.Run(ctx, t, writer)
		}
	}
	s.mu.Lock()
	merge := mergeOutcome{}
	integrationSkipped := false
	if err == nil && ctx.Err() == nil && t.Isolated {
		if t.noAutoIntegrate() {
			integrationSkipped = true
		} else {
			for i := range s.data.Tasks {
				owner := &s.data.Tasks[i]
				if owner.ID != t.ID {
					continue
				}
				if s.hasInheritedWorkspaceLocked(*owner, true) {
					merge.Status = MergeWaiting
				} else {
					owner.integrating = true
					s.mu.Unlock()
					merge = s.autoMergeIsolated(t)
					s.mu.Lock()
				}
				break
			}
		}
	}
	defer s.mu.Unlock()
	defer s.cancel[t.ID]()
	delete(s.cancel, t.ID)
	delete(s.live, t.ID)
	for i := range s.data.Tasks {
		task := &s.data.Tasks[i]
		if task.ID != t.ID {
			continue
		}
		task.UpdatedAt = time.Now().UTC()
		task.controlSocket, task.controlToken, task.controlGuidance = "", "", ""
		interruptActivities(task)
		attemptID := task.currentAttemptID()
		task.settlePendingDeliveries(attemptID, "execution ended before acknowledgement", task.UpdatedAt)
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
		task.integrating = false
		if integrationSkipped {
			task.Events = append(task.Events, Event{task.UpdatedAt, "integration_skipped", "Profile " + task.ProfileName + " does not integrate prepared work; worktree retained for the operator"})
		}
		if merge.Status != "" {
			task.recordMerge(merge)
		}
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
			s.resumeMergesLocked()
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
	s.closeSubagentControl()
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
	return s.addContextLocked(taskID, triggerID, text)
}

// addContextLocked records one context input for a queued or running task.
// Caller holds mu and has validated triggerID and text.
func (s *Service) addContextLocked(taskID, triggerID, text string) error {
	var task *Task
	for i := range s.data.Tasks {
		if s.data.Tasks[i].ID == taskID {
			task = &s.data.Tasks[i]
			break
		}
	}
	if task == nil {
		return errors.New("task not found")
	}
	// A retry must name the same recipient. A mandate or a thread-wide
	// trigger can have the same text, but came through a different route.
	for _, thread := range s.data.Threads {
		for _, existing := range thread.Triggers {
			if existing.ID != triggerID {
				continue
			}
			if thread.ID == task.ThreadID && existing.Text == text && !thread.isThreadContext(triggerID) {
				for _, c := range task.Context {
					if c.ID == triggerID {
						return nil
					}
				}
			}
			return errors.New("trigger ID already exists")
		}
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
		old := clone(*task)
		if live != nil {
			trigger.Delivery = DeliveryPending
			task.appendDelivery(triggerID, task.currentAttemptID(), DeliveryPending, "", now)
		} else {
			trigger.Delivery = DeliveryAtStart
		}
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

// AddManualContext creates a unique trigger for a local follow-up.
func (s *Service) AddManualContext(taskID, text string) error {
	return s.AddContext(taskID, newID(), text)
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
		if t.DiscardPending {
			return errors.New("worktree is being discarded; wait before retrying the task")
		}
		if err := s.checkRequeueIntegrationLocked(*t); err != nil {
			return err
		}
		mergeFailed := t.Status == Completed && t.MergeStatus == MergeFailed
		if t.Status != Failed && t.Status != Interrupted && t.Status != Canceled && !mergeFailed {
			return fmt.Errorf("cannot retry task in %s state", t.Status)
		}
		old := clone(*t)
		t.Status = Queued
		t.UpdatedAt = time.Now().UTC()
		t.Error = ""
		t.MergeStatus, t.MergeError, t.MergeCommit, t.WorktreeCommit = "", "", "", ""
		t.MergedAt = time.Time{}
		t.resetReview()
		message := "Manual retry requested"
		if mergeFailed {
			message = "Manual retry requested after automatic merge failure"
		}
		t.Events = append(t.Events, Event{t.UpdatedAt, "requeued", message})
		if err := s.save(); err != nil {
			*t = old
			return err
		}
		return nil
	}
	return errors.New("task not found")
}

func (w outputWriter) contextAck(triggerID string, accepted bool, reason string) {
	w.s.mu.Lock()
	defer w.s.mu.Unlock()
	task, err := w.taskLocked()
	if err != nil {
		return
	}
	for i := range task.Context {
		trigger := &task.Context[i]
		if trigger.ID != triggerID || trigger.Delivery != DeliveryPending {
			continue
		}
		state, deliveryError := DeliveryRejected, reason
		if accepted {
			state, deliveryError = DeliveryAccepted, ""
		}
		old := clone(*task)
		now := time.Now().UTC()
		trigger.Delivery, trigger.DeliveryError = state, deliveryError
		if !task.updatePendingDelivery(triggerID, w.attemptID, state, deliveryError, now) {
			task.appendDelivery(triggerID, w.attemptID, state, deliveryError, now)
		}
		task.Events = append(task.Events, Event{now, "context_" + state, "Context " + triggerID + ": " + reason})
		if err := w.s.save(); err != nil {
			*task = old
			w.s.persistErr = err
		}
		return
	}
}
