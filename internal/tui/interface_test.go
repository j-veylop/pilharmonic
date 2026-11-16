package tui

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
	"pilharmonic/internal/tui/editor"
)

func TestHeaderSummarizesDisplayStatesAndAttention(t *testing.T) {
	now := time.Now()
	tasks := []orchestrator.Task{
		{ID: "parent", Title: "parent", Status: orchestrator.Running},
		{ID: "child", ParentID: "parent", Title: "child", Status: orchestrator.Running},
		{ID: "queued", Title: "queued", Status: orchestrator.Queued},
		{ID: "blocked", Title: "blocked", Status: orchestrator.Running,
			Reports: []orchestrator.Report{{Kind: "blocked", Message: "need input", CreatedAt: now}}},
	}
	m := model{width: 120, height: 35, tasks: tasks}
	plain := ansi.Strip(m.View())
	for _, want := range []string{"4 tasks", "● 1 running", "◷ 1 queued", "◆ 1 needs action"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("header missing %q: %q", want, plain)
		}
	}
	if strings.Contains(plain, "● 2 running") {
		t.Fatalf("parent waiting for a subagent was counted as running: %q", plain)
	}

	normal := model{width: 120, height: 35, tasks: []orchestrator.Task{{ID: "done", Status: orchestrator.Completed}}}
	if plain := ansi.Strip(normal.View()); !strings.Contains(plain, "◆ 0 need action") {
		t.Fatalf("header omits the zero action count: %q", plain)
	}
}

func TestBoardFooterShowsOnlyValidSelectedTaskActions(t *testing.T) {
	cases := []struct {
		name    string
		task    orchestrator.Task
		want    []string
		notWant []string
	}{
		{
			name:    "running",
			task:    orchestrator.Task{Status: orchestrator.Running},
			want:    []string{"c cancel", "tab output/info/activity"},
			notWant: []string{"r run", "v review"},
		},
		{
			name:    "unscheduled queued",
			task:    orchestrator.Task{Status: orchestrator.Queued},
			want:    []string{"o setup", "r run"},
			notWant: []string{"c cancel", "v review"},
		},
		{
			name:    "scheduled queued",
			task:    orchestrator.Task{Status: orchestrator.Queued, ScheduleRequested: true},
			want:    []string{"c cancel"},
			notWant: []string{"r run", "v review"},
		},
		{
			name:    "failed retry",
			task:    orchestrator.Task{Status: orchestrator.Failed},
			want:    []string{"r run"},
			notWant: []string{"c cancel", "tab output/info/activity"},
		},
		{
			name:    "merge failed retry",
			task:    orchestrator.Task{Status: orchestrator.Completed, MergeStatus: orchestrator.MergeFailed},
			want:    []string{"r run"},
			notWant: []string{"c cancel", "v review"},
		},
		{
			name:    "awaiting review",
			task:    orchestrator.Task{Status: orchestrator.Completed, ReviewStatus: orchestrator.ReviewPending, ReviewCommit: "abc"},
			want:    []string{"v review"},
			notWant: []string{"r run", "c cancel"},
		},
		{
			name:    "rejected review",
			task:    orchestrator.Task{Status: orchestrator.Completed, ReviewStatus: orchestrator.ReviewRejected},
			want:    []string{"f follow up"},
			notWant: []string{"r run", "c cancel", "v review"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.task.ID = "task"
			tc.task.Title = "work"
			for _, size := range [][2]int{{30, 10}, {50, 20}, {80, 24}, {120, 35}} {
				m := model{width: size[0], height: size[1], tasks: []orchestrator.Task{tc.task}}
				footer := strings.ReplaceAll(ansi.Strip(m.footer(size[0])), "\n", "")
				for _, want := range append([]string{"q quit"}, tc.want...) {
					if !strings.Contains(footer, want) {
						t.Fatalf("%v: footer missing %q: %q", size, want, footer)
					}
				}
				for _, unwanted := range tc.notWant {
					if strings.Contains(footer, unwanted) {
						t.Fatalf("%v: footer advertises invalid %q: %q", size, unwanted, footer)
					}
				}
				assertFits(t, m.View(), size[0], size[1])
				if size[0] < 120 {
					continue
				}
				for _, essential := range []string{"? help", "/ request"} {
					if !strings.Contains(footer, essential) {
						t.Fatalf("footer missing essential %q: %q", essential, footer)
					}
				}
			}
		})
	}
}

func TestFooterKeepsModalComposerActivityAndDetailModes(t *testing.T) {
	base := model{width: 120, height: 35, tasks: []orchestrator.Task{{ID: "task", Title: "work", Status: orchestrator.Running}}}

	detail := base
	detail.detailFocus = true
	if footer := ansi.Strip(detail.footer(120)); !strings.Contains(footer, "j/k scroll") || strings.Contains(footer, "c cancel") {
		t.Fatalf("detail footer changed: %q", footer)
	}

	activity := base
	activity.tab, activity.detailFocus = activityTab, true
	if footer := ansi.Strip(activity.footer(120)); !strings.Contains(footer, "enter expand") || !strings.Contains(footer, "e filter") || strings.Contains(footer, "esc collapse") || strings.Contains(footer, "c cancel") {
		t.Fatalf("activity list footer changed: %q", footer)
	}
	activity.activity.expanded = true
	if footer := ansi.Strip(activity.footer(120)); !strings.Contains(footer, "esc collapse") || strings.Contains(footer, "enter expand") {
		t.Fatalf("expanded activity footer changed: %q", footer)
	}

	composerModel := base
	composerModel.input = composer{kind: reportInput, Editor: editor.Editor{Text: "!keep this", Cursor: len("!keep this")}}
	if footer := ansi.Strip(composerModel.footer(120)); !strings.Contains(footer, "enter submit") || !strings.Contains(footer, "ctrl+j/alt+enter newline") {
		t.Fatalf("composer footer changed: %q", footer)
	}
	if view := ansi.Strip(composerModel.View()); !strings.Contains(view, "REPORT") || !strings.Contains(view, "!keep this") {
		t.Fatalf("composer box missing its label or draft: %q", view)
	}

	cleanup := base
	cleanup.cleanup = cleanupRequest{kind: deleteTask, ids: []string{"task"}, title: "work"}
	if footer := ansi.Strip(cleanup.View()); !strings.Contains(footer, "DELETE TASK") || !strings.Contains(footer, "y confirm") {
		t.Fatalf("cleanup footer changed: %q", footer)
	}
}

// ! walks attention tasks in grouped display order, wraps, and resets the
// detail state; composers keep it as text.
func TestAttentionShortcut(t *testing.T) {
	grouped := []orchestrator.Task{
		{ID: "child", ParentID: "parent", Depth: 1, Title: "child", Status: orchestrator.Failed},
		{ID: "normal", Title: "normal", Status: orchestrator.Completed},
		{ID: "parent", Title: "parent", Status: orchestrator.Failed},
	}
	detail := activityState{id: "call", expanded: true}
	cases := []struct {
		name     string
		m        model
		selected []int // selection after each press
		notice   string
		text     string
	}{
		{name: "main tasks only wrap", m: model{tasks: grouped, selected: 2, historyOffset: 4, activity: detail}, selected: []int{2, 2}},
		{name: "from a task without attention", m: model{tasks: grouped, selected: 1, historyOffset: 3, activity: detail}, selected: []int{2}},
		{name: "nothing needs action", m: model{tasks: grouped[1:2], historyOffset: 3}, selected: []int{0}, notice: "No tasks need action"},
		{name: "task composer", m: model{input: composer{kind: reportInput}}, text: "!"},
		{name: "request form", m: model{router: routerState{open: true}}, text: "!"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.m
			m.width, m.height = 120, 35
			if tc.text != "" {
				m = key(m, runeKey("!"))
				if m.input.Text+m.router.Text != tc.text || tc.m.router.open != m.router.open {
					t.Fatalf("composer consumed ! as a command: input=%q router=%q open=%v", m.input.Text, m.router.Text, m.router.open)
				}
				return
			}
			for _, want := range tc.selected {
				m = key(m, runeKey("!"))
				if m.selected != want {
					t.Fatalf("! selected %d, want %d", m.selected, want)
				}
				if tc.notice == "" && (m.historyOffset != 0 || m.activity.id != "" || m.activity.expanded) {
					t.Fatalf("! did not reset detail state: offset=%d activity=%+v", m.historyOffset, m.activity)
				}
			}
			if !strings.Contains(m.notice.text, tc.notice) {
				t.Fatalf("notice = %q, want %q", m.notice.text, tc.notice)
			}
		})
	}
}

func TestNarrowLayoutShowsSelectedTaskPosition(t *testing.T) {
	tasks := []orchestrator.Task{
		{ID: "child", ParentID: "parent", Depth: 1, Title: "child", Status: orchestrator.Running},
		{ID: "parent", Title: "parent", Status: orchestrator.Running},
		{ID: "other", Title: "other", Status: orchestrator.Completed},
	}
	m := model{width: 50, height: 24, tasks: tasks, selected: 0, detailFocus: true}
	if plain := ansi.Strip(m.View()); !strings.Contains(plain, "TASK 1/2") {
		t.Fatalf("narrow layout lost main task position: %q", plain)
	}
	m.tasks[0].Attempts = []orchestrator.Attempt{{ID: "attempt"}}
	m.tasks[0].Activities = []orchestrator.ToolActivity{{ID: "call", AttemptID: "attempt", Name: "read", Status: "running"}}
	for _, width := range []int{30, 50, 120} {
		m.width = width
		m.fullscreen = width == 120
		plain := ansi.Strip(m.View())
		if !strings.Contains(plain, "TASK 1/2") || !strings.Contains(plain, "read") {
			t.Fatalf("width %d: active tool hid task position or action: %q", width, plain)
		}
	}
	m.fullscreen = false
	m.width = 120
	if plain := ansi.Strip(m.View()); strings.Contains(plain, "TASK 1/2") {
		t.Fatalf("wide layout duplicated position beside the sidebar: %q", plain)
	}
}

func TestTickKeepsSelectionByIDWhenANewerTaskArrives(t *testing.T) {
	dir := t.TempDir()
	s, err := orchestrator.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()
	selected, err := s.CreateTask("selected", "work", dir)
	if err != nil {
		t.Fatal(err)
	}
	m := model{service: s, tasks: s.ListTasks(), width: 120, height: 35}
	selectTask(t, &m, selected.ID)

	if _, err := s.CreateTask("newer", "work", dir); err != nil {
		t.Fatal(err)
	}
	next, _ := m.Update(tickMsg(time.Now()))
	m = next.(model)
	if m.selectedID() != selected.ID {
		t.Fatalf("tick moved selection from %q to %q", selected.ID, m.selectedID())
	}
	if len(m.tasks) != 2 || m.tasks[0].Title != "newer" {
		t.Fatalf("task insertion not reflected in the snapshot: %+v", m.tasks)
	}
}

func TestNoticeExpiresAfterRepeatedTicks(t *testing.T) {
	m := model{notice: noticeState{text: "saved"}}
	for i := 0; i < 4; i++ {
		next, _ := m.Update(tickMsg(time.Now()))
		m = next.(model)
	}
	if m.notice.text == "" {
		t.Fatal("notice expired too early")
	}
	next, _ := m.Update(tickMsg(time.Now()))
	m = next.(model)
	if m.notice.text != "" {
		t.Fatalf("stale notice survived five ticks: %q", m.notice.text)
	}
	m.notice.text = "fresh"
	m = key(m, runeKey("?"))
	if m.notice.text != "fresh" {
		t.Fatalf("unrelated key cleared the notice: %q", m.notice.text)
	}
}

// conditionService reports a standing service condition so the notice line can
// be tested without provoking a real save failure.
type conditionService struct {
	*orchestrator.Service
	saveErr error
	startup string
}

func (c conditionService) PersistenceError() error { return c.saveErr }
func (c conditionService) StartupWarning() string  { return c.startup }

func TestNoticeLineReportsStandingServiceConditions(t *testing.T) {
	s := newTestService(t)
	saveErr := errors.New("disk full")
	m := model{service: conditionService{Service: s, saveErr: saveErr}, width: 120, height: 35}
	m.refreshTasks()
	if plain := ansi.Strip(m.View()); !strings.Contains(plain, "State save failed: disk full") {
		t.Fatalf("footer did not report the failed save: %q", plain)
	}

	startup := model{service: conditionService{Service: s, startup: "state file was corrupt; preserved as old.json"}, width: 120, height: 35}
	startup.refreshTasks()
	if plain := ansi.Strip(startup.View()); !strings.Contains(plain, "state file was corrupt") {
		t.Fatalf("footer did not report the startup warning: %q", plain)
	}

	// A standing condition must not hide a transient action notice.
	m.notice.text = "Task queued"
	m.refreshTasks()
	if plain := ansi.Strip(m.View()); !strings.Contains(plain, "Task queued") || !strings.Contains(plain, "State save failed") {
		t.Fatalf("notice and warning did not share the notice line: %q", plain)
	}
}
