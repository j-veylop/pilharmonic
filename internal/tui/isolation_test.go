package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
)

func TestIsolationControlTogglesAndExplainsWorkspace(t *testing.T) {
	dir := reviewRepo(t)
	s := newTestService(t)
	task, err := s.CreateTask("work", "mandate", dir)
	if err != nil {
		t.Fatal(err)
	}
	m := model{service: s, workspace: dir, tasks: s.ListTasks(), width: 120, height: 35, tab: infoTab, detailFocus: true}
	if !strings.Contains(m.View(), "shared workspace") {
		t.Fatal("initial workspace state not shown")
	}
	m = key(key(key(m, runeKey("o")), runeKey("j")), runeKey("j"))
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
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
	if m.setup.target != task.ID || !strings.Contains(ansi.Strip(m.View()), "Isolation") {
		t.Fatal("the setup popup closed after toggling isolation")
	}
	if m.setup = (setupState{}); !strings.Contains(m.notice.text, "Isolation enabled") || !strings.Contains(m.View(), "isolated worktree requested") {
		t.Fatalf("isolation state not surfaced: notice=%q view=%s", m.notice.text, m.View())
	}

	m = key(key(key(key(m, runeKey("o")), runeKey("j")), runeKey("j")), runeKey("j"))
	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
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
	if m.setup = (setupState{}); !strings.Contains(m.notice.text, "Isolation off") || !strings.Contains(m.View(), "shared workspace") {
		t.Fatalf("disabled state not surfaced: notice=%q view=%s", m.notice.text, m.View())
	}
}

func TestDetailShowsAutomaticMergeResult(t *testing.T) {
	m := model{width: 120, height: 35, tasks: []orchestrator.Task{{
		ID: "task", Title: "merged", Prompt: "work", Status: orchestrator.Completed,
		Isolated: true, ExecutionWorkspace: "/state/workspaces/task/tree", IsolationCommit: "abc123",
		MergeStatus: orchestrator.MergeMerged, MergeCommit: "def456",
	}}, tab: infoTab, detailFocus: true}
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
	m.tasks[0].MergeStatus = orchestrator.MergeMerged
	m.tasks[0].MergeError = ""
	m.tasks[0].MergeWarning = "uncommitted source changes are kept in the autostash entry stash@{0}"
	view = m.View()
	if !strings.Contains(view, "Merge warning") || !strings.Contains(view, "stash@{0}") {
		t.Fatalf("merge warning not surfaced: %s", view)
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
	}}, tab: infoTab, detailFocus: true}
	view := m.View()
	for _, text := range []string{"Execution: /state/workspaces/task/tree", "Isolated worktree", "abc123"} {
		if !strings.Contains(view, text) {
			t.Fatalf("missing %q in view", text)
		}
	}
}
