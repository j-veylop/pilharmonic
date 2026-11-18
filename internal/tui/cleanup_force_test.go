package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/j-veylop/pilharmonic/internal/orchestrator"
)

type forceCleanupService struct {
	Service
	called string
}

func (s *forceCleanupService) ForceDiscardWorktree(id string) error {
	s.called = id
	return nil
}

func TestForceRemoveWorktreeConfirmation(t *testing.T) {
	task := orchestrator.Task{ID: "task", Title: "dirty changes", Isolated: true, ExecutionWorkspace: "/tmp/worktree", Status: orchestrator.Completed}
	m := model{tasks: []orchestrator.Task{task}, width: 100, height: 24}
	selectTask(t, &m, task.ID)
	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlX})
	view := m.View()
	for _, want := range []string{"PERMANENTLY REMOVE WORKTREE", "Permanently deletes all uncommitted and unmerged work"} {
		if !strings.Contains(view, want) {
			t.Fatalf("confirmation missing %q: %s", want, view)
		}
	}
	m = key(m, runeKey("n"))
	if m.cleanup.kind != "" {
		t.Fatal("cancellation retained force-removal confirmation")
	}

	service := &forceCleanupService{}
	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlX})
	m.service = service
	m, cmd := m.cleanupKey("y")
	if service.called != "" || cmd == nil {
		t.Fatal("confirmation did not defer the removal")
	}
	msg := cmd()
	if service.called != task.ID || !strings.Contains(string(msg.(cleanupResultMsg)), "history kept") {
		t.Fatalf("confirmed removal: called=%q result=%v", service.called, msg)
	}
}
