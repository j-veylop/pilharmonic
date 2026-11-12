package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
)

func taskState(task orchestrator.Task) (glyph, label string, color lipgloss.Color) {
	if task.Status == orchestrator.Queued && task.ScheduleRequested {
		return "◷", "WAITING", statusWaiting
	}
	switch stage := task.Stage(); stage {
	case "blocked", "merge failed":
		return "✗", strings.ToUpper(stage), statusFailed
	case "waiting", "waiting for subagents", "input undelivered":
		return "◷", strings.ToUpper(stage), statusWaiting
	case "awaiting review":
		return "◆", strings.ToUpper(stage), cyan
	}
	return statusState(task.Status)
}

func statusState(status orchestrator.Status) (glyph, label string, color lipgloss.Color) {
	switch status {
	case orchestrator.Running:
		return "●", "RUNNING", statusRunning
	case orchestrator.Completed:
		return "✓", "COMPLETED", statusCompleted
	case orchestrator.Failed:
		return "✗", "FAILED", statusFailed
	case orchestrator.Interrupted:
		return "⊘", "INTERRUPTED", statusFailed
	case orchestrator.Canceled:
		return "⊘", "CANCELED", muted
	case orchestrator.Queued:
		return "◷", "QUEUED", statusWaiting
	default:
		return "•", strings.ToUpper(string(status)), muted
	}
}

func taskLabel(task orchestrator.Task) string {
	_, label, color := taskState(task)
	return lipgloss.NewStyle().Foreground(color).Bold(true).Render("● " + label)
}

func statusLabel(status orchestrator.Status) string {
	_, label, color := statusState(status)
	return lipgloss.NewStyle().Foreground(color).Bold(true).Render("● " + label)
}

// taskStateWithPeers derives the displayed state from the task list. A running
// parent whose child is still queued, running or awaiting workspace integration
// is waiting for that subagent, even though its own process has not exited.
func taskStateWithPeers(task orchestrator.Task, tasks []orchestrator.Task) (glyph, label string, color lipgloss.Color) {
	if task.Status == orchestrator.Running && task.Stage() == "working" && hasActiveChild(task, tasks) {
		return "◷", "WAITING FOR SUBAGENTS", statusWaiting
	}
	return taskState(task)
}

func hasActiveChild(task orchestrator.Task, tasks []orchestrator.Task) bool {
	if task.ID == "" {
		return false
	}
	for _, child := range tasks {
		if child.ParentID != task.ID {
			continue
		}
		if child.Status == orchestrator.Queued || child.Status == orchestrator.Running || child.MergeStatus == orchestrator.MergeWaiting {
			return true
		}
	}
	return false
}

func (m model) taskState(task orchestrator.Task) (glyph, label string, color lipgloss.Color) {
	return taskStateWithPeers(task, m.tasks)
}

func (m model) taskLabel(task orchestrator.Task) string {
	_, label, color := m.taskState(task)
	return lipgloss.NewStyle().Foreground(color).Bold(true).Render("● " + label)
}

func (m model) taskStatusIcon(task orchestrator.Task) string {
	glyph, _, color := m.taskState(task)
	return lipgloss.NewStyle().Foreground(color).Bold(true).Render(glyph)
}

// taskStatusIcon is the compact task-list status: a coloured glyph, with no
// status text. Distinct glyphs per state keep meaning visible without colour.
func taskStatusIcon(task orchestrator.Task) string {
	glyph, _, color := taskState(task)
	return lipgloss.NewStyle().Foreground(color).Bold(true).Render(glyph)
}

// taskMetaLine holds the secondary task details. Status is not repeated here:
// taskTitleRow starts every row with it, so narrow sidebars keep it visible.
// Child rows omit the redundant "subagent" suffix because indentation and the
// inherited-workspace badge already communicate ownership.
func taskMetaLine(task orchestrator.Task, width int) string {
	return fit("  "+mutedStyle.Render(taskRowExecutorLabel(task))+"  "+workspaceBadgeFor(task).render(), width, 1)
}

func taskRowExecutorLabel(task orchestrator.Task) string {
	if task.ParentID != "" {
		return task.Agent
	}
	return executorLabel(task)
}

// taskListRow pairs a task with its snapshot index. Grouping and indentation
// are display concerns; the service keeps its own ordering.
type taskListRow struct {
	index int
	task  orchestrator.Task
}

// taskListRows groups delegated children directly beneath their parent while
// preserving newest-first order for top-level tasks and within each sibling
// group. An orphaned child stays at the top level.
func taskListRows(tasks []orchestrator.Task) []taskListRow {
	known := make(map[string]bool, len(tasks))
	for _, task := range tasks {
		if task.ID != "" {
			known[task.ID] = true
		}
	}
	children := make(map[string][]int)
	for i, task := range tasks {
		if task.ParentID != "" && known[task.ParentID] {
			children[task.ParentID] = append(children[task.ParentID], i)
		}
	}
	rows := make([]taskListRow, 0, len(tasks))
	visited := make([]bool, len(tasks))
	var walk func(int)
	walk = func(index int) {
		if index < 0 || index >= len(tasks) || visited[index] {
			return
		}
		visited[index] = true
		task := tasks[index]
		rows = append(rows, taskListRow{
			index: index,
			task:  task,
		})
		for _, child := range children[task.ID] {
			walk(child)
		}
	}
	for i, task := range tasks {
		if task.ParentID == "" || !known[task.ParentID] {
			walk(i)
		}
	}
	for i := range tasks {
		walk(i)
	}
	return rows
}

// taskChildIndent nests a delegated child's status glyph under its parent by
// depth, capped so deep trees stay readable in a narrow sidebar.
func taskChildIndent(task orchestrator.Task) string {
	if task.ParentID == "" {
		return ""
	}
	return strings.Repeat("  ", min(max(1, task.Depth), 3))
}

// taskTitleRow puts the coloured status glyph before the title. Selection uses
// the same rail and body styles as other selectable rows while keeping the
// status colour distinct.
func taskTitleRow(task orchestrator.Task, width int, selected bool) string {
	return taskTitleRowWithIcon(task, taskStatusIcon(task), width, selected)
}

func (m model) taskTitleRow(task orchestrator.Task, width int, selected bool) string {
	return taskTitleRowWithIcon(task, m.taskStatusIcon(task), width, selected)
}

func taskTitleRowWithIcon(task orchestrator.Task, icon string, width int, selected bool) string {
	text := strings.ReplaceAll(terminalText(task.Title), "\n", " ")
	indent := taskChildIndent(task)
	if !selected {
		return fit("  "+indent+icon+" "+titleStyle.Render(text), width, 1)
	}
	if width < 1 {
		return ""
	}
	if width == 1 {
		return selectionRailStyle.Render("▌")
	}
	if width == 2 {
		return selectionRailStyle.Render("▌ ")
	}
	body := fit(indent+icon+" "+text, width-2, 1)
	return selectionRailStyle.Render("▌ ") + selectionBodyStyle.Width(width-2).Render(body)
}

// toolStatusStyle gives tool activity a consistent visual language in the
// output transcript and the activity panel. The glyph is the only status the
// terminal prints, so each status needs a distinct shape as well as colour.
// Running rows use a placeholder that the renderer replaces with the current
// Bubble Tea spinner frame.
func toolStatusStyle(status string) (lipgloss.Style, string) {
	switch status {
	case "running":
		return lipgloss.NewStyle().Foreground(statusRunning), runningGlyphPlaceholder
	case "finished":
		return lipgloss.NewStyle().Foreground(statusCompleted), "✓"
	case "failed":
		return lipgloss.NewStyle().Foreground(statusFailed), "✗"
	case "interrupted":
		return lipgloss.NewStyle().Foreground(statusFailed), "⊘"
	default:
		return mutedStyle, "•"
	}
}

// runningGlyph returns the current frame of the Bubble Tea spinner. Models
// constructed without Run, such as in tests, fall back to the first frame.
func (m model) runningGlyph() string {
	if len(m.spinner.Spinner.Frames) == 0 {
		return defaultRunningGlyph
	}
	return m.spinner.View()
}

func (m model) toolStatusStyle(status string) (lipgloss.Style, string) {
	if status == "running" {
		return lipgloss.NewStyle().Foreground(statusRunning), m.runningGlyph()
	}
	return toolStatusStyle(status)
}

// fit limits the rendered surface to the terminal's cells, including ANSI styles.
func fit(s string, width, height int) string {
	if width < 1 || height < 1 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], width, "…")
	}
	return strings.Join(lines, "\n")
}
