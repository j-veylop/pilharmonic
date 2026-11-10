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

func TestRouterComposerSitsBelowLists(t *testing.T) {
	now := time.Now()
	m := model{router: true, width: 120, height: 35, routerDraft: "next request", tasks: []orchestrator.Task{
		{ID: "recent", Title: "recent task", Status: orchestrator.Running, Agent: "general", CreatedAt: now},
	}}
	view := ansi.Strip(m.View())
	recent := strings.Index(view, "RECENT ROUTES")
	composer := strings.Index(view, "next request")
	if recent < 0 || composer < 0 {
		t.Fatalf("missing list or composer: %s", view)
	}
	if composer < recent {
		t.Fatalf("composer is above the recent routes: %s", view)
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

func TestRouterCursorVisibleWithNoticeInShortTerminal(t *testing.T) {
	m := model{
		router:       true,
		width:        30,
		height:       10,
		notice:       "Routed to general · queued · shared workspace — title",
		routerDraft:  strings.Repeat("word ", 12),
		routerCursor: 0,
	}
	for i := 0; i < 3; i++ {
		m = key(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	view := m.View()
	if !strings.Contains(ansi.Strip(view), "▏") {
		t.Fatalf("cursor row clipped by router panel with notice at 30x10: %s", ansi.Strip(view))
	}
	if width, height := lipgloss.Width(view), lipgloss.Height(view); width > 30 || height > 10 {
		t.Fatalf("rendered %dx%d", width, height)
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

func TestRouterMentionHintsAndCompletes(t *testing.T) {
	dir := t.TempDir()
	s, err := orchestrator.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	s.RegisterRunner("general", routerRunner{})
	s.RegisterRunner("reviewer", routerRunner{})
	m := model{service: s, workspace: dir, tasks: s.ListTasks(), router: true, width: 120, height: 35}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("@rev")})
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "Executors:") || !strings.Contains(view, "@reviewer") {
		t.Fatalf("mention hint missing: %s", view)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.routerFocus != 0 || m.routerDraft != "@reviewer " {
		t.Fatalf("tab did not complete the executor: focus=%d draft=%q", m.routerFocus, m.routerDraft)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("fix it")})
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.tasks) != 1 {
		t.Fatalf("routed request did not create one task: %+v", m.tasks)
	}
	task := m.tasks[0]
	if task.Agent != "reviewer" || task.Prompt != "fix it" {
		t.Fatalf("mentioned executor was not routed: %+v", task)
	}
	if !strings.Contains(m.notice, "Routed to reviewer") {
		t.Fatalf("routing notice missing the executor: %q", m.notice)
	}
}

func TestRouterRejectedMentionPreservesDraftAndCursor(t *testing.T) {
	cases := []struct {
		name    string
		draft   string
		wantErr string
	}{{
		name:    "unknown executor",
		draft:   "@missing do work",
		wantErr: `agent "missing" unavailable`,
	}, {
		name:    "incomplete mention",
		draft:   "@reviewer",
		wantErr: "request text is required after the executor mention",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			s, err := orchestrator.Open(filepath.Join(t.TempDir(), "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Shutdown()
			s.RegisterRunner("general", routerRunner{})
			s.RegisterRunner("reviewer", routerRunner{})
			m := model{service: s, workspace: dir, tasks: s.ListTasks(), router: true, width: 120, height: 35}
			m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tc.draft)})
			for i := 0; i < 3; i++ {
				m = key(m, tea.KeyMsg{Type: tea.KeyLeft})
			}
			draft, cursor := m.routerDraft, m.routerCursor
			m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
			if tasks := s.ListTasks(); len(tasks) != 0 {
				t.Fatalf("rejected mention created tasks: %+v", tasks)
			}
			if len(m.tasks) != 0 {
				t.Fatalf("rejected mention added tasks to the view: %+v", m.tasks)
			}
			if m.routerDraft != draft || m.routerCursor != cursor {
				t.Fatalf("draft or cursor changed on rejection: draft=%q cursor=%d, want draft=%q cursor=%d", m.routerDraft, m.routerCursor, draft, cursor)
			}
			if !strings.Contains(m.notice, tc.wantErr) {
				t.Fatalf("rejection notice missing %q: %q", tc.wantErr, m.notice)
			}
			view := ansi.Strip(m.View())
			before, _, _ := strings.Cut(draft, " ")
			if !strings.Contains(view, before) {
				t.Fatalf("draft missing from router after rejection: %s", view)
			}
		})
	}
}
