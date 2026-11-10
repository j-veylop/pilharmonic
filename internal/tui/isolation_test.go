package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"pilharmonic/internal/orchestrator"
)

func TestIsolationControlTogglesAndExplainsWorkspace(t *testing.T) {
	dir := t.TempDir()
	s, err := orchestrator.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	task, err := s.CreateTask("work", "mandate", dir)
	if err != nil {
		t.Fatal(err)
	}
	m := model{service: s, workspace: dir, tasks: s.ListTasks(), width: 120, height: 35}
	if !strings.Contains(m.View(), "shared workspace") {
		t.Fatal("initial workspace state not shown")
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("w")})
	m = next.(model)
	if cmd == nil {
		t.Fatal("isolation toggle produced no command")
	}
	next, _ = m.Update(cmd())
	m = next.(model)
	got, _ := s.GetTask(task.ID)
	if !got.Isolated {
		t.Fatal("isolation was not enabled")
	}
	if !strings.Contains(m.notice, "Isolation enabled") || !strings.Contains(m.View(), "isolated worktree requested") {
		t.Fatalf("isolation state not surfaced: notice=%q view=%s", m.notice, m.View())
	}

	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("w")})
	m = next.(model)
	if cmd == nil {
		t.Fatal("isolation toggle off produced no command")
	}
	next, _ = m.Update(cmd())
	m = next.(model)
	got, _ = s.GetTask(task.ID)
	if got.Isolated {
		t.Fatal("isolation was not disabled")
	}
	if !strings.Contains(m.notice, "Isolation off") || !strings.Contains(m.View(), "shared workspace") {
		t.Fatalf("disabled state not surfaced: notice=%q view=%s", m.notice, m.View())
	}
}

func TestDetailShowsAutomaticMergeResult(t *testing.T) {
	m := model{width: 120, height: 35, tasks: []orchestrator.Task{{
		ID: "task", Title: "merged", Prompt: "work", Status: orchestrator.Completed,
		Isolated: true, ExecutionWorkspace: "/state/workspaces/task/tree", IsolationCommit: "abc123",
		MergeStatus: orchestrator.MergeMerged, MergeCommit: "def456",
	}}}
	view := m.View()
	for _, text := range []string{"Merge: merged", "def456"} {
		if !strings.Contains(view, text) {
			t.Fatalf("missing %q in view", text)
		}
	}
	m.tasks[0].MergeStatus = orchestrator.MergeFailed
	m.tasks[0].MergeError = "conflict in tracked.txt"
	view = m.View()
	if !strings.Contains(view, "MERGE FAILED") || !strings.Contains(view, "conflict in tracked.txt") {
		t.Fatalf("merge failure not surfaced: %s", view)
	}
}

func TestDetailShowsPreparedIsolatedWorkspace(t *testing.T) {
	m := model{width: 120, height: 35, tasks: []orchestrator.Task{{
		ID:                 "task",
		Title:              "isolated",
		Prompt:             "work",
		Status:             orchestrator.Running,
		Isolated:           true,
		ExecutionWorkspace: "/state/workspaces/task/tree",
		IsolationCommit:    "abc123",
	}}}
	view := m.View()
	for _, text := range []string{"Execution: /state/workspaces/task/tree", "Isolated worktree", "abc123"} {
		if !strings.Contains(view, text) {
			t.Fatalf("missing %q in view", text)
		}
	}
}
