package tui

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
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
	if !m.router.open {
		t.Fatal("router closed after routing")
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "RECENT TASKS") || !strings.Contains(view, task.Title) {
		t.Fatalf("recent route not shown: %s", view)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.router.open {
		t.Fatal("escape did not return to the task board")
	}
	if !strings.Contains(ansi.Strip(m.View()), "MANDATE") {
		t.Fatal("task board not shown after routing")
	}
	m = key(m, runeKey("/"))
	if !m.router.open {
		t.Fatal("/ did not reopen the router")
	}
}

func TestRouterAttentionListOpensTask(t *testing.T) {
	dir := t.TempDir()
	s := newTestService(t)
	task, err := s.CreateTask("fix login", "work", dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordReport(task.ID, "blocked", "Need the database credentials"); err != nil {
		t.Fatal(err)
	}
	m := model{service: s, workspace: dir, tasks: s.ListTasks(), router: routerState{open: true}, width: 120, height: 35}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "NEEDS ACTION") || !strings.Contains(view, "BLOCKED") {
		t.Fatalf("attention list missing blocked task: %s", view)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.router.focus != 1 {
		t.Fatal("tab did not focus the attention list")
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.router.open {
		t.Fatal("router stayed open after opening a task")
	}
	if m.selected != 0 || m.tasks[0].ID != task.ID {
		t.Fatalf("wrong task selected: %+v", m.tasks)
	}
	if !strings.Contains(ansi.Strip(m.View()), "Need the database credentials") {
		t.Fatal("task details not shown")
	}
}

func TestRouterCursorVisibleWithNoticeInShortTerminal(t *testing.T) {
	m := model{router: routerState{open: true, editor: editor{text: strings.Repeat("word ", 12), cursor: 0}}, width: 30,
		height: 10, notice: noticeState{text: "Request accepted · general · queued · shared workspace — title"},
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
	if m.router.focus != 0 || m.router.text != "@reviewer " {
		t.Fatalf("tab did not complete the executor: focus=%d draft=%q", m.router.focus, m.router.text)
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
			draft, cursor := m.router.text, m.router.cursor
			m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
			if tasks := s.ListTasks(); len(tasks) != 0 {
				t.Fatalf("rejected mention created tasks: %+v", tasks)
			}
			if len(m.tasks) != 0 {
				t.Fatalf("rejected mention added tasks to the view: %+v", m.tasks)
			}
			if m.router.text != draft || m.router.cursor != cursor {
				t.Fatalf("draft or cursor changed on rejection: draft=%q cursor=%d, want draft=%q cursor=%d", m.router.text, m.router.cursor, draft, cursor)
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

func routerTasks(n int, blocked bool) []orchestrator.Task {
	now := time.Now()
	var tasks []orchestrator.Task
	for i := 0; i < n; i++ {
		task := orchestrator.Task{ID: fmt.Sprintf("id-%d", i), Title: fmt.Sprintf("task-%d", i), Status: orchestrator.Completed, Agent: "general", CreatedAt: now.Add(-time.Duration(i) * time.Minute)}
		if blocked {
			task.Status, task.Reports = orchestrator.Queued, []orchestrator.Report{{Kind: "blocked", Message: "need input", CreatedAt: now}}
		}
		tasks = append(tasks, task)
	}
	return tasks
}

func TestRouterPanelLayout(t *testing.T) {
	empty := ansi.Strip(model{router: routerState{open: true}, width: 100, height: 30}.View())
	for _, want := range []string{"NEW REQUEST", "Describe the work", "Nothing needs action.", "No tasks yet. Submit a request to create one."} {
		if !strings.Contains(empty, want) {
			t.Fatalf("empty form missing %q: %s", want, empty)
		}
	}
	draft := ansi.Strip(model{router: routerState{open: true, editor: editor{text: "next request"}}, width: 120, height: 35, tasks: routerTasks(1, false)}.View())
	if recent, box := strings.Index(draft, "RECENT TASKS"), strings.Index(draft, "next request"); recent < 0 || box < recent || strings.Contains(draft, "▌") {
		t.Fatalf("box must sit below the lists, with no rail while the draft has focus: %s", draft)
	}
	// A focused list scrolls so its selected row and its heading stay visible.
	for _, size := range [][2]int{{120, 35}, {80, 24}, {80, 20}, {60, 16}, {30, 10}} {
		for _, tc := range []struct {
			focus   int
			blocked bool
			heading string
		}{{routerFocusRecent, false, "RECENT TASKS"}, {routerFocusAttention, true, "NEEDS ACTION"}} {
			m := model{router: routerState{open: true, focus: tc.focus, selected: routerListLimit - 1, recentSelected: routerListLimit - 1}, width: size[0], height: size[1], tasks: routerTasks(10, tc.blocked)}
			view := m.View()
			assertFits(t, view, size[0], size[1])
			if plain := ansi.Strip(view); size[1] >= 16 && (!strings.Contains(plain, fmt.Sprintf("task-%d", routerListLimit-1)) || !strings.Contains(plain, "▌") || size[1] >= 20 && !strings.Contains(plain, tc.heading)) {
				t.Fatalf("%v focus %d: selected row or heading scrolled away: %s", size, tc.focus, plain)
			}
		}
	}
}

func TestRouterFocusNavigationKeepsTheDraft(t *testing.T) {
	m := model{router: routerState{open: true, editor: editor{text: "draft", cursor: 5}}, width: 120, height: 35, tasks: append(routerTasks(1, true), routerTasks(12, false)...)}
	for _, want := range []int{routerFocusAttention, routerFocusRecent, routerFocusComposer} {
		if m = key(m, tea.KeyMsg{Type: tea.KeyTab}); m.router.focus != want {
			t.Fatalf("tab focus = %d, want %d", m.router.focus, want)
		}
	}
	if m = key(m, tea.KeyMsg{Type: tea.KeyShiftTab}); m.router.focus != routerFocusRecent {
		t.Fatalf("shift+tab focus = %d, want the recent list", m.router.focus)
	}
	for i := 0; i < 12; i++ {
		m = key(m, runeKey("j"))
	}
	if m.router.recentSelected != routerListLimit-1 || m.router.text != "draft" || m.router.cursor != 5 {
		t.Fatalf("navigation left the drawn rows or touched the draft: %d %q", m.router.recentSelected, m.router.text)
	}
	m.tasks = nil
	if m = key(m, runeKey("j")); m.router.focus != routerFocusComposer || !strings.Contains(m.notice.text, "empty") {
		t.Fatalf("an emptied list kept focus silently: %d %q", m.router.focus, m.notice.text)
	}
	if empty := key(model{router: routerState{open: true}}, tea.KeyMsg{Type: tea.KeyTab}); empty.router.focus != routerFocusComposer {
		t.Fatal("tab focused an empty list")
	}
	m = key(key(m, tea.KeyMsg{Type: tea.KeyEsc}), runeKey("/"))
	if !m.router.open || m.router.text != "draft" || m.router.cursor != 5 {
		t.Fatalf("esc and reopen lost the draft: %q at %d", m.router.text, m.router.cursor)
	}
}

// The recent rail stays on its task when a refresh inserts a newer one, is
// pulled back into the drawn rows, and Enter opens that task on the board.
func TestRouterRecentListFollowsItsTaskAndOpensIt(t *testing.T) {
	dir := t.TempDir()
	s := newTestService(t)
	first, err := s.CreateTask("first task", "first", dir)
	if err != nil {
		t.Fatal(err)
	}
	m := model{service: s, workspace: dir, tasks: s.ListTasks(), router: routerState{open: true}, width: 80, height: 24,
		activity: activityState{open: true, expanded: true, id: "call-1"}}
	if m = key(m, tea.KeyMsg{Type: tea.KeyTab}); m.router.focus != routerFocusRecent {
		t.Fatalf("tab focused %d, want the recent list", m.router.focus)
	}
	if _, err := s.CreateTask("second task", "second", dir); err != nil {
		t.Fatal(err)
	}
	ticked, _ := m.Update(tickMsg(time.Now()))
	m = ticked.(model)
	if m.tasks[m.router.recentSelected].ID != first.ID || !strings.Contains(ansi.Strip(m.View()), "▌") {
		t.Fatalf("tick moved the recent rail off its task: %+v", m.tasks)
	}
	m.router.recentSelected = 9
	if m.refreshTasks(); m.router.recentSelected >= len(m.tasks) {
		t.Fatalf("stale recent selection survived a refresh: %d", m.router.recentSelected)
	}
	m.router.recentSelected = 1
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.router.open || m.tasks[m.selected].ID != first.ID || !strings.Contains(m.notice.text, "first task") {
		t.Fatalf("enter did not open the selected task: open=%v selected=%d notice=%q", m.router.open, m.selected, m.notice.text)
	}
	if m.activity.id != "" || m.activity.expanded {
		t.Fatal("the previous activity selection survived opening a task")
	}
}
