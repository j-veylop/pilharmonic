package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
)

// taskState gives a task one glyph, status word and colour. The sidebar shows
// only the glyph; other surfaces pair it with the word so status never relies
// on colour alone. A running parent whose child is still queued, running or
// merging is waiting for that subagent, which only the task list knows.
func (m model) taskState(task orchestrator.Task) (glyph, label string, color lipgloss.TerminalColor) {
	glyph, label, color = m.taskStateASCII(task)
	return m.icon(glyph), label, color
}

// icon swaps status glyphs for Nerd Font equivalents in icon mode.
func (m model) icon(glyph string) string {
	if !m.icons {
		return glyph
	}
	switch glyph {
	case "●":
		return "\uf111"
	case "✓":
		return "\uf00c"
	case "✗":
		return "\uf00d"
	case "⊘":
		return "\uf05e"
	case "◷":
		return "\uf017"
	case "◆":
		return "\uf06e"
	case "▸":
		return "\uf0da"
	}
	return glyph
}

func (m model) taskStateASCII(task orchestrator.Task) (glyph, label string, color lipgloss.TerminalColor) {
	if task.ID != "" && task.Status == orchestrator.Running && task.Stage() == orchestrator.StageWorking {
		for _, child := range m.tasks {
			if child.ParentID == task.ID && (child.Status == orchestrator.Queued || child.Status == orchestrator.Running || child.MergeStatus == orchestrator.MergeWaiting) {
				return "◷", "WAITING FOR SUBAGENTS", statusWaiting
			}
		}
	}
	if task.Status == orchestrator.Queued && task.ScheduleRequested {
		return "◷", "WAITING", statusWaiting
	}
	switch stage := task.Stage(); stage {
	case orchestrator.StageBlocked, orchestrator.StageMergeFailed:
		return "✗", strings.ToUpper(stage), statusFailed
	case orchestrator.StageWaiting, orchestrator.StageWaitingSubagents, orchestrator.StageInputUndelivered:
		return "◷", strings.ToUpper(stage), statusWaiting
	case orchestrator.StageAwaitingReview:
		return "◆", strings.ToUpper(stage), cyan
	}
	return statusState(task.Status)
}

func statusState(status orchestrator.Status) (glyph, label string, color lipgloss.TerminalColor) {
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

func statusLabel(status orchestrator.Status) string {
	_, label, color := statusState(status)
	return lipgloss.NewStyle().Foreground(color).Bold(true).Render("● " + label)
}

func (m model) taskLabel(task orchestrator.Task) string {
	_, label, color := m.taskState(task)
	return lipgloss.NewStyle().Foreground(color).Bold(true).Render("● " + label)
}

func (m model) taskStatusIcon(task orchestrator.Task) string {
	glyph, _, color := m.taskState(task)
	return lipgloss.NewStyle().Foreground(color).Bold(true).Render(glyph)
}

// taskMetaLine holds the secondary task details; the title row already shows
// status. Child rows omit the "subagent" suffix because indentation and the
// inherited-workspace badge already show ownership.
func taskMetaLine(task orchestrator.Task, width int) string {
	label := executorLabel(task)
	if task.ParentID != "" {
		label = task.Agent
	}
	return fit("  "+mutedStyle.Render(label)+"  "+workspaceBadgeFor(task).render(), width, 1)
}

// taskListRow pairs a task with its snapshot index.
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
		if visited[index] {
			return
		}
		visited[index] = true
		rows = append(rows, taskListRow{index: index, task: tasks[index]})
		for _, child := range children[tasks[index].ID] {
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

// taskTitleRow nests a child's status glyph under its parent by depth, capped
// so deep trees stay readable in a narrow sidebar.
func (m model) taskTitleRow(task orchestrator.Task, width int, selected bool) string {
	text, icon, indent := singleLine(task.Title), m.taskStatusIcon(task), ""
	if task.ParentID != "" {
		indent = strings.Repeat("  ", min(max(1, task.Depth), 3))
	}
	if !selected {
		return fit("  "+indent+icon+" "+titleStyle.Render(text), width, 1)
	}
	return selectionRow(indent+icon+" "+text, width)
}

// toolStatusStyle gives tool activity a consistent visual language in the
// output transcript and the activity panel. The glyph is the only status the
// terminal prints, so each status needs a distinct shape as well as colour.
// Running rows show the caller's running glyph: the current spinner frame, or
// a placeholder the transcript renderer replaces with it.
func toolStatusStyle(status, running string) (lipgloss.Style, string) {
	switch status {
	case orchestrator.ActivityRunning:
		return lipgloss.NewStyle().Foreground(statusRunning), running
	case orchestrator.ActivityFinished:
		return lipgloss.NewStyle().Foreground(statusCompleted), "✓"
	case orchestrator.ActivityFailed:
		return lipgloss.NewStyle().Foreground(statusFailed), "✗"
	case orchestrator.ActivityInterrupted:
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

// Nerd Font glyphs keep workspace and integration state compact in task rows.
// Every glyph is paired with a text label so the meaning survives a terminal
// without Nerd Fonts installed.
const (
	glyphWorktree   = "\ue725" // nf-oct-git_branch
	glyphMerged     = "\ue727" // nf-oct-git_merge
	glyphCheck      = "\uf00c" // nf-fa-check
	glyphConflict   = "\uf071" // nf-fa-exclamation_triangle
	glyphMergeError = "\uf00d" // nf-fa-times
	glyphReview     = "\uf06e" // nf-fa-eye
	glyphShared     = "\uf07b" // nf-fa-folder
	glyphInherited  = "\uf0c1" // nf-fa-link
)

type workspaceBadge struct {
	glyph string
	label string
	color lipgloss.TerminalColor
}

// workspaceBadgeFor summarizes where a task runs and what happened to its
// isolated work. An outstanding or rejected review takes precedence because it
// is an operator decision that still needs attention; merge results describe
// the final integration state, and a conflict remains distinct from other merge
// failures so it can be recognized at a glance.
func workspaceBadgeFor(task orchestrator.Task) workspaceBadge {
	switch {
	case task.ReviewStatus == orchestrator.ReviewRejected:
		return workspaceBadge{glyphMergeError, "rejected", statusFailed}
	case task.AwaitingReview():
		return workspaceBadge{glyphReview, "review", cyan}
	case task.MergeStatus == orchestrator.MergeFailed && task.MergeConflict():
		return workspaceBadge{glyphConflict, "conflict", statusFailed}
	case task.MergeStatus == orchestrator.MergeFailed:
		return workspaceBadge{glyphMergeError, "merge error", statusFailed}
	case task.MergeStatus == orchestrator.MergeMerged:
		return workspaceBadge{glyphMerged, "merged", statusCompleted}
	case task.MergeStatus == orchestrator.MergeUpToDate:
		return workspaceBadge{glyphCheck, "up to date", statusCompleted}
	case task.Isolated:
		return workspaceBadge{glyphWorktree, "isolated", cyan}
	case task.ParentID != "":
		return workspaceBadge{glyphInherited, "inherited", muted}
	default:
		return workspaceBadge{glyphShared, "shared", muted}
	}
}

func (b workspaceBadge) render() string {
	return lipgloss.NewStyle().Foreground(b.color).Render(b.glyph) + mutedStyle.Render(" "+b.label)
}
