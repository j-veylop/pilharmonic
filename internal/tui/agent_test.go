package tui

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"orchestrator/internal/orchestrator"
)

type noopAgent struct{}

func (noopAgent) Run(context.Context, orchestrator.Task, io.Writer) error { return nil }
func TestAgentPickerSelectAndCancel(t *testing.T) {
	s, err := orchestrator.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTask("work", "mandate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.RegisterRunner("alternate", noopAgent{})
	m := model{service: s, tasks: s.ListTasks(), width: 80, height: 24}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("A")})
	if m.agentPicker.target != task.ID || !strings.Contains(m.View(), "SELECT AGENT") {
		t.Fatal("picker not opened")
	}
	for i, agent := range m.agentPicker.choices {
		if agent.Name == "alternate" {
			m.agentPicker.index = i
		}
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	got, _ := s.GetTask(task.ID)
	if got.Agent != "alternate" || m.agentPicker.target != "" {
		t.Fatalf("selection failed: %+v", got)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("A")})
	m = key(m, tea.KeyMsg{Type: tea.KeyEsc})
	got, _ = s.GetTask(task.ID)
	if m.agentPicker.target != "" || got.Agent != "alternate" {
		t.Fatal("cancel changed selection")
	}
}

func TestAgentPickerReportsCapturedTargetThatDisappeared(t *testing.T) {
	s, err := orchestrator.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	task, err := s.CreateTask("work", "mandate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := model{service: s, tasks: s.ListTasks(), width: 80, height: 24}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("A")})
	if err := s.DeleteTask(task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTask("other", "another mandate", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	next, _ := m.Update(tickMsg{})
	m = key(next.(model), tea.KeyMsg{Type: tea.KeyEnter})
	if m.notice != "task not found" || m.agentPicker.target != "" {
		t.Fatalf("missing captured task was not reported/closed: notice=%q picker=%+v", m.notice, m.agentPicker)
	}
}

func TestAgentPickerFitsSmallTerminal(t *testing.T) {
	m := model{agentPicker: pickerState[orchestrator.AgentInfo]{target: "task", index: 9, choices: make([]orchestrator.AgentInfo, 10)}}
	for i := range m.agentPicker.choices {
		m.agentPicker.choices[i] = orchestrator.AgentInfo{Name: strings.Repeat("long-name", 20)}
	}
	for _, size := range [][2]int{{30, 10}, {50, 20}, {80, 24}} {
		m.width, m.height = size[0], size[1]
		view := m.View()
		if lipgloss.Width(view) > size[0] || lipgloss.Height(view) > size[1] {
			t.Fatalf("picker overflows %v", size)
		}
	}
}

func TestAgentPickerShowsCapabilities(t *testing.T) {
	m := model{agentPicker: pickerState[orchestrator.AgentInfo]{target: "task", choices: []orchestrator.AgentInfo{{
		Name:         "pi",
		Capabilities: orchestrator.AgentCapabilities{LiveContext: true, ToolActivity: true, StructuredReports: true},
	}}}, width: 160, height: 30}
	view := m.View()
	for _, want := range []string{"Live follow-up context", "tool activity", "structured reports"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q in agent picker: %s", want, view)
		}
	}
}
