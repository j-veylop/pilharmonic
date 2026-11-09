package tui

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"orchestrator/internal/orchestrator"
)

type routerRunner struct{}

func (routerRunner) Run(context.Context, orchestrator.Task, io.Writer) error { return nil }

func TestRouterRoutesRequestAndReturnsToBoard(t *testing.T) {
	dir := t.TempDir()
	s, err := orchestrator.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	s.RegisterRunner("general", routerRunner{})
	m := model{service: s, workspace: dir, tasks: s.ListTasks(), router: true, width: 120, height: 35}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("write tests")})
	if !strings.Contains(ansi.Strip(m.View()), "write tests") {
		t.Fatal("composer did not show the draft")
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.tasks) != 1 {
		t.Fatalf("request did not create one task: %+v", m.tasks)
	}
	task := m.tasks[0]
	if task.Prompt != "write tests" || task.Decision.PolicyVersion != orchestrator.LocalPolicyVersion {
		t.Fatalf("request not routed: %+v", task)
	}
	if !strings.Contains(m.notice, "Routed to "+orchestrator.DefaultRouteAgent) {
		t.Fatalf("routing notice missing: %q", m.notice)
	}
	if !m.router {
		t.Fatal("router closed after routing")
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "RECENT ROUTES") || !strings.Contains(view, task.Title) {
		t.Fatalf("recent route not shown: %s", view)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.router {
		t.Fatal("escape did not return to the task board")
	}
	if !strings.Contains(ansi.Strip(m.View()), "MANDATE") {
		t.Fatal("task board not shown after routing")
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	if !m.router {
		t.Fatal("/ did not reopen the router")
	}
}

func TestRouterAttentionListOpensTask(t *testing.T) {
	dir := t.TempDir()
	s, err := orchestrator.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	task, err := s.CreateTask("fix login", "work", dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordReport(task.ID, "blocked", "Need the database credentials"); err != nil {
		t.Fatal(err)
	}
	m := model{service: s, workspace: dir, tasks: s.ListTasks(), router: true, width: 120, height: 35}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "NEEDS ATTENTION") || !strings.Contains(view, "BLOCKED") {
		t.Fatalf("attention list missing blocked task: %s", view)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.routerFocus != 1 {
		t.Fatal("tab did not focus the attention list")
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.router {
		t.Fatal("router stayed open after opening a task")
	}
	if m.selected != 0 || m.tasks[0].ID != task.ID {
		t.Fatalf("wrong task selected: %+v", m.tasks)
	}
	if !strings.Contains(ansi.Strip(m.View()), "Need the database credentials") {
		t.Fatal("task details not shown")
	}
}

func TestRouterEmptyState(t *testing.T) {
	m := model{router: true, width: 100, height: 30}
	view := ansi.Strip(m.View())
	for _, want := range []string{"ROUTE WORK", "Describe the work", "Nothing needs attention.", "No requests yet."} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q in %s", want, view)
		}
	}
}

func TestRouterFitsTerminal(t *testing.T) {
	now := time.Now()
	m := model{router: true, width: 120, height: 35, tasks: []orchestrator.Task{
		{ID: "blocked", Title: "blocked task with a long title that may wrap", Status: orchestrator.Queued, Agent: "general", CreatedAt: now, Reports: []orchestrator.Report{{Kind: "blocked", Message: "need input", CreatedAt: now}}},
		{ID: "review", Title: "finished task", Status: orchestrator.Completed, Agent: "general", CreatedAt: now.Add(-time.Minute)},
		{ID: "running", Title: "running task", Status: orchestrator.Running, Agent: "general", CreatedAt: now.Add(-2 * time.Minute)},
	}}
	for _, size := range [][2]int{{120, 35}, {80, 24}, {50, 20}, {30, 10}} {
		m.width, m.height = size[0], size[1]
		view := m.View()
		if width, height := lipgloss.Width(view), lipgloss.Height(view); width > size[0] || height > size[1] {
			t.Fatalf("%v: rendered %dx%d", size, width, height)
		}
	}
}
