package tui

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
)

func TestTaskSidebarStatusIconsAndLabels(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name  string
		task  orchestrator.Task
		glyph string
		label string
	}{
		{name: "running", task: orchestrator.Task{Status: orchestrator.Running}, glyph: "●", label: "RUNNING"},
		{name: "completed", task: orchestrator.Task{Status: orchestrator.Completed}, glyph: "✓", label: "COMPLETED"},
		{name: "failed", task: orchestrator.Task{Status: orchestrator.Failed}, glyph: "✗", label: "FAILED"},
		{name: "interrupted", task: orchestrator.Task{Status: orchestrator.Interrupted}, glyph: "⊘", label: "INTERRUPTED"},
		{name: "canceled", task: orchestrator.Task{Status: orchestrator.Canceled}, glyph: "⊘", label: "CANCELED"},
		{name: "queued", task: orchestrator.Task{Status: orchestrator.Queued}, glyph: "◷", label: "QUEUED"},
		{name: "scheduled", task: orchestrator.Task{Status: orchestrator.Queued, ScheduleRequested: true}, glyph: "◷", label: "WAITING"},
		{name: "blocked report", task: orchestrator.Task{Status: orchestrator.Running, Reports: []orchestrator.Report{{Kind: "blocked", Message: "need input", CreatedAt: now}}}, glyph: "✗", label: "BLOCKED"},
		{name: "waiting report", task: orchestrator.Task{Status: orchestrator.Running, Reports: []orchestrator.Report{{Kind: "waiting", Message: "waiting", CreatedAt: now}}}, glyph: "◷", label: "WAITING"},
		{name: "waiting for subagents", task: orchestrator.Task{Status: orchestrator.Completed, MergeStatus: orchestrator.MergeWaiting}, glyph: "◷", label: "WAITING FOR SUBAGENTS"},
		{name: "merge failed", task: orchestrator.Task{Status: orchestrator.Completed, MergeStatus: orchestrator.MergeFailed}, glyph: "✗", label: "MERGE FAILED"},
		{name: "awaiting review", task: orchestrator.Task{Status: orchestrator.Completed, ReviewStatus: orchestrator.ReviewPending}, glyph: "◆", label: "AWAITING REVIEW"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.task.ID = "task"
			tc.task.Title = "work"
			tc.task.Agent = "tester"
			if got := ansi.Strip((model{}).taskStatusIcon(tc.task)); got != tc.glyph {
				t.Fatalf("status icon = %q, want %q", got, tc.glyph)
			}
			label := ansi.Strip((model{}).taskLabel(tc.task))
			if !strings.Contains(label, tc.label) || !strings.Contains(label, "●") {
				t.Fatalf("labelled status = %q, want %q after the shared glyph", label, tc.label)
			}
			// The sidebar row starts with its number, then the glyph alone, never the word.
			if plain := ansi.Strip((model{}).taskTitleRow(tc.task, 1, 9, 60, false)); !strings.HasPrefix(plain, "  1 "+tc.glyph+" work") || strings.Contains(plain, tc.label) {
				t.Fatalf("title row %q does not lead with the number and status glyph", plain)
			}
		})
	}
}

func TestTaskStateShowsRunningParentWaitingForSubagents(t *testing.T) {
	parent := orchestrator.Task{ID: "parent", Title: "parent work", Status: orchestrator.Running}
	cases := []struct {
		name  string
		child orchestrator.Task
		label string
		glyph string
		color lipgloss.TerminalColor
	}{
		{name: "queued child", child: orchestrator.Task{ID: "child", ParentID: "parent", Status: orchestrator.Queued}, glyph: "◷", label: "WAITING FOR SUBAGENTS", color: statusWaiting},
		{name: "running child", child: orchestrator.Task{ID: "child", ParentID: "parent", Status: orchestrator.Running}, glyph: "◷", label: "WAITING FOR SUBAGENTS", color: statusWaiting},
		{name: "child waiting to integrate", child: orchestrator.Task{ID: "child", ParentID: "parent", Status: orchestrator.Completed, MergeStatus: orchestrator.MergeWaiting}, glyph: "◷", label: "WAITING FOR SUBAGENTS", color: statusWaiting},
		{name: "finished child", child: orchestrator.Task{ID: "child", ParentID: "parent", Status: orchestrator.Completed}, glyph: "●", label: "RUNNING", color: statusRunning},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := model{tasks: []orchestrator.Task{parent, tc.child}}
			glyph, label, color := m.taskState(parent)
			if glyph != tc.glyph || label != tc.label || color != tc.color {
				t.Fatalf("parent state = %q %q %q, want %q %q %q", glyph, label, color, tc.glyph, tc.label, tc.color)
			}
			if got := ansi.Strip(m.taskStatusIcon(parent)); got != tc.glyph {
				t.Fatalf("parent icon = %q, want %q", got, tc.glyph)
			}
			if got := ansi.Strip(m.taskLabel(parent)); !strings.Contains(got, tc.label) {
				t.Fatalf("parent label = %q, want %q", got, tc.label)
			}
			if tc.child.MergeStatus != orchestrator.MergeWaiting {
				if got := ansi.Strip(m.taskLabel(tc.child)); strings.Contains(got, "WAITING FOR SUBAGENTS") {
					t.Fatalf("child inherited the parent's waiting label: %q", got)
				}
			}
		})
	}
}

func TestTaskStateKeepsAttentionReportOverSubagentWait(t *testing.T) {
	now := time.Now()
	parent := orchestrator.Task{ID: "parent", Status: orchestrator.Running, Reports: []orchestrator.Report{{Kind: "blocked", Message: "need input", CreatedAt: now}}}
	child := orchestrator.Task{ID: "child", ParentID: "parent", Status: orchestrator.Running}
	m := model{tasks: []orchestrator.Task{parent, child}}
	if _, label, _ := m.taskState(parent); label != "BLOCKED" {
		t.Fatalf("blocked parent = %q, want BLOCKED", label)
	}
}

func TestTaskPanelGroupsChildrenUnderParentByIndentation(t *testing.T) {
	now := time.Now()
	tasks := []orchestrator.Task{
		{ID: "grandchild", ParentID: "child-a", Depth: 2, Title: "grand child", Agent: "tester", Status: orchestrator.Running, CreatedAt: now.Add(-30 * time.Second)},
		{ID: "child-b", ParentID: "parent", Depth: 1, Title: "second child", Agent: "tester", Status: orchestrator.Running, CreatedAt: now.Add(-time.Minute)},
		{ID: "child-a", ParentID: "parent", Depth: 1, Title: "first child", Agent: "explorer", Status: orchestrator.Running, CreatedAt: now.Add(-2 * time.Minute)},
		{ID: "parent", Title: "parent work", Agent: "general", Status: orchestrator.Running, CreatedAt: now.Add(-4 * time.Minute)},
		{ID: "other", Title: "other work", Agent: "general", Status: orchestrator.Running, CreatedAt: now.Add(-3 * time.Minute)},
	}
	m := model{width: 120, height: 40, tasks: tasks, selected: 4}
	plain := ansi.Strip(m.taskPanel(60, 30))
	order := []string{"parent work", "second child", "first child", "grand child", "other work"}
	last := -1
	for _, title := range order {
		position := strings.Index(plain, title)
		if position < 0 {
			t.Fatalf("missing %q in grouped panel: %q", title, plain)
		}
		if position < last {
			t.Fatalf("task %q is out of grouped order: %q", title, plain)
		}
		last = position
	}
	for _, want := range []string{"1 ◷ parent work", "2   ● second child", "3   ◷ first child", "4     ● grand child", "5 ● other work"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing numbered layout %q in %q", want, plain)
		}
	}
	for _, unwanted := range []string{"▾", "↳"} {
		if strings.Contains(plain, unwanted) {
			t.Fatalf("task rows still use an arrow marker %q: %q", unwanted, plain)
		}
	}
}

func TestTaskSelectionFollowsGroupedRows(t *testing.T) {
	now := time.Now()
	tasks := []orchestrator.Task{
		{ID: "grandchild", ParentID: "child-a", Depth: 2, Title: "grand child", CreatedAt: now.Add(-30 * time.Second)},
		{ID: "child-b", ParentID: "parent", Depth: 1, Title: "second child", CreatedAt: now.Add(-time.Minute)},
		{ID: "child-a", ParentID: "parent", Depth: 1, Title: "first child", CreatedAt: now.Add(-2 * time.Minute)},
		{ID: "parent", Title: "parent work", CreatedAt: now.Add(-4 * time.Minute)},
	}
	m := model{tasks: tasks, selected: 3}
	for _, want := range []int{1, 2, 0, 0} {
		if !m.vimNavigation("j") {
			t.Fatal("j was not handled")
		}
		if m.selected != want {
			t.Fatalf("j selected index %d, want %d", m.selected, want)
		}
	}
	m.vimNavigation("g")
	m.vimNavigation("g")
	if m.selected != 3 {
		t.Fatalf("gg selected index %d, want parent at 3", m.selected)
	}
	m.vimNavigation("G")
	if m.selected != 0 {
		t.Fatalf("G selected index %d, want last grouped row at 0", m.selected)
	}
}

// Selected rows fill their width exactly and compact panels still lead with
// status, then title and executor.
func TestTaskSidebarRowsFitAndLeadWithStatus(t *testing.T) {
	task := orchestrator.Task{ID: "task", Title: "work", Agent: "general", Status: orchestrator.Running}
	for _, width := range []int{1, 2, 5, 20, 34} {
		row := (model{}).taskTitleRow(task, 3, 9, width, true)
		if got := lipgloss.Width(row); got != width || strings.Contains(row, "\n") {
			t.Fatalf("width %d rendered %d cells: %q", width, got, ansi.Strip(row))
		}
	}
	if plain := ansi.Strip((model{}).taskTitleRow(task, 3, 9, 20, true)); !strings.Contains(plain, "▌ 3 ● work") {
		t.Fatalf("selection lost the numbered status-first layout: %q", plain)
	}
	child := orchestrator.Task{ID: "child", ParentID: "parent", Depth: 1, Title: "child", Status: orchestrator.Running}
	if selected := ansi.Strip((model{}).taskTitleRow(child, 2, 9, 40, true)); !strings.Contains(selected, "▌ 2   ● child") {
		t.Fatalf("selected child row lost its numbered indentation: %q", selected)
	}

	task = orchestrator.Task{
		ID: "task", Title: "Fix narrow sidebar status visibility", Prompt: "work",
		Status: orchestrator.Running, Agent: "implementer",
	}
	m := model{width: 80, height: 20, tasks: []orchestrator.Task{task}}
	for _, width := range []int{12, 14, 20, 24} {
		rendered := m.taskPanel(width, 12)
		if lipgloss.Width(rendered) != width || lipgloss.Height(rendered) != 12 {
			t.Fatalf("width %d rendered %dx%d", width, lipgloss.Width(rendered), lipgloss.Height(rendered))
		}
		plain := ansi.Strip(rendered)
		if strings.Contains(plain, "RUNNING") {
			t.Fatalf("width %d still shows status text: %q", width, plain)
		}
		iconAt := strings.Index(plain, "● ")
		titleAt := strings.Index(plain, "Fix")
		executorAt := strings.Index(plain, "impl")
		if iconAt < 0 || titleAt < 0 || executorAt < 0 {
			t.Fatalf("width %d lost status, title or executor: %q", width, plain)
		}
		if iconAt > titleAt || iconAt > executorAt {
			t.Fatalf("width %d does not show status first: %q", width, plain)
		}
		if strings.Contains(plain, "shared") {
			t.Fatalf("width %d kept the workspace label instead of the executor: %q", width, plain)
		}
	}
}

// The task list numbers every row so operators can refer to a task by its
// place, not just a truncated title.
func TestTaskPanelNumbersRowsInGroupedOrder(t *testing.T) {
	now := time.Now()
	tasks := []orchestrator.Task{
		{ID: "child", ParentID: "parent", Depth: 1, Title: "child work", Agent: "tester", Status: orchestrator.Running, CreatedAt: now},
		{ID: "parent", Title: "parent work", Agent: "general", Status: orchestrator.Running, CreatedAt: now.Add(-time.Minute)},
		{ID: "other", Title: "other work", Agent: "general", Status: orchestrator.Completed, CreatedAt: now.Add(-2 * time.Minute)},
	}
	m := model{width: 120, height: 40, tasks: tasks, selected: 0}
	plain := ansi.Strip(m.taskPanel(60, 30))
	for _, want := range []string{"1 ◷ parent work", "▌ 2   ● child work", "3 ✓ other work"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing numbered row %q in %q", want, plain)
		}
	}
}

// numberedTasks builds n top-level running tasks in display order.
func numberedTasks(n int) []orchestrator.Task {
	now := time.Now()
	tasks := make([]orchestrator.Task, n)
	for i := range tasks {
		tasks[i] = orchestrator.Task{
			ID: "task-" + strconv.Itoa(i), Title: "work " + strconv.Itoa(i), Agent: "general",
			Status: orchestrator.Running, CreatedAt: now.Add(-time.Duration(i) * time.Minute),
		}
	}
	return tasks
}

// Numbers keep their position when the list scrolls; they are not row slots.
func TestTaskPanelNumbersSurviveScrolling(t *testing.T) {
	m := model{width: 80, height: 20, tasks: numberedTasks(12), selected: 11}
	plain := ansi.Strip(m.taskPanel(40, 12))
	for _, want := range []string{" 9 ● work 8", "10 ● work 9", "11 ● work 10", "▌ 12 ● work 11"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing scrolled number %q in %q", want, plain)
		}
	}
}

// The gutter is sized from the task count, not the visible rows, so numbers do
// not shift as the window moves.
func TestTaskPanelPadsNumberColumnFromTotal(t *testing.T) {
	m := model{width: 80, height: 20, tasks: numberedTasks(12), selected: 0}
	plain := ansi.Strip(m.taskPanel(40, 12))
	for _, want := range []string{"▌  1 ● work 0", " 2 ● work 1", " 3 ● work 2", " 4 ● work 3"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing padded number %q in %q", want, plain)
		}
	}
}

// The number gives way when indentation would otherwise squeeze out the status
// and title, not only when the number itself does not fit.
func TestTaskTitleRowDropsNumberBeforeIndentedStatus(t *testing.T) {
	child := orchestrator.Task{ID: "child", ParentID: "parent", Depth: 2, Title: "work", Status: orchestrator.Running}
	narrow := ansi.Strip((model{}).taskTitleRow(child, 2, 9, 12, false))
	if strings.Contains(narrow, "2") || !strings.Contains(narrow, "● work") {
		t.Fatalf("narrow indented row = %q, want status and title without the number", narrow)
	}
	roomy := ansi.Strip((model{}).taskTitleRow(child, 2, 9, 14, false))
	if !strings.Contains(roomy, "2     ● work") {
		t.Fatalf("row with room = %q, want the numbered indented row", roomy)
	}
}

func TestTaskNumberColumnAlignsMultiDigitPositions(t *testing.T) {
	if got, want := taskNumberColumn(1, 9), "1 "; got != want {
		t.Fatalf("single-digit column = %q, want %q", got, want)
	}
	if got, want := taskNumberColumn(1, 12), " 1 "; got != want {
		t.Fatalf("aligned single-digit column = %q, want %q", got, want)
	}
	if got, want := taskNumberColumn(12, 12), "12 "; got != want {
		t.Fatalf("double-digit column = %q, want %q", got, want)
	}
	if got, want := taskNumberColumn(5, 100), "  5 "; got != want {
		t.Fatalf("triple-digit column = %q, want %q", got, want)
	}
	if got, want := taskNumberColumn(100, 100), "100 "; got != want {
		t.Fatalf("widest triple-digit column = %q, want %q", got, want)
	}
}
