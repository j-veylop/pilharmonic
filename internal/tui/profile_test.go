package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"orchestrator/internal/orchestrator"
)

func TestProfilePickerSelectAndCancel(t *testing.T) {
	s, err := orchestrator.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	task, err := s.CreateTask("work", "mandate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RegisterProfile(orchestrator.Profile{Name: "reviewer", Description: "Review only", NoIntegrate: true}); err != nil {
		t.Fatal(err)
	}
	m := model{service: s, tasks: s.ListTasks(), width: 80, height: 24}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("P")})
	if m.profileTask != task.ID || !strings.Contains(m.View(), "SELECT PROFILE") {
		t.Fatal("picker not opened")
	}
	for i, profile := range m.profileChoices {
		if profile.Name == "reviewer" {
			m.profileIndex = i
		}
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	got, _ := s.GetTask(task.ID)
	if got.ProfileName != "reviewer" || m.profileTask != "" {
		t.Fatalf("selection failed: %+v", got)
	}
	if view := m.View(); !strings.Contains(view, "reviewer") {
		t.Fatalf("selected profile not displayed: %s", view)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("P")})
	m = key(m, tea.KeyMsg{Type: tea.KeyEsc})
	got, _ = s.GetTask(task.ID)
	if m.profileTask != "" || got.ProfileName != "reviewer" {
		t.Fatal("cancel changed selection")
	}
}
