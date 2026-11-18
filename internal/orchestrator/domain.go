package orchestrator

import (
	"context"
	"io"
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
// it, auditable without storing a second copy of the mandate.
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

// Turn is one durable message from an attempt's tool conversation. Role is
// "assistant" or "tool". An assistant turn may carry text, a tool call, or
// both; a tool turn carries the call's result.
type Turn struct {
	Role       string `json:"role"`
	Text       string `json:"text,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	Input      string `json:"input,omitempty"`
	Output     string `json:"output,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
}

// Attempt records one execution separately from the task mandate.
type Attempt struct {
	ID              string    `json:"id"`
	Agent           string    `json:"agent"`
	Tools           []string  `json:"tools,omitempty"`
	Profile         string    `json:"profile,omitempty"`
	RequestedModel  string    `json:"requested_model,omitempty"`
	Status          Status    `json:"status"`
	StartedAt       time.Time `json:"started_at"`
	FinishedAt      time.Time `json:"finished_at,omitempty"`
	OutputTruncated bool      `json:"output_truncated,omitempty"`
	Output          string    `json:"output,omitempty"`
	// Turns is the structured tool conversation this attempt ran: assistant
	// text and tool calls with their results. The next attempt replays it so a
	// follow-up or retry keeps what earlier tool calls discovered, not just the
	// prose tail.
	Turns []Turn `json:"turns,omitempty"`
	Error string `json:"error,omitempty"`
	// Usage is nil when the adapter reported none, rather than implying the
	// attempt cost nothing.
	Usage *AttemptUsage `json:"usage,omitempty"`
}

type Task struct {
	DiscardPending bool `json:"discard_pending,omitempty"`
	// Isolated is the effective workspace choice; IsolationAuto records that
	// the start-up policy, not the operator, made it.
	Isolated           bool           `json:"isolated,omitempty"`
	IsolationAuto      bool           `json:"isolation_auto,omitempty"`
	ExecutionWorkspace string         `json:"execution_workspace,omitempty"`
	IsolationCommit    string         `json:"isolation_commit,omitempty"`
	MergeStatus        string         `json:"merge_status,omitempty"`
	MergeError         string         `json:"merge_error,omitempty"`
	MergeWarning       string         `json:"merge_warning,omitempty"`
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
	TitleGenerated     bool           `json:"title_generated,omitempty"` // display metadata written by the title model
	Prompt             string         `json:"prompt"`
	Workspace          string         `json:"workspace"`
	Status             Status         `json:"status"`
	Agent              string         `json:"agent"`
	// ProfileSpec freezes the profile accepted with the task, so catalog edits
	// cannot change it. SpecialistTools freezes the executor's ceiling and
	// InheritedTools keeps a delegated task within its parent's limits.
	SpecialistTools []string  `json:"specialist_tools,omitempty"`
	InheritedTools  []string  `json:"inherited_tools,omitempty"`
	ProfileName     string    `json:"profile,omitempty"`
	ProfileSpec     *Profile  `json:"profile_spec,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	Output          string    `json:"output,omitempty"`
	Error           string    `json:"error,omitempty"`
	Trigger         Trigger   `json:"trigger"`
	Decision        Decision  `json:"decision"`
	Events          []Event   `json:"events"`

	// Process-local: never written to the state file; control credentials are
	// cleared when the attempt ends.
	integrating     bool // guards this retained worktree while Git runs outside mu
	controlSocket   string
	controlToken    string
	controlGuidance string
	model           string // the selector recorded on the running attempt
	// sourceWorkspace is the operator's checkout when Workspace has been
	// replaced by an isolated worktree. Project instructions are read from it
	// so an agent cannot author the context a later attempt of the same task
	// will read.
	sourceWorkspace string
}

// stateVersion is written on every save. A file from a newer build fails
// loudly instead of silently dropping fields.
const stateVersion = 1

type state struct {
	Version int      `json:"version,omitempty"`
	Tasks   []Task   `json:"tasks"`
	Threads []Thread `json:"threads,omitempty"`
}

// Runner is the extension point for agent implementations.
type Runner interface {
	Run(context.Context, Task, io.Writer) error
}

// LiveRunner accepts contextual follow-ups for a running execution. A
// successful acknowledgement means the runner accepted the message, not that
// it was consumed.
type LiveRunner interface {
	RunLive(context.Context, Task, io.Writer, <-chan Trigger, func(string, bool, string)) error
}

func clone(t Task) Task {
	t.SpecialistTools = append([]string(nil), t.SpecialistTools...)
	t.InheritedTools = append([]string(nil), t.InheritedTools...)
	t.ProfileSpec = cloneProfileRef(t.ProfileSpec)
	t.Activities = cloneActivities(t.Activities)
	t.Events = append([]Event(nil), t.Events...)
	t.Reports = append([]Report(nil), t.Reports...)
	t.Deliveries = append([]Delivery(nil), t.Deliveries...)
	t.Context = append([]Trigger(nil), t.Context...)
	t.Attempts = append([]Attempt(nil), t.Attempts...)
	for i := range t.Attempts {
		t.Attempts[i].Tools = append([]string(nil), t.Attempts[i].Tools...)
		t.Attempts[i].Turns = append([]Turn(nil), t.Attempts[i].Turns...)
		if t.Attempts[i].Usage != nil {
			usage := *t.Attempts[i].Usage
			t.Attempts[i].Usage = &usage
		}
	}
	return t
}

func (t Task) currentAttemptID() string {
	if len(t.Attempts) == 0 {
		return ""
	}
	return t.Attempts[len(t.Attempts)-1].ID
}

// endAttempt records the outcome on both the task and its current attempt.
func (t *Task) endAttempt(status Status, reason string, now time.Time) {
	interruptActivities(t)
	t.Status, t.Error, t.UpdatedAt = status, reason, now
	if len(t.Attempts) > 0 {
		a := &t.Attempts[len(t.Attempts)-1]
		a.Status, a.FinishedAt, a.Error = status, now, reason
	}
	t.Events = append(t.Events, Event{now, string(status), reason})
}

// requeue returns an inactive task to the queue. A new attempt invalidates the
// previous integration outcome and review decision.
func (t *Task) requeue(now time.Time) {
	t.Status = Queued
	t.ScheduleRequested = false
	t.ScheduledAt = time.Time{}
	t.UpdatedAt = now
	t.Error = ""
	t.clearIntegration()
	t.resetReview()
}
