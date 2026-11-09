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
