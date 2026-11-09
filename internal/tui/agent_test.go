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
	if m.agentTask != task.ID || !strings.Contains(m.View(), "SELECT AGENT") {
		t.Fatal("picker not opened")
	}
	for i, agent := range m.agentChoices {
		if agent.Name == "alternate" {
			m.agentIndex = i
		}
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	got, _ := s.GetTask(task.ID)
	if got.Agent != "alternate" || m.agentTask != "" {
		t.Fatalf("selection failed: %+v", got)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("A")})
	m = key(m, tea.KeyMsg{Type: tea.KeyEsc})
	got, _ = s.GetTask(task.ID)
	if m.agentTask != "" || got.Agent != "alternate" {
		t.Fatal("cancel changed selection")
	}
}
func TestAgentPickerFitsSmallTerminal(t *testing.T) {
	m := model{agentTask: "task", agentIndex: 9, agentChoices: make([]orchestrator.AgentInfo, 10)}
	for i := range m.agentChoices {
		m.agentChoices[i] = orchestrator.AgentInfo{Name: strings.Repeat("long-name", 20)}
	}
	for _, size := range [][2]int{{30, 10}, {50, 20}, {80, 24}} {
		m.width, m.height = size[0], size[1]
		view := m.View()
		if lipgloss.Width(view) > size[0] || lipgloss.Height(view) > size[1] {
			t.Fatalf("picker overflows %v", size)
		}
	}
}
