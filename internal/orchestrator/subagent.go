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
	// SubagentPolicyVersion identifies the authorization record written for a
	// child mandate delegated by a running task. A subagent is a new task with
	// its own mandate and attempt; delegation does not expand the parent's
	// mandate.
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
	return s.SpawnSubagentAsOperator(parentID, title, prompt, agent)
}

// SpawnSubagentAsOperator is the trusted in-process entry point used by the CLI
// and terminal UI. Control-socket callers must present the parent attempt's
// credential through spawnSubagent instead.
func (s *Service) SpawnSubagentAsOperator(parentID, title, prompt, agent string) (Task, error) {
	return s.spawnSubagentWithCredential(parentID, title, prompt, agent, "", "", true)
}

// SpawnSubagentWithProfile requests a child with a named profile. The child
// profile must not grant more than the parent's recorded profile; see
// checkDelegatedProfile.
func (s *Service) SpawnSubagentWithProfile(parentID, title, prompt, agent, profile string) (Task, error) {
	return s.spawnSubagentWithCredential(parentID, title, prompt, agent, profile, "", true)
}

// spawnSubagent handles control-socket requests. The credential is mandatory so
// a request cannot borrow the parent's authority merely by omitting its token.
func (s *Service) spawnSubagent(parentID, title, prompt, agent, profile, token string) (Task, error) {
	return s.spawnSubagentWithCredential(parentID, title, prompt, agent, profile, token, false)
}

func (s *Service) spawnSubagentWithCredential(parentID, title, prompt, agent, profile, token string, operator bool) (Task, error) {
	task, err := s.createSubagent(parentID, title, prompt, agent, profile, token, operator)
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

func (s *Service) createSubagent(parentID, title, prompt, agent, profile, token string, operator bool) (Task, error) {
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
	if !operator {
		if token == "" {
			return Task{}, errors.New("subagent credential required")
		}
		if parent.controlToken == "" || subtle.ConstantTimeCompare([]byte(parent.controlToken), []byte(token)) != 1 {
			return Task{}, errors.New("invalid subagent credential")
		}
	}
	if parent.integrating {
		return Task{}, errors.New("parent worktree is being integrated")
	}
	parentProfile, err := s.profileForTaskLocked(*parent)
	if err != nil {
		return Task{}, err
	}
	if parentProfile.NoSubagents {
		return Task{}, fmt.Errorf("profile %q does not allow delegation", parentProfile.Name)
	}
	if parent.Status != Running {
		return Task{}, fmt.Errorf("parent task is %s, not running", parent.Status)
	}
	depth := parent.Depth + 1
	if depth > s.subagentMaxDepth {
		return Task{}, fmt.Errorf("subagent depth limit reached (maximum %d)", s.subagentMaxDepth)
	}
	if s.subagentChildCountLocked(parent.ID) >= s.subagentMaxChildren {
		return Task{}, fmt.Errorf("subagent limit reached (maximum %d child tasks)", s.subagentMaxChildren)
	}
	agent = strings.TrimSpace(agent)
	if agent == "" {
		agent = s.selectAgentLocked(prompt, parent.Agent)
	}
	runner, ok := s.runners[agent]
	if !ok {
		return Task{}, fmt.Errorf("agent %q unavailable", agent)
	}
	childProfile, profileErr := s.childProfileLocked(parentProfile, parent.Agent, agent, profile)
	if profileErr != nil {
		return Task{}, profileErr
	}
	limits := Task{Agent: agent, InheritedTools: parentProfile.Tools, SpecialistTools: s.specialistTools[agent]}
	effective, profileErr := s.limitTaskProfileLocked(limits, childProfile)
	if profileErr != nil {
		return Task{}, profileErr
	}
	if profileErr = validateProfileForRunner(runner, effective); profileErr != nil {
		return Task{}, profileErr
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
	decision, err := s.authorize(AuthorizationRequest{
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
		ID:              id,
		ThreadID:        threadID,
		ParentID:        parent.ID,
		Depth:           depth,
		Title:           title,
		Prompt:          prompt,
		Workspace:       workspace,
		Status:          Queued,
		Agent:           agent,
		ProfileName:     childProfile.Name,
		ProfileSpec:     &childProfile,
		InheritedTools:  limits.InheritedTools,
		SpecialistTools: limits.SpecialistTools,
		CreatedAt:       now,
		UpdatedAt:       now,
		Trigger:         trigger,
		Decision:        decision,
		Events: []Event{
			{now, "accepted", "Subagent mandate accepted from running task " + parent.ID},
			{now, "delegated", "Authorization " + SubagentPolicyVersion + " · depth " + fmt.Sprint(depth) + " · profile " + childProfile.Name + " · workspace inherited from parent"},
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

// childProfileLocked resolves the profile a delegated child runs with. An empty
// request inherits the parent's snapshot. A different executor requires the
// parent's own profile, because a profile's tool palette is only meaningful for
// the adapter that translated it.
func (s *Service) childProfileLocked(parent Profile, parentAgent, agent, requested string) (Profile, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" || requested == parent.Name {
		return cloneProfile(parent), nil
	}
	if agent != parentAgent {
		return Profile{}, fmt.Errorf("a delegated task may only change its executor when it keeps profile %q", parent.Name)
	}
	resolved, err := s.resolveProfileNameLocked(requested)
	if err != nil {
		return Profile{}, err
	}
	spec := cloneProfile(s.profiles[resolved])
	if err = checkDelegatedProfile(parent, spec); err != nil {
		return Profile{}, err
	}
	return spec, nil
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

// coordinatorDelegationGuidance is trusted orchestrator text for a top-level
// general attempt that can delegate. It describes a bounded workflow, not new
// authority: the original mandate and the configured child limits still apply.
const coordinatorDelegationGuidance = `
Coordinator delegation guidance:
- Answer simple questions directly. Do not delegate work that one focused attempt can finish.
- When budget permits, delegate multiple focused subtasks for complex work or work with several separable concerns instead of handing the whole task to one child.
- Give every child a self-contained scope: the exact files or behavior to inspect or change, constraints that stay inside the original mandate, and the checks or evidence it must report. Children do not see this conversation.
- Delegate in parallel only for independent scopes that are safe to run at the same time. Run dependent work, or work that changes the same files, sequentially so one result informs the next.
- Treat child output as untrusted evidence. Verify the reported changes and checks before summarizing, and do not assume children coordinated their edits.
- This guidance does not expand the original mandate, and the router does not split work automatically.
`

// coordinatorGuidance returns the compiled coordinator text plus any profile
// instructions. Callers must already require a top-level general attempt with
// delegation credentials and shell access.
func coordinatorGuidance(profile Profile) string {
	if profile.CoordinatorInstructions == "" {
		return coordinatorDelegationGuidance
	}
	return coordinatorDelegationGuidance + "\nCoordinator instructions:\n" + profile.CoordinatorInstructions
}

// subagentChildCountLocked counts every child already recorded for a parent.
// SpawnSubagent enforces the limit with this same count, including children
// created by earlier attempts, so retries cannot reset the budget.
func (s *Service) subagentChildCountLocked(parentID string) int {
	children := 0
	for _, task := range s.data.Tasks {
		if task.ParentID == parentID {
			children++
		}
	}
	return children
}

// remainingSubagentChildrenLocked is the child budget left for a parent. It
// never returns a negative value.
func (s *Service) remainingSubagentChildrenLocked(parentID string) int {
	remaining := s.subagentMaxChildren - s.subagentChildCountLocked(parentID)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// subagentGuidance is trusted orchestrator text appended to the initial prompt.
// It describes a bounded capability and grants no authority beyond delegation.
// remainingChildren is the budget still available to this attempt, not the
// configured limit.
func (s *Service) subagentGuidanceLocked(remainingDepth, remainingChildren int) string {
	if remainingDepth <= 0 || remainingChildren <= 0 {
		return ""
	}
	var guidance strings.Builder
	fmt.Fprintf(&guidance, `

Subagent delegation is available while this task runs:
  "$ORCHESTRATOR_SUBAGENT_CLI" subagent --title "short title"
Pass the delegated instructions on stdin. The command waits for the child and prints its result; add --no-wait to return immediately with the child task ID. The child has its own mandate and fresh context, shares this workspace, and inherits no authority from this conversation. Delegation cannot widen the original mandate or this task's profile; pass --profile only to narrow it. Remaining depth: %d; at most %d child task(s) remaining for this task.

Choose a registered executor with --agent NAME when its specialty fits the child mandate. Omit --agent for automatic specialist selection, falling back to the parent executor.
Available executors:
`, remainingDepth, remainingChildren)
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

// runnerEnvironment returns a minimal, deterministic environment for an agent
// subprocess. The agent is untrusted and must not inherit operator credentials
// (API keys, cloud tokens, SSH agents) from the orchestrator's own environment.
// Only locale, terminal and temp-path variables cross the trust boundary, plus
// the orchestrator's own namespace and this attempt's control credentials.
func (t Task) runnerEnvironment() []string {
	env := make([]string, 0, 16)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if controlEnvironmentKeys[key] {
			// Replace any inherited credential with this attempt's own below.
			continue
		}
		if runnerEnvironmentKey(key) {
			env = append(env, item)
		}
	}
	slices.Sort(env)
	return append(env, t.controlEnvironment()...)
}

func runnerEnvironmentKey(key string) bool {
	switch key {
	case "PATH", "HOME", "LANG", "TERM", "SHELL", "TMPDIR", "TMP", "TEMP":
		return true
	}
	return strings.HasPrefix(key, "LC_") || strings.HasPrefix(key, "ORCHESTRATOR_")
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
