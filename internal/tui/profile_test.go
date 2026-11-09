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
	if m.profilePicker.target != task.ID || !strings.Contains(m.View(), "SELECT PROFILE") {
		t.Fatal("picker not opened")
	}
	for i, profile := range m.profilePicker.choices {
		if profile.Name == "reviewer" {
			m.profilePicker.index = i
		}
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	got, _ := s.GetTask(task.ID)
	if got.ProfileName != "reviewer" || m.profilePicker.target != "" {
		t.Fatalf("selection failed: %+v", got)
	}
	if view := m.View(); !strings.Contains(view, "reviewer") {
		t.Fatalf("selected profile not displayed: %s", view)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("P")})
	m = key(m, tea.KeyMsg{Type: tea.KeyEsc})
	got, _ = s.GetTask(task.ID)
	if m.profilePicker.target != "" || got.ProfileName != "reviewer" {
		t.Fatal("cancel changed selection")
	}
}

func TestImplementerSummaryDistinguishesExecutorFromProfile(t *testing.T) {
	p := orchestrator.Profile{Name: orchestrator.DefaultProfileName}
	task := orchestrator.Task{Agent: "implementer", ProfileName: p.Name, ProfileSpec: &p}
	if got := executorLabel(task); got != "implementer" || profileLabel(p) != "Executor defaults" {
		t.Fatalf("built-in profile: summary %q, picker %q", got, profileLabel(p))
	}
	p.NoSubagents = true
	if got := executorLabel(task); got != "implementer · profile implementer" || profileLabel(p) != p.Name {
		t.Fatalf("configured profile: summary %q, picker %q", got, profileLabel(p))
	}
}

func TestChildSummaryAndAttemptModel(t *testing.T) {
	task := orchestrator.Task{ParentID: "parent", Agent: "explorer", ProfileName: "coordinator",
		Attempts: []orchestrator.Attempt{{RequestedModel: "deepseek/deepseek-flash"}}}
	if got := executorLabel(task); got != "explorer · subagent" {
		t.Fatalf("child summary: %s", got)
	}
	m := model{tasks: []orchestrator.Task{task}}
	detail := strings.Join(m.detailContent(100), "\n")
	for _, want := range []string{"Profile: coordinator", "Last attempt model: deepseek/deepseek-flash"} {
		if !strings.Contains(detail, want) {
			t.Errorf("missing %q in %s", want, detail)
		}
	}
}
