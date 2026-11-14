package orchestrator

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SetDefaultIsolation controls whether mandates that explicitly request
// changes default to a dedicated worktree when their workspace supports it.
// Existing tasks are not changed.
func (s *Service) SetDefaultIsolation(enabled bool) error {
	return s.whileOpen(func() error { s.defaultIsolation = enabled; return nil })
}

// workspacePlan records the workspace choice made at acceptance. An automatic
// choice stays tied to the mandate classification; an operator choice is not
// automatic.
type workspacePlan struct {
	isolated  bool
	automatic bool
}

// defaultWorkspacePlan isolates only when the default is enabled, the
// workspace can be isolated and the mandate explicitly requests changes.
func (s *Service) defaultWorkspacePlan(workspace, prompt string) workspacePlan {
	s.mu.Lock()
	enabled := s.defaultIsolation
	s.mu.Unlock()
	if !enabled || !workspaceIsolatable(workspace) {
		return workspacePlan{}
	}
	return workspacePlan{isolated: taskMakesChanges(prompt), automatic: true}
}

// CreateTask routes an explicit manual request into a ticket with an immutable mandate.
func (s *Service) CreateTask(title, prompt, workspace string) (Task, error) {
	return s.CreateTaskWithProfile(title, prompt, workspace, "")
}

// CreateTaskWithProfile creates a manual request assigned to a named profile.
// An empty name uses the routing default. The resolved profile is snapshotted.
func (s *Service) CreateTaskWithProfile(title, prompt, workspace, profile string) (Task, error) {
	return s.acceptLocal(PurposeManual, "", title, prompt, workspace, profile)
}

// CreateRelatedTask accepts a separate local mandate in an existing task's
// thread. Existing tasks are not given the new mandate as instructions.
func (s *Service) CreateRelatedTask(parentID, title, prompt, workspace string) (Task, error) {
	return s.acceptLocal(PurposeRelated, parentID, title, prompt, workspace, "")
}

// acceptLocal accepts an explicit local request. A parentID joins that task's
// thread; the router derives the title and records its assignment in the same
// write as the decision.
func (s *Service) acceptLocal(purpose, parentID, title, text, workspace, profile string) (Task, error) {
	agent, request, err := s.routeTopLevelRequest(text)
	if err != nil {
		return Task{}, err
	}
	if purpose == PurposeRouter {
		title = requestTitle(request)
	}
	title = strings.TrimSpace(title)
	if title == "" || request == "" {
		return Task{}, errors.New("title and prompt are required")
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return Task{}, err
	}
	if err = requireDir(abs, "workspace"); err != nil {
		return Task{}, err
	}
	plan := s.defaultWorkspacePlan(abs, request)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return Task{}, errShuttingDown
	}
	in := acceptedTaskInput{
		auth:     AuthorizationRequest{Source: LocalPolicySource, Actor: LocalActor, Purpose: purpose, ParentID: parentID, Text: request, Workspace: abs},
		title:    title,
		agent:    agent,
		profile:  profile,
		plan:     plan,
		accepted: "Manual request accepted",
	}
	if parentID != "" {
		parent := s.taskLocked(parentID)
		if parent == nil {
			return Task{}, errors.New("parent task not found")
		}
		if s.threadLocked(parent.ThreadID) == nil {
			return Task{}, errors.New("task thread not found")
		}
		in.auth.ThreadID = parent.ThreadID
		in.accepted += " in existing thread"
	}
	if purpose == PurposeRouter {
		if in.profile, err = s.resolveProfileNameLocked(profile); err != nil {
			return Task{}, err
		}
		workspaceChoice := "shared workspace"
		if plan.isolated {
			workspaceChoice = "isolated committed-HEAD worktree"
		}
		in.notes = []Event{{Kind: "routed", Message: fmt.Sprintf("Router assignment: agent %s, profile %s, %s, execution requested (%s)", agent, in.profile, workspaceChoice, LocalPolicyVersion)}}
	}
	return s.acceptLocked(in)
}

func requireDir(path, label string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	if !info.IsDir() {
		return errors.New(label + " must be a directory")
	}
	return nil
}

// acceptedTaskInput is everything needed to authorize and record a task.
// auth.Text is the mandate and auth.Workspace its validated workspace. An
// empty auth.ThreadID starts a new thread. A positive auth.Depth marks
// delegated work, whose task records auth.ParentID as its parent.
type acceptedTaskInput struct {
	auth      AuthorizationRequest
	title     string
	agent     string
	profile   string
	spec      *Profile // a pre-resolved snapshot; otherwise profile is resolved
	inherited []string
	plan      workspacePlan
	accepted  string
	notes     []Event // recorded after acceptance, stamped with its time
}

// acceptLocked is the single acceptance path: it authorizes the request,
// snapshots the profile and durably records the task with its trigger and
// decision before returning. A rejected request or a failed save leaves no
// trace. Caller holds mu.
func (s *Service) acceptLocked(in acceptedTaskInput) (Task, error) {
	in.auth.TriggerID = newID()
	if in.auth.ThreadID == "" {
		in.auth.ThreadID = newID()
	}
	decision, err := s.authorize(in.auth)
	if err != nil {
		return Task{}, err
	}
	now := time.Now().UTC()
	decision.CreatedAt = now
	profileName, spec := "", in.spec
	if spec != nil {
		profileName = spec.Name
	} else if profileName, spec, err = s.resolveProfileLocked(in.profile); err != nil {
		return Task{}, err
	}
	events := []Event{{now, "accepted", in.accepted}}
	for _, note := range in.notes {
		note.Time = now
		events = append(events, note)
	}
	if in.plan.isolated {
		events = append(events, Event{now, "workspace", "Isolated committed-HEAD worktree selected by default; uncommitted files are excluded"})
	} else if in.plan.automatic {
		events = append(events, Event{now, "workspace", "Automatic workspace choice: shared source workspace; no requested change detected"})
	}
	t := Task{
		ID:              newID(),
		ThreadID:        in.auth.ThreadID,
		Title:           in.title,
		Prompt:          in.auth.Text,
		Workspace:       in.auth.Workspace,
		Isolated:        in.plan.isolated,
		IsolationAuto:   in.plan.automatic,
		Status:          Queued,
		Agent:           in.agent,
		ProfileName:     profileName,
		ProfileSpec:     spec,
		InheritedTools:  in.inherited,
		SpecialistTools: s.specialistTools[in.agent],
		Trigger:         Trigger{ID: in.auth.TriggerID, Source: in.auth.Source, Text: in.auth.Text, CreatedAt: now},
		Decision:        decision,
		Events:          events,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if in.auth.Depth > 0 {
		t.ParentID, t.Depth = in.auth.ParentID, in.auth.Depth
	}
	snapshot := s.snapshotLocked()
	if t.ParentID != "" {
		parent := s.taskLocked(t.ParentID)
		parent.Events = append(parent.Events, Event{now, "subagent", "Subagent spawned: " + t.ID + " · " + t.Title})
	}
	s.data.Tasks = append(s.data.Tasks, t)
	if thread := s.threadLocked(t.ThreadID); thread != nil {
		thread.Triggers = append(thread.Triggers, t.Trigger)
		thread.Decisions = append(thread.Decisions, t.Decision)
	} else {
		s.data.Threads = append(s.data.Threads, Thread{ID: t.ThreadID, Triggers: []Trigger{t.Trigger}, Decisions: []Decision{t.Decision}})
	}
	if err = s.commitLocked(snapshot); err != nil {
		return Task{}, err
	}
	return clone(t), nil
}
