package tui

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"pilharmonic/internal/orchestrator"
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
	m = key(m, runeKey("A"))
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
	m = key(m, runeKey("A"))
	m = key(m, tea.KeyMsg{Type: tea.KeyEsc})
	got, _ = s.GetTask(task.ID)
	if m.agentPicker.target != "" || got.Agent != "alternate" {
		t.Fatal("cancel changed selection")
	}
}

func TestAgentPickerReportsCapturedTargetThatDisappeared(t *testing.T) {
	s := newTestService(t)
	task, err := s.CreateTask("work", "mandate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := model{service: s, tasks: s.ListTasks(), width: 80, height: 24}
	m = key(m, runeKey("A"))
	if err := s.DeleteTask(task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTask("other", "another mandate", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	next, _ := m.Update(tickMsg{})
	m = key(next.(model), tea.KeyMsg{Type: tea.KeyEnter})
	if m.notice.text != "task not found" || m.agentPicker.target != "" {
		t.Fatalf("missing captured task was not reported/closed: notice=%q picker=%+v", m.notice.text, m.agentPicker)
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
		Name:         "general",
		Capabilities: orchestrator.AgentCapabilities{LiveContext: true, ToolActivity: true, StructuredReports: true},
	}}}, width: 160, height: 30}
	view := m.View()
	for _, want := range []string{"Live follow-up context", "tool activity", "structured reports"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q in agent picker: %s", want, view)
		}
	}
}

func TestProfilePickerSelectAndCancel(t *testing.T) {
	s := newTestService(t)
	task, err := s.CreateTask("work", "mandate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RegisterProfile(orchestrator.Profile{Name: "reviewer", Description: "Review only", NoIntegrate: true}); err != nil {
		t.Fatal(err)
	}
	m := model{service: s, tasks: s.ListTasks(), width: 80, height: 24}
	m = key(m, runeKey("P"))
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
	m = key(m, runeKey("P"))
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
