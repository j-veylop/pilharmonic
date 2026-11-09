package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"orchestrator/internal/orchestrator"
)

func TestDeleteConfirmation(t *testing.T) {
	s, err := orchestrator.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTask("test task", "prompt", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := model{service: s, tasks: s.ListTasks(), width: 80, height: 24}
	update := func(key string) {
		result, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		m = result.(model)
	}
	update("d")
	if !strings.Contains(m.View(), "DELETE TASK + HISTORY") {
		t.Fatal("missing confirmation")
	}
	update("n")
	if _, ok := s.GetTask(task.ID); !ok {
		t.Fatal("cancel deleted task")
	}
	update("d")
	update("y")
	if _, ok := s.GetTask(task.ID); ok {
		t.Fatal("confirmed task still exists")
	}
}
