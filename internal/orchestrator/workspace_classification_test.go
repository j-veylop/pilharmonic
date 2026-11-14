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

// A submitted request selects a worktree only when its mandate asks for a
// change; the chosen executor is not a signal.
func TestSubmittedRequestWorkspaceChoice(t *testing.T) {
	repo := committedRepo(t)
	statePath := filepath.Join(t.TempDir(), "state.json")
	s := openTestService(t, statePath)
	runs := make(chan Task, 1)
	for _, agent := range []string{"general", "implementer", "reviewer", "explorer", "tester", "debugger", "gitter"} {
		s.RegisterRunner(agent, runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
			runs <- task
			return nil
		}))
	}
	for _, tc := range []struct {
		prompt, agent string
		isolated      bool
	}{
		{"@reviewer review the parser", "reviewer", false},
		{"@explorer explain the parser", "explorer", false},
		{"@tester run the tests", "tester", false},
		{"@debugger debug the failure", "debugger", false},
		{"@gitter git status", "gitter", false},
		{"review the parser", "general", false},
		{"do work", "general", false},
		{"@implementer do work", "implementer", false},
		{"@reviewer do work", "reviewer", false},
		{"fix the parser", "general", true},
		{"@implementer fix the parser", "implementer", true},
		{"@reviewer review the parser and fix the bug", "reviewer", true},
		{"@tester run tests then update the docs", "tester", true},
		{"@gitter git commit the changes", "gitter", true},
	} {
		t.Run(tc.prompt, func(t *testing.T) {
			task, err := s.SubmitRequest(tc.prompt, repo)
			must(t, err)
			if task.Agent != tc.agent || task.Isolated != tc.isolated || !task.IsolationAuto {
				t.Fatalf("agent %q isolated=%v automatic=%v, want %q isolated=%v", task.Agent, task.Isolated, task.IsolationAuto, tc.agent, tc.isolated)
			}
			done := await(t, s, task.ID, Completed)
			run := <-runs
			routed := "shared workspace"
			if tc.isolated {
				routed = "isolated committed-HEAD worktree"
				if done.ExecutionWorkspace == "" || !strings.Contains(run.Workspace, filepath.Join("workspaces", task.ID, "tree")) {
					t.Fatalf("change ran in %q instead of the task worktree", run.Workspace)
				}
			} else {
				if run.Workspace != repo || done.ExecutionWorkspace != "" {
					t.Fatalf("runner cwd %q, want source workspace %q", run.Workspace, repo)
				}
				if _, err := os.Stat(filepath.Join(filepath.Dir(statePath), "workspaces", task.ID)); !os.IsNotExist(err) {
					t.Fatalf("shared task created a workspace reservation: %v", err)
				}
			}
			routedFound, sharedFound := false, false
			for _, event := range done.Events {
				routedFound = routedFound || event.Kind == "routed" && strings.Contains(event.Message, routed)
				sharedFound = sharedFound || event.Kind == "workspace" && strings.Contains(event.Message, "shared source workspace")
			}
			if !routedFound || sharedFound == tc.isolated {
				t.Fatalf("workspace decision was not recorded: %+v", done.Events)
			}
		})
	}
}

func TestExecutorChangePreservesWorkspaceChoice(t *testing.T) {
	repo := committedRepo(t)
	s := newTestService(t)
	s.LoadBuiltinSpecialists()
	task := createTask(t, s, "draft", "do work", repo)
	if task.Isolated || !task.IsolationAuto {
		t.Fatalf("ambiguous draft did not use the shared workspace: %+v", task)
	}

	must(t, s.SetTaskAgent(task.ID, "explorer"))
	got, _ := s.GetTask(task.ID)
	if got.Isolated || !got.IsolationAuto {
		t.Fatalf("inspection executor changed the automatic workspace choice: %+v", got)
	}

	must(t, s.SetTaskAgent(task.ID, "implementer"))
	got, _ = s.GetTask(task.ID)
	if got.Isolated || !got.IsolationAuto {
		t.Fatalf("executor role alone enabled isolation: %+v", got)
	}

	must(t, s.SetTaskIsolation(task.ID, true))
	got, _ = s.GetTask(task.ID)
	if !got.Isolated || got.IsolationAuto {
		t.Fatalf("explicit isolation was not recorded: %+v", got)
	}
	must(t, s.SetTaskAgent(task.ID, "reviewer"))
	got, _ = s.GetTask(task.ID)
	if !got.Isolated || got.IsolationAuto {
		t.Fatalf("executor change overrode explicit isolation: %+v", got)
	}

	must(t, s.SetTaskIsolation(task.ID, false))
	must(t, s.SetTaskAgent(task.ID, "implementer"))
	got, _ = s.GetTask(task.ID)
	if got.Isolated || got.IsolationAuto {
		t.Fatalf("executor change overrode explicit shared choice: %+v", got)
	}

	change := createTask(t, s, "change", "fix the parser", repo)
	if !change.Isolated || !change.IsolationAuto {
		t.Fatalf("change request did not select isolation: %+v", change)
	}
	must(t, s.SetTaskAgent(change.ID, "explorer"))
	got, _ = s.GetTask(change.ID)
	if !got.Isolated || !got.IsolationAuto {
		t.Fatalf("executor change overrode the mandate's change signal: %+v", got)
	}
}

func TestAutomaticWorkspaceChoiceReconciledAtStart(t *testing.T) {
	var err error
	repo := committedRepo(t)
	s := newTestService(t)
	seen := make(chan Task, 1)
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		seen <- task
		return nil
	}))
	task := createTask(t, s, "legacy", "do work", repo)
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

	must(t, s.Start(context.Background(), task.ID))
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
	s := openTestService(t, path)
	s.LoadBuiltinSpecialists()

	inspection := createTask(t, s, "inspect", "review the parser", repo)
	change := createTask(t, s, "change", "fix the parser", repo)
	if inspection.Isolated || !inspection.IsolationAuto || !change.Isolated || !change.IsolationAuto {
		t.Fatalf("initial workspace choices: inspection=%+v change=%+v", inspection, change)
	}
	// Confirming the automatic shared choice makes it an explicit operator
	// choice, which a later executor change must not override.
	must(t, s.SetTaskIsolation(inspection.ID, false))
	must(t, s.Shutdown())

	restored := mustGet(Open(path))(t)
	t.Cleanup(func() { _ = restored.Shutdown() })
	restored.LoadBuiltinSpecialists()

	gotInspection, _ := restored.GetTask(inspection.ID)
	if gotInspection.Isolated || gotInspection.IsolationAuto {
		t.Fatalf("explicit shared choice did not survive restart: %+v", gotInspection)
	}
	must(t, restored.SetTaskAgent(inspection.ID, "implementer"))
	gotInspection, _ = restored.GetTask(inspection.ID)
	if gotInspection.Isolated || gotInspection.IsolationAuto {
		t.Fatalf("executor change overrode persisted shared choice: %+v", gotInspection)
	}

	gotChange, _ := restored.GetTask(change.ID)
	if !gotChange.Isolated || !gotChange.IsolationAuto {
		t.Fatalf("automatic isolation did not survive restart: %+v", gotChange)
	}
	must(t, restored.SetTaskAgent(change.ID, "reviewer"))
	gotChange, _ = restored.GetTask(change.ID)
	if !gotChange.Isolated || !gotChange.IsolationAuto {
		t.Fatalf("executor change overrode the mandate's change signal: %+v", gotChange)
	}
}

func TestRelatedInspectionTaskUsesSharedWorkspace(t *testing.T) {
	repo := committedRepo(t)
	s := newTestService(t)
	seen := make(chan Task, 1)
	s.RegisterRunner("reviewer", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		seen <- task
		return nil
	}))
	parent := createTask(t, s, "parent", "implement the parser", repo)
	task := mustGet(s.CreateRelatedTask(parent.ID, "review", "@reviewer review the parser", repo))(t)
	if task.Agent != "reviewer" || task.Isolated || !task.IsolationAuto {
		t.Fatalf("related inspection classification: %+v", task)
	}
	must(t, s.Start(context.Background(), task.ID))
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
	task := createTask(t, s, "draft", "do work", repo)
	must(t, s.SetTaskAgent(task.ID, "reviewer"))
	if selected, _ := s.GetTask(task.ID); selected.Isolated {
		t.Fatal("bundled reviewer selected a worktree without a change request")
	}
	must(t, s.Shutdown())

	restored := openTestService(t, path)
	must(t, restored.LoadAgents(writeAgentConfig(t, `{"agents":[{"name":"reviewer","command":"cat"}]}`)))
	restored.LoadBuiltinSpecialists()
	draft := createTask(t, restored, "another draft", "do work", repo)
	must(t, restored.SetTaskAgent(draft.ID, "reviewer"))
	if selected, _ := restored.GetTask(draft.ID); selected.Isolated {
		t.Fatal("custom reviewer enabled isolation from its role alone")
	}

	must(t, restored.Start(context.Background(), task.ID))
	done := await(t, restored, task.ID, Completed)
	if done.Isolated || done.ExecutionWorkspace != "" {
		t.Fatalf("restored ambiguous task prepared a worktree: %+v", done)
	}
}
