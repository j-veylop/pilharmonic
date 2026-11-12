package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"pilharmonic/internal/orchestrator"
)

// key applies a key press and drains the commands that carry service results so
// tests observe state after an asynchronous action completes. It deliberately
// ignores ticks and quit commands.
func key(m model, msg tea.KeyMsg) model {
	next, cmd := m.Update(msg)
	m = next.(model)
	for i := 0; cmd != nil && i < 8; i++ {
		switch result := cmd().(type) {
		case actionResultMsg, composerResultMsg, routerResultMsg, cleanupResultMsg, isolationResultMsg, selectionCopiedMsg, fetchModelsMsg:
			next, cmd = m.Update(result)
			m = next.(model)
		default:
			return m
		}
	}
	return m
}
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
	if strings.Contains(m.View(), "RELATED TASK") {
		t.Fatal("related-task composer still open after submit")
	}
	tasks := s.ListTasks()
	if len(tasks) != 2 || tasks[0].ThreadID != first.ThreadID {
		t.Fatalf("related task not created: %+v", tasks)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("shared context")})
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if strings.Contains(m.View(), "FOLLOW UP") {
		t.Fatal("follow-up composer still open after submit")
	}
	tasks = s.ListTasks()
	for _, task := range tasks {
		if len(task.Context) != 1 || task.Context[0].Text != "shared context" {
			t.Fatalf("task missing context: %+v", task)
		}
	}
}
