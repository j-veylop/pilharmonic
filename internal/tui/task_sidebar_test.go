package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"orchestrator/internal/orchestrator"
)

func TestTaskSidebarStatusIconsAndColors(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name  string
		task  orchestrator.Task
		glyph string
		label string
		color lipgloss.Color
	}{
		{name: "running", task: orchestrator.Task{Status: orchestrator.Running}, glyph: "●", label: "RUNNING", color: statusRunning},
		{name: "completed", task: orchestrator.Task{Status: orchestrator.Completed}, glyph: "✓", label: "COMPLETED", color: statusCompleted},
		{name: "failed", task: orchestrator.Task{Status: orchestrator.Failed}, glyph: "✗", label: "FAILED", color: statusFailed},
		{name: "interrupted", task: orchestrator.Task{Status: orchestrator.Interrupted}, glyph: "⊘", label: "INTERRUPTED", color: statusFailed},
		{name: "canceled", task: orchestrator.Task{Status: orchestrator.Canceled}, glyph: "⊘", label: "CANCELED", color: muted},
		{name: "queued", task: orchestrator.Task{Status: orchestrator.Queued}, glyph: "◷", label: "QUEUED", color: statusWaiting},
		{name: "scheduled", task: orchestrator.Task{Status: orchestrator.Queued, ScheduleRequested: true}, glyph: "◷", label: "WAITING", color: statusWaiting},
		{name: "blocked report", task: orchestrator.Task{Status: orchestrator.Running, Reports: []orchestrator.Report{{Kind: "blocked", Message: "need input", CreatedAt: now}}}, glyph: "✗", label: "BLOCKED", color: statusFailed},
		{name: "waiting report", task: orchestrator.Task{Status: orchestrator.Running, Reports: []orchestrator.Report{{Kind: "waiting", Message: "waiting", CreatedAt: now}}}, glyph: "◷", label: "WAITING", color: statusWaiting},
		{name: "waiting for subagents", task: orchestrator.Task{Status: orchestrator.Completed, MergeStatus: orchestrator.MergeWaiting}, glyph: "◷", label: "WAITING FOR SUBAGENTS", color: statusWaiting},
		{name: "merge failed", task: orchestrator.Task{Status: orchestrator.Completed, MergeStatus: orchestrator.MergeFailed}, glyph: "✗", label: "MERGE FAILED", color: statusFailed},
		{name: "awaiting review", task: orchestrator.Task{Status: orchestrator.Completed, ReviewStatus: orchestrator.ReviewPending}, glyph: "◆", label: "AWAITING REVIEW", color: cyan},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.task.ID = "task"
			tc.task.Title = "work"
			tc.task.Agent = "tester"
			if got := ansi.Strip(taskStatusIcon(tc.task)); got != tc.glyph {
				t.Fatalf("status icon = %q, want %q", got, tc.glyph)
			}
			wantStyle := lipgloss.NewStyle().Foreground(tc.color).Bold(true).Render(tc.glyph)
			if icon := taskStatusIcon(tc.task); !strings.Contains(icon, wantStyle) {
				t.Fatalf("status icon %q lacks expected colour style %q", icon, wantStyle)
			}
			label := ansi.Strip(taskLabel(tc.task))
			if !strings.Contains(label, tc.label) || !strings.Contains(label, "●") {
				t.Fatalf("labelled status = %q, want %q after the shared glyph", label, tc.label)
			}
		})
	}
}

func TestTaskTitleRowShowsStatusFirstWithoutStatusText(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name  string
		task  orchestrator.Task
		glyph string
		color lipgloss.Color
	}{
		{name: "running", task: orchestrator.Task{Status: orchestrator.Running}, glyph: "●", color: statusRunning},
		{name: "completed", task: orchestrator.Task{Status: orchestrator.Completed}, glyph: "✓", color: statusCompleted},
		{name: "failed", task: orchestrator.Task{Status: orchestrator.Failed}, glyph: "✗", color: statusFailed},
		{name: "queued", task: orchestrator.Task{Status: orchestrator.Queued}, glyph: "◷", color: statusWaiting},
		{name: "blocked", task: orchestrator.Task{Status: orchestrator.Running, Reports: []orchestrator.Report{{Kind: "blocked", Message: "need input", CreatedAt: now}}}, glyph: "✗", color: statusFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.task.ID = "task"
			tc.task.Title = "work"
			tc.task.Agent = "general"
			line := taskTitleRow(tc.task, 60, false)
			plain := ansi.Strip(line)
			if !strings.HasPrefix(plain, "  "+tc.glyph+" work") {
				t.Fatalf("title row does not start with status: %q", plain)
			}
			if strings.Contains(plain, strings.ToUpper(string(tc.task.Status))) {
				t.Fatalf("title row still contains status text: %q", plain)
			}
			wantIcon := lipgloss.NewStyle().Foreground(tc.color).Bold(true).Render(tc.glyph)
			if !strings.Contains(line, wantIcon) {
				t.Fatalf("title row %q lacks styled status icon %q", line, wantIcon)
			}
			if strings.Index(line, wantIcon) > strings.Index(line, "work") {
				t.Fatalf("status is not first in title row: %q", line)
			}
		})
	}
}

func TestTaskTitleRowSelectionFitsAndKeepsStatus(t *testing.T) {
	task := orchestrator.Task{ID: "task", Title: "work", Agent: "general", Status: orchestrator.Running}
	for _, width := range []int{1, 2, 5, 20, 34} {
		row := taskTitleRow(task, width, true)
		if got := lipgloss.Width(row); got != width {
			t.Fatalf("width %d rendered %d cells: %q", width, got, ansi.Strip(row))
		}
		if strings.Contains(row, "\n") {
			t.Fatalf("selected row wrapped at width %d", width)
		}
	}
	plain := ansi.Strip(taskTitleRow(task, 20, true))
	if !strings.Contains(plain, "▌ ● work") {
		t.Fatalf("selection lost the status-first layout: %q", plain)
	}
	if row := taskTitleRow(task, 20, true); !strings.Contains(row, taskStatusIcon(task)) {
		t.Fatalf("selection lost the coloured status icon: %q", row)
	}
}

func TestTaskMetaLineOmitsRedundantChildSuffix(t *testing.T) {
	task := orchestrator.Task{ID: "child", ParentID: "parent", Depth: 1, Title: "work", Agent: "explorer"}
	plain := ansi.Strip(taskMetaLine(task, 80))
	if strings.Contains(plain, "subagent") {
		t.Fatalf("child meta line repeats ownership: %q", plain)
	}
	if !strings.Contains(plain, "explorer") || !strings.Contains(plain, "inherited") {
		t.Fatalf("child meta line lost executor or workspace: %q", plain)
	}
}

func TestTaskPanelGroupsChildrenUnderParentAndMarksOwnership(t *testing.T) {
	now := time.Now()
	tasks := []orchestrator.Task{
		{ID: "grandchild", ParentID: "child-a", Depth: 2, Title: "grand child", Agent: "tester", Status: orchestrator.Running, CreatedAt: now.Add(-30 * time.Second)},
		{ID: "child-b", ParentID: "parent", Depth: 1, Title: "second child", Agent: "tester", Status: orchestrator.Running, CreatedAt: now.Add(-time.Minute)},
		{ID: "child-a", ParentID: "parent", Depth: 1, Title: "first child", Agent: "explorer", Status: orchestrator.Running, CreatedAt: now.Add(-2 * time.Minute)},
		{ID: "parent", Title: "parent work", Agent: "general", Status: orchestrator.Running, CreatedAt: now.Add(-4 * time.Minute)},
		{ID: "other", Title: "other work", Agent: "general", Status: orchestrator.Running, CreatedAt: now.Add(-3 * time.Minute)},
	}
	m := model{width: 120, height: 40, tasks: tasks}
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
	for _, want := range []string{"▾ parent work", "↳ second child", "↳▾ first child", "↳↳ grand child"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing ownership marker %q in %q", want, plain)
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

func TestTaskSidebarCompactLayoutKeepsStatusAndExecutor(t *testing.T) {
	task := orchestrator.Task{
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
