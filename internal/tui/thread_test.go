package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"orchestrator/internal/orchestrator"
)

func key(m model, msg tea.KeyMsg) model { next, _ := m.Update(msg); return next.(model) }
func TestRelatedTaskAndThreadContextControls(t *testing.T) {
	dir := t.TempDir()
	s, err := orchestrator.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.CreateTask("first", "original mandate", dir)
	if err != nil {
		t.Fatal(err)
	}
	m := model{service: s, workspace: dir, tasks: s.ListTasks(), width: 120, height: 35}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if !strings.Contains(m.View(), "RELATED TASK") {
		t.Fatal("related-task prompt not shown")
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("second mandate")})
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	tasks := s.ListTasks()
	if len(tasks) != 2 || tasks[0].ThreadID != first.ThreadID {
		t.Fatalf("related task not created: %+v", tasks)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("shared context")})
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	tasks = s.ListTasks()
	for _, task := range tasks {
		if len(task.Context) != 1 || task.Context[0].Text != "shared context" {
			t.Fatalf("task missing context: %+v", task)
		}
	}
}
