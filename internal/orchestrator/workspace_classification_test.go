package orchestrator

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskMakesChangesClassification(t *testing.T) {
	cases := []struct {
		name   string
		prompt string
		want   bool
	}{
		{"inspection role", "review the parser", false},
		{"read request", "read the configuration", false},
		{"inspect command", "inspect the logs", false},
		{"investigate command", "investigate the failure", false},
		{"explain fix", "show me how to fix the parser", false},
		{"inspect existing fix", "review the recent fix", false},
		{"inspect then edit", "inspect the parser and remove dead code", true},
		{"review then make faster", "review the parser and make it faster", true},
		{"explain command", "explain how to run gofmt", false},
		{"question command", "what does the parser do", false},
		{"question mutation", "what is wrong? fix the parser", true},
		{"read-only git", "git status", false},
		{"run tests", "run the tests", false},
		{"run test suite", "run the test suite", false},
		{"run a test", "run a test for the parser", false},
		{"cleanup request", "cleanup the temporary files", true},
		{"explicit change", "fix the parser", true},
		{"ambiguous request", "do work", false},
		{"ambiguous custom executor", "do the thing", false},
		{"review then edit", "review the parser and fix the bug", true},
		{"review then improve", "review the parser and improve tests", true},
		{"verify request", "verify the parser", false},
		{"polite edit", "Can you add regression tests", true},
		{"punctuated edit", "review the code; update the docs", true},
		{"newline edit", "review the code\nupdate the docs", true},
		{"gofmt command", "run gofmt", true},
		{"git mutation", "git commit the changes", true},
		{"git push", "git push the branch", true},
		{"git worktree list", "git worktree list", false},
		{"git worktree mutation", "git worktree add ../review-tree", true},
		{"check out mutation", "check out a new branch", true},
		{"polite let's", "let's add tests", true},
		{"polite would like", "I'd like to update the docs", true},
		{"polite let me", "let me update the changelog", true},
		{"polite we should", "we should refactor this", true},
		{"polite help me", "help me fix the parser", true},
		{"subject auxiliary", "we could add regression tests", true},
		{"polite inspection stays shared", "I'll review the parser", false},
		{"subject inspection stays shared", "we should review the parser", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := taskMakesChanges(tc.prompt); got != tc.want {
				t.Fatalf("taskMakesChanges(%q) = %v, want %v", tc.prompt, got, tc.want)
			}
		})
	}
}

func TestInspectionRequestsUseSharedWorkspace(t *testing.T) {
	repo := committedRepo(t)
	statePath := filepath.Join(t.TempDir(), "state.json")
	s := openTestService(t, statePath)
	runs := make(chan Task, 8)
	for _, agent := range []string{"reviewer", "explorer", "tester", "debugger", "gitter"} {
		s.RegisterRunner(agent, runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
			runs <- task
			return nil
		}))
	}
	cases := []struct {
		prompt string
		agent  string
	}{
		{"@reviewer review the parser", "reviewer"},
		{"@explorer explain the parser", "explorer"},
		{"@tester run the tests", "tester"},
		{"@debugger debug the failure", "debugger"},
		{"@gitter git status", "gitter"},
	}
	for _, tc := range cases {
		t.Run(tc.agent, func(t *testing.T) {
			task, err := s.SubmitRequest(tc.prompt, repo)
			if err != nil {
				t.Fatal(err)
			}
			if task.Agent != tc.agent {
				t.Fatalf("agent %q, want %q", task.Agent, tc.agent)
			}
			if task.Isolated || !task.IsolationAuto {
				t.Fatalf("inspection workspace choice: isolated=%v automatic=%v", task.Isolated, task.IsolationAuto)
			}
			done := await(t, s, task.ID, Completed)
			if done.Isolated || done.ExecutionWorkspace != "" {
				t.Fatalf("inspection task prepared a worktree: %+v", done)
			}
			run := <-runs
			if run.Workspace != repo {
				t.Fatalf("runner cwd %q, want source workspace %q", run.Workspace, repo)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(statePath), "workspaces", task.ID)); !os.IsNotExist(err) {
				t.Fatalf("inspection task created a workspace reservation: %v", err)
			}
			found := false
			for _, event := range done.Events {
				if event.Kind == "workspace" && strings.Contains(event.Message, "shared source workspace") {
					found = true
				}
			}
			if !found {
				t.Fatalf("inspection workspace decision was not recorded: %+v", done.Events)
			}
		})
	}
}

func TestInspectionRequestWithFallbackExecutorUsesSharedWorkspace(t *testing.T) {
	repo := committedRepo(t)
	statePath := filepath.Join(t.TempDir(), "state.json")
	s := openTestService(t, statePath)
	seen := make(chan Task, 1)
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		seen <- task
		return nil
	}))
	task, err := s.SubmitRequest("review the parser", repo)
	if err != nil {
		t.Fatal(err)
	}
	if task.Agent != DefaultRouteAgent || task.Isolated || !task.IsolationAuto {
		t.Fatalf("fallback inspection classification: %+v", task)
	}
	await(t, s, task.ID, Completed)
	if run := <-seen; run.Workspace != repo {
		t.Fatalf("fallback runner cwd %q, want %q", run.Workspace, repo)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(statePath), "workspaces", task.ID)); !os.IsNotExist(err) {
		t.Fatalf("fallback inspection task created a workspace reservation: %v", err)
	}
}

func TestAmbiguousRequestsUseSharedWorkspace(t *testing.T) {
	repo := committedRepo(t)
	statePath := filepath.Join(t.TempDir(), "state.json")
	s := openTestService(t, statePath)
	runs := make(chan Task, 8)
	for _, agent := range []string{"general", "implementer", "reviewer"} {
		s.RegisterRunner(agent, runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
			runs <- task
			return nil
		}))
	}
	cases := []struct {
		prompt string
		agent  string
	}{
		{"do work", "general"},
		{"@implementer do work", "implementer"},
		{"@reviewer do work", "reviewer"},
	}
	for _, tc := range cases {
		t.Run(tc.agent, func(t *testing.T) {
			task, err := s.SubmitRequest(tc.prompt, repo)
			if err != nil {
				t.Fatal(err)
			}
			if task.Agent != tc.agent {
				t.Fatalf("agent %q, want %q", task.Agent, tc.agent)
			}
			if task.Isolated || !task.IsolationAuto {
				t.Fatalf("ambiguous workspace choice: isolated=%v automatic=%v", task.Isolated, task.IsolationAuto)
			}
			done := await(t, s, task.ID, Completed)
			if done.Isolated || done.ExecutionWorkspace != "" {
				t.Fatalf("ambiguous task prepared a worktree: %+v", done)
			}
			if run := <-runs; run.Workspace != repo {
				t.Fatalf("runner cwd %q, want source workspace %q", run.Workspace, repo)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(statePath), "workspaces", task.ID)); !os.IsNotExist(err) {
				t.Fatalf("ambiguous task created a workspace reservation: %v", err)
			}
		})
	}
}

func TestChangeRequestsRetainIsolation(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	runs := make(chan Task, 8)
	for _, agent := range []string{"general", "implementer", "reviewer", "tester", "gitter"} {
		s.RegisterRunner(agent, runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
			runs <- task
			return nil
		}))
	}
	cases := []struct {
		prompt string
		agent  string
	}{
		{"fix the parser", "general"},
		{"@implementer fix the parser", "implementer"},
		{"@reviewer review the parser and fix the bug", "reviewer"},
		{"@tester run tests then update the docs", "tester"},
		{"@gitter git commit the changes", "gitter"},
	}
	for _, tc := range cases {
		t.Run(tc.agent, func(t *testing.T) {
			task, err := s.SubmitRequest(tc.prompt, repo)
			if err != nil {
				t.Fatal(err)
			}
			if task.Agent != tc.agent {
				t.Fatalf("agent %q, want %q", task.Agent, tc.agent)
			}
			if !task.Isolated || !task.IsolationAuto {
				t.Fatalf("change workspace choice: isolated=%v automatic=%v", task.Isolated, task.IsolationAuto)
			}
			done := await(t, s, task.ID, Completed)
			run := <-runs
			if !done.Isolated || done.ExecutionWorkspace == "" {
				t.Fatalf("change task did not prepare a worktree: %+v", done)
			}
			if run.Workspace == repo || !strings.Contains(run.Workspace, filepath.Join("workspaces", task.ID, "tree")) {
				t.Fatalf("runner cwd %q is not the task worktree", run.Workspace)
			}
		})
	}
}

func TestExecutorChangePreservesWorkspaceChoice(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	s.LoadBuiltinSpecialists()
	task, err := s.CreateTask("draft", "do work", repo)
	if err != nil {
		t.Fatal(err)
	}
	if task.Isolated || !task.IsolationAuto {
		t.Fatalf("ambiguous draft did not use the shared workspace: %+v", task)
	}

	if err = s.SetTaskAgent(task.ID, "explorer"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetTask(task.ID)
	if got.Isolated || !got.IsolationAuto {
		t.Fatalf("inspection executor changed the automatic workspace choice: %+v", got)
	}

	if err = s.SetTaskAgent(task.ID, "implementer"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetTask(task.ID)
	if got.Isolated || !got.IsolationAuto {
		t.Fatalf("executor role alone enabled isolation: %+v", got)
	}

	if err = s.SetTaskIsolation(task.ID, true); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetTask(task.ID)
	if !got.Isolated || got.IsolationAuto {
		t.Fatalf("explicit isolation was not recorded: %+v", got)
	}
	if err = s.SetTaskAgent(task.ID, "reviewer"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetTask(task.ID)
	if !got.Isolated || got.IsolationAuto {
		t.Fatalf("executor change overrode explicit isolation: %+v", got)
	}

	if err = s.SetTaskIsolation(task.ID, false); err != nil {
		t.Fatal(err)
	}
	if err = s.SetTaskAgent(task.ID, "implementer"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetTask(task.ID)
	if got.Isolated || got.IsolationAuto {
		t.Fatalf("executor change overrode explicit shared choice: %+v", got)
	}

	change, err := s.CreateTask("change", "fix the parser", repo)
	if err != nil {
		t.Fatal(err)
	}
	if !change.Isolated || !change.IsolationAuto {
		t.Fatalf("change request did not select isolation: %+v", change)
	}
	if err = s.SetTaskAgent(change.ID, "explorer"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetTask(change.ID)
	if !got.Isolated || !got.IsolationAuto {
		t.Fatalf("executor change overrode the mandate's change signal: %+v", got)
	}
}

func TestAutomaticWorkspaceChoiceReconciledAtStart(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	seen := make(chan Task, 1)
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		seen <- task
		return nil
	}))
	task, err := s.CreateTask("legacy", "do work", repo)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an automatic isolated choice persisted by the previous
	// role-based heuristic for a mandate with no change action.
	s.mu.Lock()
	for i := range s.data.Tasks {
		if s.data.Tasks[i].ID == task.ID {
			s.data.Tasks[i].Isolated = true
			s.data.Tasks[i].IsolationAuto = true
		}
	}
	err = s.save()
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	done := await(t, s, task.ID, Completed)
	if done.Isolated || done.ExecutionWorkspace != "" {
		t.Fatalf("legacy automatic choice prepared a worktree: %+v", done)
	}
	if run := <-seen; run.Workspace != repo {
		t.Fatalf("runner cwd %q, want source workspace %q", run.Workspace, repo)
	}
	found := false
	for _, event := range done.Events {
		if event.Kind == "workspace" && strings.Contains(event.Message, "Shared source workspace selected") {
			found = true
		}
	}
	if !found {
		t.Fatalf("workspace reconciliation was not recorded: %+v", done.Events)
	}
}

func TestAutomaticWorkspaceChoiceSurvivesRestart(t *testing.T) {
	repo := committedRepo(t)
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Shutdown() })
	s.LoadBuiltinSpecialists()

	inspection, err := s.CreateTask("inspect", "review the parser", repo)
	if err != nil {
		t.Fatal(err)
	}
	change, err := s.CreateTask("change", "fix the parser", repo)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Isolated || !inspection.IsolationAuto || !change.Isolated || !change.IsolationAuto {
		t.Fatalf("initial workspace choices: inspection=%+v change=%+v", inspection, change)
	}
	// Confirming the automatic shared choice makes it an explicit operator
	// choice, which a later executor change must not override.
	if err = s.SetTaskIsolation(inspection.ID, false); err != nil {
		t.Fatal(err)
	}
	if err = s.Shutdown(); err != nil {
		t.Fatal(err)
	}

	restored, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Shutdown() })
	restored.LoadBuiltinSpecialists()

	gotInspection, _ := restored.GetTask(inspection.ID)
	if gotInspection.Isolated || gotInspection.IsolationAuto {
		t.Fatalf("explicit shared choice did not survive restart: %+v", gotInspection)
	}
	if err = restored.SetTaskAgent(inspection.ID, "implementer"); err != nil {
		t.Fatal(err)
	}
	gotInspection, _ = restored.GetTask(inspection.ID)
	if gotInspection.Isolated || gotInspection.IsolationAuto {
		t.Fatalf("executor change overrode persisted shared choice: %+v", gotInspection)
	}

	gotChange, _ := restored.GetTask(change.ID)
	if !gotChange.Isolated || !gotChange.IsolationAuto {
		t.Fatalf("automatic isolation did not survive restart: %+v", gotChange)
	}
	if err = restored.SetTaskAgent(change.ID, "reviewer"); err != nil {
		t.Fatal(err)
	}
	gotChange, _ = restored.GetTask(change.ID)
	if !gotChange.Isolated || !gotChange.IsolationAuto {
		t.Fatalf("executor change overrode the mandate's change signal: %+v", gotChange)
	}
}

func TestRelatedInspectionTaskUsesSharedWorkspace(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	seen := make(chan Task, 1)
	s.RegisterRunner("reviewer", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		seen <- task
		return nil
	}))
	parent, err := s.CreateTask("parent", "implement the parser", repo)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateRelatedTask(parent.ID, "review", "@reviewer review the parser", repo)
	if err != nil {
		t.Fatal(err)
	}
	if task.Agent != "reviewer" || task.Isolated || !task.IsolationAuto {
		t.Fatalf("related inspection classification: %+v", task)
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	await(t, s, task.ID, Completed)
	if run := <-seen; run.Workspace != repo {
		t.Fatalf("related inspection runner cwd %q, want %q", run.Workspace, repo)
	}
}

func TestCustomReviewerWithoutChangeRequestUsesSharedWorkspace(t *testing.T) {
	repo := committedRepo(t)
	path := filepath.Join(t.TempDir(), "state.json")
	s := openTestService(t, path)
	s.LoadBuiltinSpecialists()
	task, err := s.CreateTask("draft", "do work", repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetTaskAgent(task.ID, "reviewer"); err != nil {
		t.Fatal(err)
	}
	if selected, _ := s.GetTask(task.ID); selected.Isolated {
		t.Fatal("bundled reviewer selected a worktree without a change request")
	}
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}

	restored := openTestService(t, path)
	if err := restored.LoadAgents(writeAgentConfig(t, `{"agents":[{"name":"reviewer","command":"cat"}]}`)); err != nil {
		t.Fatal(err)
	}
	restored.LoadBuiltinSpecialists()
	draft, err := restored.CreateTask("another draft", "do work", repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.SetTaskAgent(draft.ID, "reviewer"); err != nil {
		t.Fatal(err)
	}
	if selected, _ := restored.GetTask(draft.ID); selected.Isolated {
		t.Fatal("custom reviewer enabled isolation from its role alone")
	}

	if err := restored.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	done := await(t, restored, task.ID, Completed)
	if done.Isolated || done.ExecutionWorkspace != "" {
		t.Fatalf("restored ambiguous task prepared a worktree: %+v", done)
	}
}
