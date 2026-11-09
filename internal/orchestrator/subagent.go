package orchestrator

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"time"
)

const (
	// SubagentPolicyVersion identifies the fixed policy that accepts a child
	// mandate delegated by a running task. A subagent is a new task with its own
	// mandate and attempt; delegation does not expand the parent's mandate.
	SubagentPolicyVersion = "subagent-1"

	// DefaultMaxSubagentDepth bounds how many delegation levels can run.
	DefaultMaxSubagentDepth = 2
	// DefaultMaxSubagents bounds how many child tasks one task may create.
	DefaultMaxSubagents = 4
)

// SetSubagentLimits configures delegation bounds. Zero disables spawning.
func (s *Service) SetSubagentLimits(maxDepth, maxChildren int) error {
	if maxDepth < 0 || maxChildren < 0 {
		return errors.New("subagent limits cannot be negative")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errors.New("service is shutting down")
	}
	s.subagentMaxDepth = maxDepth
	s.subagentMaxChildren = maxChildren
	return nil
}

// SpawnSubagent records and starts a child task for work delegated by a running
// parent. The child gets a separate thread, immutable mandate, decision and
// attempt. It does not inherit the parent's context or authority beyond the
// delegated mandate, and its lifecycle is independent after creation.
func (s *Service) SpawnSubagent(parentID, title, prompt, agent string) (Task, error) {
	return s.spawnSubagent(parentID, title, prompt, agent, "")
}

func (s *Service) spawnSubagent(parentID, title, prompt, agent, token string) (Task, error) {
	task, err := s.createSubagent(parentID, title, prompt, agent, token)
	if err != nil {
		return Task{}, err
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		s.noteSubagentStartFailure(task.ID, err)
		current, _ := s.GetTask(task.ID)
		return current, fmt.Errorf("subagent recorded but start failed: %w", err)
	}
	current, _ := s.GetTask(task.ID)
	return current, nil
}

func (s *Service) createSubagent(parentID, title, prompt, agent, token string) (Task, error) {
	title = strings.TrimSpace(title)
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return Task{}, errors.New("subagent mandate is required")
	}
	if title == "" {
		title = requestTitle(prompt)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return Task{}, errors.New("service is shutting down")
	}
	if s.subagentMaxDepth == 0 || s.subagentMaxChildren == 0 {
		return Task{}, errors.New("subagent spawning is disabled")
	}
	var parent *Task
	var parentIndex int
	for i := range s.data.Tasks {
		if s.data.Tasks[i].ID == parentID {
			parent = &s.data.Tasks[i]
			parentIndex = i
			break
		}
	}
	if parent == nil {
		return Task{}, errors.New("parent task not found")
	}
	if token != "" {
		if parent.controlToken == "" || subtle.ConstantTimeCompare([]byte(parent.controlToken), []byte(token)) != 1 {
			return Task{}, errors.New("invalid subagent credential")
		}
	}
	if parent.integrating {
		return Task{}, errors.New("parent worktree is being integrated")
	}
	if parent.Status != Running {
		return Task{}, fmt.Errorf("parent task is %s, not running", parent.Status)
	}
	depth := parent.Depth + 1
	if depth > s.subagentMaxDepth {
		return Task{}, fmt.Errorf("subagent depth limit reached (maximum %d)", s.subagentMaxDepth)
	}
	children := 0
	for _, task := range s.data.Tasks {
		if task.ParentID == parent.ID {
			children++
		}
	}
	if children >= s.subagentMaxChildren {
		return Task{}, fmt.Errorf("subagent limit reached (maximum %d child tasks)", s.subagentMaxChildren)
	}
	agent = strings.TrimSpace(agent)
	if agent == "" {
		agent = s.selectAgentLocked(prompt, parent.Agent)
	}
	if _, ok := s.runners[agent]; !ok {
		return Task{}, fmt.Errorf("agent %q unavailable", agent)
	}
	workspace := parent.ExecutionWorkspace
	if workspace == "" {
		if parent.Isolated {
			return Task{}, errors.New("parent isolated workspace is not ready")
		}
		workspace = parent.Workspace
	}
	info, err := os.Stat(workspace)
	if err != nil {
		return Task{}, fmt.Errorf("parent execution workspace: %w", err)
	}
	if !info.IsDir() {
		return Task{}, errors.New("parent execution workspace must be a directory")
	}
	id := newID()
	triggerID := newID()
	threadID := newID()
	now := time.Now().UTC()
	trigger := Trigger{ID: triggerID, Source: SubagentPolicySource, Text: prompt, CreatedAt: now}
	decision, err := s.authorizeLocked(AuthorizationRequest{
		Source:    SubagentPolicySource,
		Actor:     "task:" + parent.ID,
		Purpose:   PurposeSubagent,
		TriggerID: triggerID,
		ThreadID:  threadID,
		ParentID:  parent.ID,
		Text:      prompt,
		Workspace: workspace,
		Depth:     depth,
	})
	if err != nil {
		return Task{}, err
	}
	decision.CreatedAt = now
	task := Task{
		ID:        id,
		ThreadID:  threadID,
		ParentID:  parent.ID,
		Depth:     depth,
		Title:     title,
		Prompt:    prompt,
		Workspace: workspace,
		Status:    Queued,
		Agent:     agent,
		CreatedAt: now,
		UpdatedAt: now,
		Trigger:   trigger,
		Decision:  decision,
		Events: []Event{
			{now, "accepted", "Subagent mandate accepted from running task " + parent.ID},
			{now, "delegated", "Policy " + SubagentPolicyVersion + " · depth " + fmt.Sprint(depth) + " · workspace inherited from parent"},
		},
	}
	s.data.Tasks = append(s.data.Tasks, task)
	s.data.Threads = append(s.data.Threads, Thread{ID: threadID, Triggers: []Trigger{trigger}, Decisions: []Decision{decision}})
	s.data.Tasks[parentIndex].Events = append(s.data.Tasks[parentIndex].Events, Event{now, "subagent", "Subagent spawned: " + id + " · " + title})
	if err = s.save(); err != nil {
		s.data.Tasks = s.data.Tasks[:len(s.data.Tasks)-1]
		s.data.Threads = s.data.Threads[:len(s.data.Threads)-1]
		s.data.Tasks[parentIndex].Events = s.data.Tasks[parentIndex].Events[:len(s.data.Tasks[parentIndex].Events)-1]
		return Task{}, err
	}
	return clone(task), nil
}

func (s *Service) noteSubagentStartFailure(id string, cause error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Tasks {
		t := &s.data.Tasks[i]
		if t.ID != id {
			continue
		}
		old := clone(*t)
		now := time.Now().UTC()
		t.UpdatedAt = now
		t.Events = append(t.Events, Event{now, "start_failed", cause.Error()})
		if err := s.save(); err != nil {
			*t = old
			s.persistErr = err
		}
		return
	}
}

// subagentGuidance is trusted orchestrator text appended to the initial prompt.
// It describes a bounded capability and grants no authority beyond delegation.
func (s *Service) subagentGuidanceLocked(remainingDepth, maxChildren int) string {
	if remainingDepth <= 0 || maxChildren <= 0 {
		return ""
	}
	var guidance strings.Builder
	fmt.Fprintf(&guidance, `

Subagent delegation is available while this task runs:
  "$ORCHESTRATOR_SUBAGENT_CLI" subagent --title "short title"
Pass the delegated instructions on stdin. The command waits for the child and prints its result; add --no-wait to return immediately with the child task ID. The child has its own mandate and fresh context, shares this workspace, and inherits no authority from this conversation. Delegation cannot widen the original mandate. Remaining depth: %d; at most %d child task(s) for this task.

Choose a registered executor with --agent NAME when its specialty fits the child mandate. Omit --agent for automatic specialist selection, falling back to the parent executor.
Available executors:
`, remainingDepth, maxChildren)
	for _, name := range slices.Sorted(maps.Keys(s.runners)) {
		fmt.Fprintf(&guidance, "- %q", name)
		if description := s.agentDescriptions[name]; description != "" {
			fmt.Fprintf(&guidance, " — %s", description)
		}
		guidance.WriteByte('\n')
	}
	return guidance.String()
}

var controlEnvironmentKeys = map[string]bool{
	"ORCHESTRATOR_CONTROL_SOCKET": true,
	"ORCHESTRATOR_TASK_ID":        true,
	"ORCHESTRATOR_SUBAGENT_TOKEN": true,
	"ORCHESTRATOR_SUBAGENT_CLI":   true,
}

func (t Task) controlEnvironment() []string {
	if t.controlSocket == "" || t.controlToken == "" {
		return nil
	}
	env := []string{
		"ORCHESTRATOR_CONTROL_SOCKET=" + t.controlSocket,
		"ORCHESTRATOR_TASK_ID=" + t.ID,
		"ORCHESTRATOR_SUBAGENT_TOKEN=" + t.controlToken,
	}
	if exe, err := os.Executable(); err == nil {
		env = append(env, "ORCHESTRATOR_SUBAGENT_CLI="+exe)
	}
	return env
}

// runnerEnvironment removes inherited control credentials before adding the
// current attempt's credentials. This prevents an orchestrator started inside
// another task from forwarding stale credentials to its own agents.
func (t Task) runnerEnvironment() []string {
	env := os.Environ()
	filtered := make([]string, 0, len(env)+4)
	for _, item := range env {
		key, _, _ := strings.Cut(item, "=")
		if controlEnvironmentKeys[key] {
			continue
		}
		filtered = append(filtered, item)
	}
	return append(filtered, t.controlEnvironment()...)
}

// waitForSubagent waits for a child to finish. If capacity is unavailable, a
// queued child is returned after startTimeout so a parent does not hold a slot
// while waiting for a slot that cannot open.
func (s *Service) waitForSubagent(ctx context.Context, id string, startTimeout time.Duration) (Task, error) {
	if startTimeout < 0 {
		startTimeout = 0
	}
	deadline := time.Now().Add(startTimeout)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		task, ok := s.GetTask(id)
		if !ok {
			return Task{}, errors.New("subagent task not found")
		}
		switch task.Status {
		case Completed, Failed, Canceled, Interrupted:
			return task, nil
		case Running:
			deadline = time.Time{}
		case Queued:
			if !deadline.IsZero() && time.Now().After(deadline) {
				return task, nil
			}
		}
		select {
		case <-ctx.Done():
			return task, ctx.Err()
		case <-ticker.C:
		}
	}
}
