package tui

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/j-veylop/pilharmonic/internal/orchestrator"
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
	m = key(key(m, runeKey("o")), tea.KeyMsg{Type: tea.KeyEnter})
	if m.setup.target != task.ID || m.agentPicker.target != task.ID || !strings.Contains(m.View(), "SETUP") || !strings.Contains(m.View(), "Executor") {
		t.Fatal("picker not opened")
	}
	for i, agent := range m.agentPicker.choices {
		if agent.Name == "alternate" {
			m.agentPicker.index = i
		}
	}

	// Long names and capability details stay inside the frame at every size.
	picker := m
	capable := orchestrator.AgentCapabilities{LiveContext: true, ToolActivity: true, StructuredReports: true}
	long := orchestrator.AgentInfo{Name: strings.Repeat("long-name", 20), Capabilities: capable}
	picker.agentPicker.choices, picker.agentPicker.index = []orchestrator.AgentInfo{long, long, long, long, long, long, long, long, long, long}, 9
	for _, size := range [][2]int{{30, 10}, {50, 20}, {80, 24}} {
		picker.width, picker.height = size[0], size[1]
		assertFits(t, picker.View(), size[0], size[1])
	}
	picker.agentPicker.choices = []orchestrator.AgentInfo{{Name: "short", Capabilities: capable}}
	picker.width, picker.height = 160, 30
	for _, want := range []string{"Live follow-up context", "tool activity", "structured reports"} {
		if view := picker.View(); !strings.Contains(view, want) {
			t.Fatalf("missing %q in agent picker: %s", want, view)
		}
	}

	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	got, _ := s.GetTask(task.ID)
	if got.Agent != "alternate" || m.agentPicker.target != "" || m.setup.target != task.ID {
		t.Fatalf("selection failed or closed the setup popup: %+v", got)
	}
	m = key(key(m, tea.KeyMsg{Type: tea.KeyEnter}), tea.KeyMsg{Type: tea.KeyEsc})
	got, _ = s.GetTask(task.ID)
	if m.agentPicker.target != "" || m.setup.target != task.ID || got.Agent != "alternate" {
		t.Fatal("esc in the picker did not go back to setup unchanged")
	}
	if m = key(m, tea.KeyMsg{Type: tea.KeyEsc}); m.setup.target != "" {
		t.Fatal("esc did not close the setup popup")
	}
}

func TestSetupOpensOnlyForHeldTasks(t *testing.T) {
	m := model{tasks: []orchestrator.Task{{ID: "run", Status: orchestrator.Running}}}
	if m = key(m, runeKey("o")); m.setup.target != "" || !strings.Contains(m.notice.text, "held") {
		t.Fatalf("setup opened for a running task: %+v %q", m.setup, m.notice.text)
	}
}

func TestAgentPickerReportsCapturedTargetThatDisappeared(t *testing.T) {
	s := newTestService(t)
	task, err := s.CreateTask("work", "mandate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := model{service: s, tasks: s.ListTasks(), width: 80, height: 24}
	m = key(key(m, runeKey("o")), tea.KeyMsg{Type: tea.KeyEnter})
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

func TestProfilePickerSelectAndCancel(t *testing.T) {
	s := newTestService(t)
	task, err := s.CreateTask("work", "mandate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	addTestProfile(t, s, `{"name":"reviewer","description":"Review only","no_integrate":true}`)
	m := model{service: s, tasks: s.ListTasks(), width: 80, height: 24}
	m = key(key(key(m, runeKey("o")), runeKey("j")), tea.KeyMsg{Type: tea.KeyEnter})
	if m.profilePicker.target != task.ID || !strings.Contains(m.View(), "Profile") {
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
	m = key(key(m, tea.KeyMsg{Type: tea.KeyEnter}), tea.KeyMsg{Type: tea.KeyEsc})
	got, _ = s.GetTask(task.ID)
	if m.profilePicker.target != "" || got.ProfileName != "reviewer" {
		t.Fatal("cancel changed selection")
	}

	// Summaries name the executor, then a configured profile; a child names
	// its ownership, and the detail still shows its profile and model.
	p := orchestrator.Profile{Name: orchestrator.DefaultProfileName}
	builtIn := orchestrator.Task{Agent: "implementer", ProfileName: p.Name, ProfileSpec: &p}
	if got := executorLabel(builtIn); got != "implementer" || profileLabel(p) != "Executor defaults" {
		t.Fatalf("built-in profile: summary %q, picker %q", got, profileLabel(p))
	}
	p.NoSubagents = true
	if got := executorLabel(builtIn); got != "implementer · profile implementer" || profileLabel(p) != p.Name {
		t.Fatalf("configured profile: summary %q, picker %q", got, profileLabel(p))
	}
	child := orchestrator.Task{ParentID: "parent", Agent: "explorer", ProfileName: "coordinator",
		Attempts: []orchestrator.Attempt{{RequestedModel: "deepseek/deepseek-flash"}}}
	if got := executorLabel(child); got != "explorer · subagent" {
		t.Fatalf("child summary: %s", got)
	}
	detail := strings.Join((model{tasks: []orchestrator.Task{child}, tab: infoTab}).detailContent(100), "\n")
	for _, want := range []string{"Profile: coordinator", "Last attempt model: deepseek/deepseek-flash"} {
		if !strings.Contains(detail, want) {
			t.Errorf("missing %q in %s", want, detail)
		}
	}
}
