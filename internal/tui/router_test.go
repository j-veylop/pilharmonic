package tui

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/j-veylop/pilharmonic/internal/orchestrator"
	"github.com/j-veylop/pilharmonic/internal/tui/editor"
)

type routerRunner struct{}

func (routerRunner) Run(context.Context, orchestrator.Task, io.Writer) error { return nil }

func TestRouterRoutesRequestAndReturnsToBoard(t *testing.T) {
	configureTestProvider(t)
	dir := t.TempDir()
	s := newTestService(t)
	s.RegisterRunner("general", routerRunner{})
	m := model{service: s, workspace: dir, tasks: s.ListTasks(), router: routerState{open: true}, width: 120, height: 35}
	m = key(m, runeKey("write tests"))
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
	if !strings.Contains(m.notice.text, "Request accepted · "+orchestrator.DefaultRouteAgent) {
		t.Fatalf("request acceptance notice missing: %q", m.notice.text)
	}
	if !m.router.open || m.router.Text != "" || m.selectedID() != task.ID {
		t.Fatal("router closed, kept the draft or did not select the task after routing")
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.router.open {
		t.Fatal("escape did not return to the task board")
	}
	m = key(m, runeKey("/"))
	if m.router.open {
		t.Fatal("/ unexpectedly reopened the router")
	}
	m = key(m, runeKey("i"))
	if !m.router.open {
		t.Fatal("i did not reopen the router")
	}
}

func TestRouterCursorVisibleWithNoticeInShortTerminal(t *testing.T) {
	m := model{router: routerState{open: true, Editor: editor.Editor{Text: strings.Repeat("word ", 12), Cursor: 0}}, width: 30,
		height: 10, notice: noticeState{text: "Request accepted · general · queued · shared workspace — title"},
	}
	for i := 0; i < 3; i++ {
		m = key(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	view := m.View()
	if !strings.Contains(ansi.Strip(view), "word") {
		t.Fatalf("cursor row clipped by router panel with notice at 30x10: %s", ansi.Strip(view))
	}
	if width, height := lipgloss.Width(view), lipgloss.Height(view); width > 30 || height > 10 {
		t.Fatalf("rendered %dx%d", width, height)
	}
}

func TestRouterMentionHintsAndCompletes(t *testing.T) {
	configureTestProvider(t)
	dir := t.TempDir()
	s := newTestService(t)
	s.RegisterRunner("general", routerRunner{})
	s.RegisterRunner("reviewer", routerRunner{})
	m := model{service: s, workspace: dir, tasks: s.ListTasks(), router: routerState{open: true}, width: 120, height: 35}
	m = key(m, runeKey("@rev"))
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "Executors:") || !strings.Contains(view, "@reviewer") {
		t.Fatalf("mention hint missing: %s", view)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.router.Text != "@reviewer " {
		t.Fatalf("tab did not complete the executor: draft=%q", m.router.Text)
	}
	m = key(m, runeKey("fix it"))
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.tasks) != 1 {
		t.Fatalf("routed request did not create one task: %+v", m.tasks)
	}
	task := m.tasks[0]
	if task.Agent != "reviewer" || task.Prompt != "fix it" {
		t.Fatalf("mentioned executor was not routed: %+v", task)
	}
	if !strings.Contains(m.notice.text, "Request accepted · reviewer") {
		t.Fatalf("routing notice missing the executor: %q", m.notice.text)
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
			configureTestProvider(t)
			dir := t.TempDir()
			s, err := orchestrator.Open(filepath.Join(t.TempDir(), "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Shutdown() }()
			s.RegisterRunner("general", routerRunner{})
			s.RegisterRunner("reviewer", routerRunner{})
			m := model{service: s, workspace: dir, tasks: s.ListTasks(), router: routerState{open: true}, width: 120, height: 35}
			m = key(m, runeKey(tc.draft))
			for i := 0; i < 3; i++ {
				m = key(m, tea.KeyMsg{Type: tea.KeyLeft})
			}
			draft, cursor := m.router.Text, m.router.Cursor
			m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
			if tasks := s.ListTasks(); len(tasks) != 0 {
				t.Fatalf("rejected mention created tasks: %+v", tasks)
			}
			if len(m.tasks) != 0 {
				t.Fatalf("rejected mention added tasks to the view: %+v", m.tasks)
			}
			if m.router.Text != draft || m.router.Cursor != cursor {
				t.Fatalf("draft or cursor changed on rejection: draft=%q cursor=%d, want draft=%q cursor=%d", m.router.Text, m.router.Cursor, draft, cursor)
			}
			if !strings.Contains(m.notice.text, tc.wantErr) {
				t.Fatalf("rejection notice missing %q: %q", tc.wantErr, m.notice.text)
			}
			view := ansi.Strip(m.View())
			before, _, _ := strings.Cut(draft, " ")
			if !strings.Contains(view, before) {
				t.Fatalf("draft missing from router after rejection: %s", view)
			}
		})
	}
}

func TestRequestBoxKeepsItsDraftOverEscAndShowsTheBoard(t *testing.T) {
	m := model{router: routerState{open: true, Editor: editor.Editor{Text: "draft", Cursor: 5}}, width: 120, height: 35,
		tasks: []orchestrator.Task{{ID: "task", Title: "visible task", Status: orchestrator.Completed}}}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "visible task") || !strings.Contains(view, "draft") {
		t.Fatalf("the board is not visible under the request box: %s", view)
	}
	m = key(key(m, tea.KeyMsg{Type: tea.KeyEsc}), runeKey("i"))
	if !m.router.open || m.router.Text != "draft" || m.router.Cursor != 5 {
		t.Fatalf("esc and reopen lost the draft: %q at %d", m.router.Text, m.router.Cursor)
	}
}
