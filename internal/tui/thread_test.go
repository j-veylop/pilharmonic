package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"pilharmonic/internal/orchestrator"
)

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
	m = key(m, runeKey("a"))
	if !strings.Contains(m.View(), "RELATED TASK") {
		t.Fatal("related-task prompt not shown")
	}
	m = key(m, runeKey("second mandate"))
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if strings.Contains(m.View(), "RELATED TASK") {
		t.Fatal("related-task composer still open after submit")
	}
	tasks := s.ListTasks()
	if len(tasks) != 2 || tasks[0].ThreadID != first.ThreadID {
		t.Fatalf("related task not created: %+v", tasks)
	}
	m = key(m, runeKey("f"))
	m = key(m, runeKey("shared context"))
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
