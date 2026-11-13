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

func TestRouterComposerSitsBelowLists(t *testing.T) {
	now := time.Now()
	m := model{router: routerState{open: true, draft: "next request"}, width: 120, height: 35, tasks: []orchestrator.Task{
		{ID: "recent", Title: "recent task", Status: orchestrator.Running, Agent: "general", CreatedAt: now},
	}}
	view := ansi.Strip(m.View())
	recent := strings.Index(view, "RECENT TASKS")
	composer := strings.Index(view, "next request")
	if recent < 0 || composer < 0 {
		t.Fatalf("missing list or composer: %s", view)
	}
	if composer < recent {
		t.Fatalf("composer is above the recent routes: %s", view)
	}
}

func TestRouterEmptyState(t *testing.T) {
	m := model{router: routerState{open: true}, width: 100, height: 30}
	view := ansi.Strip(m.View())
	for _, want := range []string{"NEW REQUEST", "Describe the work", "Nothing needs action.", "No tasks yet. Submit a request to create one."} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q in %s", want, view)
		}
	}
}

func TestRouterCursorVisibleWithNoticeInShortTerminal(t *testing.T) {
	m := model{router: routerState{open: true, draft: strings.Repeat("word ", 12), cursor: 0}, width: 30,
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

func TestRouterFitsTerminal(t *testing.T) {
	now := time.Now()
	m := model{router: routerState{open: true}, width: 120, height: 35, tasks: []orchestrator.Task{
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
	if m.router.focus != 0 || m.router.draft != "@reviewer " {
		t.Fatalf("tab did not complete the executor: focus=%d draft=%q", m.router.focus, m.router.draft)
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
			draft, cursor := m.router.draft, m.router.cursor
			m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
			if tasks := s.ListTasks(); len(tasks) != 0 {
				t.Fatalf("rejected mention created tasks: %+v", tasks)
			}
			if len(m.tasks) != 0 {
				t.Fatalf("rejected mention added tasks to the view: %+v", m.tasks)
			}
			if m.router.draft != draft || m.router.cursor != cursor {
				t.Fatalf("draft or cursor changed on rejection: draft=%q cursor=%d, want draft=%q cursor=%d", m.router.draft, m.router.cursor, draft, cursor)
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

func TestRouterEscapePreservesDraftForReopen(t *testing.T) {
	m := model{router: routerState{open: true, draft: "long request", cursor: len("long request")}}
	m = key(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.router.open {
		t.Fatal("escape did not leave the router")
	}
	if m.router.draft != "long request" || m.router.cursor != len("long request") {
		t.Fatalf("escape discarded the draft: %q at %d", m.router.draft, m.router.cursor)
	}
	m = key(m, runeKey("/"))
	if !m.router.open || m.router.draft != "long request" {
		t.Fatalf("reopen did not restore the draft: router=%v draft=%q", m.router.open, m.router.draft)
	}
	if m.router.cursor != len("long request") {
		t.Fatalf("reopen cursor = %d, want end of draft", m.router.cursor)
	}
}

func TestRouterRecentListOpensSelectedTask(t *testing.T) {
	now := time.Now()
	m := model{router: routerState{open: true}, width: 120, height: 35, tasks: []orchestrator.Task{
		{ID: "newest", Title: "newest task", Status: orchestrator.Queued, Agent: "general", CreatedAt: now},
		{ID: "older", Title: "older task", Status: orchestrator.Completed, Agent: "general", CreatedAt: now.Add(-time.Minute)},
	}}
	m = key(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.router.focus != routerFocusRecent {
		t.Fatalf("tab focused %d, want the recent list when nothing needs action", m.router.focus)
	}
	if !strings.Contains(ansi.Strip(m.View()), "RECENT TASKS") {
		t.Fatal("recent list missing after tab")
	}
	m = key(m, runeKey("j"))
	if m.router.recentSelected != 1 {
		t.Fatalf("j selected row %d, want 1", m.router.recentSelected)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.router.open {
		t.Fatal("enter on a recent task left the router open")
	}
	if m.selected != 1 || m.tasks[1].ID != "older" {
		t.Fatalf("wrong recent task opened: selected=%d tasks=%+v", m.selected, m.tasks)
	}
	if !strings.Contains(m.notice.text, "older task") {
		t.Fatalf("opening a recent task set no notice: %q", m.notice.text)
	}
}

func TestRouterTabCyclesComposerAndBothLists(t *testing.T) {
	now := time.Now()
	m := model{router: routerState{open: true}, width: 120, height: 35, tasks: []orchestrator.Task{
		{ID: "blocked", Title: "blocked task", Status: orchestrator.Queued, Agent: "general", CreatedAt: now, Reports: []orchestrator.Report{{Kind: "blocked", Message: "need input", CreatedAt: now}}},
		{ID: "plain", Title: "plain task", Status: orchestrator.Completed, Agent: "general", CreatedAt: now.Add(-time.Minute)},
	}}
	for _, want := range []int{routerFocusAttention, routerFocusRecent, routerFocusComposer} {
		m = key(m, tea.KeyMsg{Type: tea.KeyTab})
		if m.router.focus != want {
			t.Fatalf("tab focus = %d, want %d", m.router.focus, want)
		}
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.router.focus != routerFocusRecent {
		t.Fatalf("shift+tab focus = %d, want the recent list", m.router.focus)
	}
}

func TestRouterRecentListSkipsEmptyList(t *testing.T) {
	m := model{router: routerState{open: true}, width: 120, height: 35}
	m = key(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.router.focus != routerFocusComposer {
		t.Fatalf("tab focused %d with no task lists to show", m.router.focus)
	}
}

func TestRouterRecentRowShowsSelectionOnlyWhenFocused(t *testing.T) {
	now := time.Now()
	m := model{router: routerState{open: true, recentSelected: 1}, width: 120, height: 35, tasks: []orchestrator.Task{
		{ID: "newest", Title: "newest task", Status: orchestrator.Queued, Agent: "general", CreatedAt: now},
		{ID: "older", Title: "older task", Status: orchestrator.Completed, Agent: "general", CreatedAt: now.Add(-time.Minute)},
	}}
	if view := ansi.Strip(m.routerPanel(120, 20)); strings.Contains(view, "▌") {
		t.Fatalf("recent list drew a selection while the composer had focus: %s", view)
	}
	m.router.focus = routerFocusRecent
	view := ansi.Strip(m.routerPanel(120, 20))
	var selectedRow string
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "older task") {
			selectedRow = line
		}
	}
	if !strings.Contains(selectedRow, "▌") {
		t.Fatalf("selected recent row has no selection rail: %s", view)
	}
}

func TestRouterSelectionClampsToDrawnRecentRows(t *testing.T) {
	now := time.Now()
	tasks := make([]orchestrator.Task, 0, 12)
	for i := 0; i < 12; i++ {
		tasks = append(tasks, orchestrator.Task{ID: string(rune('a' + i)), Title: "task", Status: orchestrator.Queued, CreatedAt: now.Add(-time.Duration(i) * time.Minute)})
	}
	m := model{router: routerState{open: true, focus: routerFocusRecent, recentSelected: 11}, tasks: tasks}
	m.clampRouter()
	if m.router.recentSelected != routerListLimit-1 {
		t.Fatalf("recent selection = %d, want %d", m.router.recentSelected, routerListLimit-1)
	}
	m = key(m, runeKey("j"))
	if m.router.recentSelected != routerListLimit-1 {
		t.Fatalf("j moved past the drawn rows to %d", m.router.recentSelected)
	}
}

func TestRouterRecentNavigationKeepsDraft(t *testing.T) {
	now := time.Now()
	m := model{router: routerState{open: true, draft: "draft request", cursor: len("draft request")}, width: 120, height: 35, tasks: []orchestrator.Task{
		{ID: "one", Title: "one", Status: orchestrator.Queued, Agent: "general", CreatedAt: now},
	}}
	m = key(m, tea.KeyMsg{Type: tea.KeyTab})
	m = key(m, runeKey("j"))
	if m.router.draft != "draft request" || m.router.cursor != len("draft request") {
		t.Fatalf("list navigation touched the draft: %q at %d", m.router.draft, m.router.cursor)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.router.open {
		t.Fatal("escape did not leave the router from the recent list")
	}
	if m.router.draft != "draft request" {
		t.Fatalf("escape discarded the draft: %q", m.router.draft)
	}
}

func TestRouterFocusedListKeepsSelectionVisible(t *testing.T) {
	now := time.Now()
	var tasks []orchestrator.Task
	for i := 0; i < 10; i++ {
		tasks = append(tasks, orchestrator.Task{
			ID:        fmt.Sprintf("id-%d", i),
			Title:     fmt.Sprintf("task-%d", i),
			Status:    orchestrator.Completed,
			Agent:     "general",
			CreatedAt: now.Add(-time.Duration(i) * time.Minute),
		})
	}
	for _, size := range [][2]int{{120, 35}, {80, 24}, {80, 20}, {60, 16}} {
		m := model{router: routerState{open: true, focus: routerFocusRecent, recentSelected: routerListLimit - 1}, width: size[0], height: size[1], tasks: tasks}
		view := ansi.Strip(m.View())
		if !strings.Contains(view, fmt.Sprintf("task-%d", routerListLimit-1)) {
			t.Fatalf("%v: selected recent row is not drawn: %s", size, view)
		}
		if !strings.Contains(view, "▌") {
			t.Fatalf("%v: selected recent row has no rail: %s", size, view)
		}
		if width, height := lipgloss.Width(m.View()), lipgloss.Height(m.View()); width > size[0] || height > size[1] {
			t.Fatalf("%v: rendered %dx%d", size, width, height)
		}
	}
	var blocked []orchestrator.Task
	for i := 0; i < 9; i++ {
		blocked = append(blocked, orchestrator.Task{
			ID:        fmt.Sprintf("blocked-%d", i),
			Title:     fmt.Sprintf("blocked-task-%d", i),
			Status:    orchestrator.Queued,
			Agent:     "general",
			CreatedAt: now.Add(-time.Duration(i) * time.Minute),
			Reports:   []orchestrator.Report{{Kind: "blocked", Message: "need input", CreatedAt: now}},
		})
	}
	m := model{router: routerState{open: true, focus: routerFocusAttention, selected: routerListLimit - 1}, width: 60, height: 16, tasks: blocked}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, fmt.Sprintf("blocked-task-%d", routerListLimit-1)) {
		t.Fatalf("selected action row is not drawn: %s", view)
	}
}

func TestRouterRecentSelectionFollowsTaskAcrossRefresh(t *testing.T) {
	dir := t.TempDir()
	s := newTestService(t)
	first, err := s.CreateTask("first task", "first", dir)
	if err != nil {
		t.Fatal(err)
	}
	m := model{service: s, workspace: dir, tasks: s.ListTasks(), router: routerState{open: true}, width: 120, height: 35}
	m = key(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.router.focus != routerFocusRecent {
		t.Fatalf("tab focused %d, want the recent list", m.router.focus)
	}
	if _, err := s.CreateTask("second task", "second", dir); err != nil {
		t.Fatal(err)
	}
	m.refreshTasks()
	if m.router.recentSelected >= len(m.tasks) || m.tasks[m.router.recentSelected].ID != first.ID {
		t.Fatalf("recent rail lost its task across a refresh: %+v", m.tasks)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.selected >= len(m.tasks) || m.tasks[m.selected].ID != first.ID {
		t.Fatalf("enter opened the wrong task after a refresh: %+v", m.tasks)
	}
}

func TestRouterOpenClearsPreviousActivitySelection(t *testing.T) {
	now := time.Now()
	m := model{router: routerState{open: true, focus: routerFocusRecent}, width: 120, height: 35, activity: activityState{open: true, expanded: true, id: "call-1"}, tasks: []orchestrator.Task{{ID: "other", Title: "other task", Status: orchestrator.Completed, Agent: "general", CreatedAt: now}}}
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.router.open {
		t.Fatal("enter on the recent list left the router open")
	}
	if m.activity.id != "" || m.activity.expanded {
		t.Fatalf("previous activity selection survived opening a task: id=%q expanded=%v", m.activity.id, m.activity.expanded)
	}
}

func TestRouterEmptyFocusedListReturnsToDraftWithNotice(t *testing.T) {
	now := time.Now()
	m := model{router: routerState{open: true, focus: routerFocusRecent}, width: 120, height: 35,
		tasks: []orchestrator.Task{{ID: "one", Title: "one", Status: orchestrator.Queued, Agent: "general", CreatedAt: now}},
	}
	m.tasks = nil
	m = key(m, runeKey("j"))
	if m.router.focus != routerFocusComposer {
		t.Fatalf("focus stayed on an empty list: %d", m.router.focus)
	}
	if !strings.Contains(m.notice.text, "empty") {
		t.Fatalf("focus reset was silent: %q", m.notice.text)
	}
}

func TestRouterAnchorRecentSelectionStaysInsideDrawnRows(t *testing.T) {
	now := time.Now()
	tasks := make([]orchestrator.Task, 0, 10)
	for i := 0; i < 10; i++ {
		tasks = append(tasks, orchestrator.Task{ID: fmt.Sprintf("id-%d", i), Title: fmt.Sprintf("task-%d", i), Status: orchestrator.Queued, Agent: "general", CreatedAt: now.Add(-time.Duration(i) * time.Minute)})
	}
	m := model{router: routerState{open: true, focus: routerFocusRecent, recentSelected: routerListLimit - 1}, tasks: tasks}
	m.anchorRecentSelection(tasks[routerListLimit].ID)
	if m.router.recentSelected != routerListLimit-1 {
		t.Fatalf("anchor moved a task beyond the drawn rows to %d", m.router.recentSelected)
	}
	m.router.recentSelected = len(tasks) + 3
	m.anchorRecentSelection("deleted")
	if m.router.recentSelected != routerListLimit-1 {
		t.Fatalf("stale selection left outside the drawn rows: %d", m.router.recentSelected)
	}
}

func TestRouterRefreshPullsAStaleRecentSelectionBackIntoView(t *testing.T) {
	dir := t.TempDir()
	s := newTestService(t)
	for i := 0; i < 3; i++ {
		if _, err := s.CreateTask(fmt.Sprintf("task %d", i), "work", dir); err != nil {
			t.Fatal(err)
		}
	}
	m := model{service: s, workspace: dir, tasks: s.ListTasks(), router: routerState{open: true, focus: routerFocusRecent, recentSelected: 9}, width: 80, height: 24}
	m.refreshTasks()
	if m.router.recentSelected >= len(m.tasks) {
		t.Fatalf("stale recent selection survived a refresh: %d tasks=%d", m.router.recentSelected, len(m.tasks))
	}
	if !strings.Contains(ansi.Strip(m.View()), "▌") {
		t.Fatal("recent list drew no selection rail after a refresh")
	}
}

func TestRouterFocusedRowKeepsItsSectionHeadingVisible(t *testing.T) {
	now := time.Now()
	var tasks []orchestrator.Task
	for i := 0; i < 10; i++ {
		tasks = append(tasks, orchestrator.Task{ID: fmt.Sprintf("id-%d", i), Title: fmt.Sprintf("task-%d", i), Status: orchestrator.Completed, Agent: "general", CreatedAt: now.Add(-time.Duration(i) * time.Minute)})
	}
	for _, size := range [][2]int{{120, 35}, {80, 24}, {80, 20}} {
		m := model{router: routerState{open: true, focus: routerFocusRecent, recentSelected: routerListLimit - 1}, width: size[0], height: size[1], tasks: tasks}
		view := ansi.Strip(m.View())
		if !strings.Contains(view, "RECENT TASKS") {
			t.Fatalf("%v: focused section heading scrolled away: %s", size, view)
		}
		if !strings.Contains(view, fmt.Sprintf("task-%d", routerListLimit-1)) {
			t.Fatalf("%v: selected row is not drawn: %s", size, view)
		}
	}
}

func TestRouterTickKeepsRecentRailOnItsTask(t *testing.T) {
	dir := t.TempDir()
	s := newTestService(t)
	first, err := s.CreateTask("first task", "first", dir)
	if err != nil {
		t.Fatal(err)
	}
	m := model{service: s, workspace: dir, tasks: s.ListTasks(), router: routerState{open: true}, width: 80, height: 24}
	m = key(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.router.focus != routerFocusRecent {
		t.Fatalf("tab focused %d, want the recent list", m.router.focus)
	}
	if _, err := s.CreateTask("second task", "second", dir); err != nil {
		t.Fatal(err)
	}
	ticked, _ := m.Update(tickMsg(time.Now()))
	m = ticked.(model)
	if m.router.recentSelected >= len(m.tasks) || m.tasks[m.router.recentSelected].ID != first.ID {
		t.Fatalf("tick moved the recent rail off its task: %+v", m.tasks)
	}
	if !strings.Contains(ansi.Strip(m.View()), "▌") {
		t.Fatal("recent list lost its selection rail after a tick")
	}
}
