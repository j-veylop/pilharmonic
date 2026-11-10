package tui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
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
			want:    []string{"c cancel", "t activity"},
			notWant: []string{"r run", "R retry", "v approve"},
		},
		{
			name:    "unscheduled queued",
			task:    orchestrator.Task{Status: orchestrator.Queued},
			want:    []string{"r run"},
			notWant: []string{"c cancel", "R retry", "v approve"},
		},
		{
			name:    "scheduled queued",
			task:    orchestrator.Task{Status: orchestrator.Queued, ScheduleRequested: true},
			want:    []string{"c cancel"},
			notWant: []string{"r run", "R retry", "v approve"},
		},
		{
			name:    "failed retry",
			task:    orchestrator.Task{Status: orchestrator.Failed},
			want:    []string{"R retry"},
			notWant: []string{"r run", "c cancel", "t activity"},
		},
		{
			name:    "merge failed retry",
			task:    orchestrator.Task{Status: orchestrator.Completed, MergeStatus: orchestrator.MergeFailed},
			want:    []string{"R retry"},
			notWant: []string{"r run", "c cancel", "v approve"},
		},
		{
			name:    "awaiting review",
			task:    orchestrator.Task{Status: orchestrator.Completed, ReviewStatus: orchestrator.ReviewPending, ReviewCommit: "abc"},
			want:    []string{"v approve", "V reject"},
			notWant: []string{"r run", "c cancel", "R retry"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.task.ID = "task"
			tc.task.Title = "work"
			m := model{width: 120, height: 35, tasks: []orchestrator.Task{tc.task}}
			footer := ansi.Strip(m.footer(120))
			for _, want := range tc.want {
				if !strings.Contains(footer, want) {
					t.Fatalf("footer missing %q: %q", want, footer)
				}
			}
			for _, unwanted := range tc.notWant {
				if strings.Contains(footer, unwanted) {
					t.Fatalf("footer advertises invalid %q: %q", unwanted, footer)
				}
			}
			for _, essential := range []string{"? help", "/ request", "n new", "q quit"} {
				if !strings.Contains(footer, essential) {
					t.Fatalf("footer missing essential %q: %q", essential, footer)
				}
			}
		})
	}
}

func TestBoardFooterIsContextualAndDimensionsStayBounded(t *testing.T) {
	task := orchestrator.Task{ID: "queued", Title: "work", Status: orchestrator.Queued}
	for _, size := range [][2]int{{30, 10}, {50, 20}, {80, 24}, {120, 35}} {
		m := model{width: size[0], height: size[1], tasks: []orchestrator.Task{task}}
		footer := ansi.Strip(m.footer(size[0]))
		if !strings.Contains(footer, "r run") || !strings.Contains(footer, "q quit") {
			t.Fatalf("%v: missing contextual run or quit hint: %q", size, footer)
		}
		if strings.Contains(footer, "R retry") || strings.Contains(footer, "c cancel") {
			t.Fatalf("%v: footer advertises an invalid action: %q", size, footer)
		}
		view := m.View()
		if lipgloss.Width(view) > size[0] || lipgloss.Height(view) > size[1] {
			t.Fatalf("%v: rendered %dx%d", size, lipgloss.Width(view), lipgloss.Height(view))
		}
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
	activity.activity = true
	if footer := ansi.Strip(activity.footer(120)); !strings.Contains(footer, "v filter") || strings.Contains(footer, "c cancel") {
		t.Fatalf("activity footer changed: %q", footer)
	}

	composerModel := base
	composerModel.input = composer{kind: newTaskInput, text: "!keep this", cursor: len("!keep this")}
	if footer := ansi.Strip(composerModel.footer(120)); !strings.Contains(footer, "!keep this") || !strings.Contains(footer, "enter submit") {
		t.Fatalf("composer footer changed: %q", footer)
	}

	cleanup := base
	cleanup.cleanup = cleanupRequest{kind: deleteTask, ids: []string{"task"}, title: "work"}
	if footer := ansi.Strip(cleanup.footer(120)); !strings.Contains(footer, "DELETE TASK") || strings.Contains(footer, "c cancel ·") {
		t.Fatalf("cleanup footer changed: %q", footer)
	}
}

func TestAttentionJumpFollowsGroupedOrderAndWraps(t *testing.T) {
	tasks := []orchestrator.Task{
		{ID: "child", ParentID: "parent", Depth: 1, Title: "child", Status: orchestrator.Failed},
		{ID: "normal", Title: "normal", Status: orchestrator.Completed},
		{ID: "parent", Title: "parent", Status: orchestrator.Failed},
	}
	m := model{tasks: tasks, selected: 2, historyOffset: 4, activityID: "call", activityExpanded: true}
	if !m.jumpAttention() {
		t.Fatal("attention jump found nothing")
	}
	if m.selected != 0 || m.historyOffset != 0 {
		t.Fatalf("jump selected %d offset %d, want child at 0 with reset scroll", m.selected, m.historyOffset)
	}
	if !m.jumpAttention() {
		t.Fatal("attention jump did not wrap")
	}
	if m.selected != 2 {
		t.Fatalf("wrapped jump selected %d, want parent at 2", m.selected)
	}

	m.selected = 1
	m.historyOffset = 3
	if !m.jumpAttention() || m.selected != 2 || m.historyOffset != 0 {
		t.Fatalf("jump from a non-attention task selected %d offset %d", m.selected, m.historyOffset)
	}
}

func TestAttentionJumpWithoutAttentionKeepsSelection(t *testing.T) {
	m := model{tasks: []orchestrator.Task{{ID: "one", Status: orchestrator.Completed}, {ID: "two", Status: orchestrator.Queued}}, selected: 1, historyOffset: 3}
	if m.jumpAttention() {
		t.Fatal("jump reported attention when none exists")
	}
	if m.selected != 1 || m.historyOffset != 3 {
		t.Fatalf("selection or scroll changed without attention: selected=%d offset=%d", m.selected, m.historyOffset)
	}
}

func TestAttentionShortcutResetsDetailState(t *testing.T) {
	s, err := orchestrator.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	first, err := s.CreateTask("first", "work", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateTask("second", "work", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordReport(second.ID, "blocked", "need input"); err != nil {
		t.Fatal(err)
	}
	m := model{service: s, tasks: s.ListTasks(), selected: 1, width: 120, height: 35, historyOffset: 5, activityID: "call", activityExpanded: true}
	if m.selectedID() != first.ID {
		t.Fatalf("test setup selected %q, want %q", m.selectedID(), first.ID)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("!")})
	if m.selectedID() != second.ID {
		t.Fatalf("! selected %q, want %q", m.selectedID(), second.ID)
	}
	if m.historyOffset != 0 || m.activityID != "" || m.activityExpanded {
		t.Fatalf("! did not reset detail state: offset=%d activity=%q expanded=%v", m.historyOffset, m.activityID, m.activityExpanded)
	}
}

func TestAttentionShortcutNoAttentionShowsNotice(t *testing.T) {
	s, err := orchestrator.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	if _, err := s.CreateTask("first", "work", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	m := model{service: s, tasks: s.ListTasks(), width: 120, height: 35}
	selected := m.selectedID()
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("!")})
	if m.selectedID() != selected {
		t.Fatalf("! changed selection without attention: %q -> %q", selected, m.selectedID())
	}
	if !strings.Contains(m.notice, "No tasks need action") {
		t.Fatalf("missing no-action notice: %q", m.notice)
	}
}

func TestAttentionShortcutStaysTextInComposers(t *testing.T) {
	m := model{input: composer{kind: newTaskInput}}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("!")})
	if m.input.text != "!" {
		t.Fatalf("task composer consumed ! as a command: %q", m.input.text)
	}

	m = model{router: true}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("!")})
	if m.routerDraft != "!" || !m.router {
		t.Fatalf("router composer consumed ! as a command: %q", m.routerDraft)
	}
}

func TestNarrowLayoutShowsSelectedTaskPosition(t *testing.T) {
	tasks := []orchestrator.Task{
		{ID: "child", ParentID: "parent", Depth: 1, Title: "child", Status: orchestrator.Running},
		{ID: "parent", Title: "parent", Status: orchestrator.Running},
		{ID: "other", Title: "other", Status: orchestrator.Completed},
	}
	m := model{width: 50, height: 24, tasks: tasks, selected: 0}
	if plain := ansi.Strip(m.View()); !strings.Contains(plain, "TASK 2/3") {
		t.Fatalf("narrow layout lost grouped task position: %q", plain)
	}
	m.tasks[0].Attempts = []orchestrator.Attempt{{ID: "attempt"}}
	m.tasks[0].Activities = []orchestrator.ToolActivity{{ID: "call", AttemptID: "attempt", Name: "read", Status: "running"}}
	for _, width := range []int{30, 50, 120} {
		m.width = width
		m.fullscreen = width == 120
		plain := ansi.Strip(m.View())
		if !strings.Contains(plain, "TASK 2/3") || !strings.Contains(plain, "read") {
			t.Fatalf("width %d: active tool hid task position or action: %q", width, plain)
		}
	}
	m.fullscreen = false
	m.width = 120
	if plain := ansi.Strip(m.View()); strings.Contains(plain, "TASK 2/3") {
		t.Fatalf("wide layout duplicated position beside the sidebar: %q", plain)
	}
}

func TestHelpDocumentsAttentionJump(t *testing.T) {
	m := model{width: 120, height: 40, help: true}
	m.scrollHelp(1)
	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "Next task needing action") {
		t.Fatalf("help does not document the action jump: %q", plain)
	}
}
