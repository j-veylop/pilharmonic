package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"pilharmonic/internal/orchestrator"
)

// taskGroup is the board's display order. Attention comes first because it is
// the only group that cannot make progress without an operator.
type taskGroup int

const (
	groupAttention taskGroup = iota
	groupRunning
	groupQueued
	groupDone
)

func (g taskGroup) label() string {
	switch g {
	case groupAttention:
		return "NEED ACTION"
	case groupRunning:
		return "RUNNING"
	case groupQueued:
		return "QUEUED"
	default:
		return "DONE"
	}
}

// groupOf classifies a task for the board. A parent waiting for subagents is
// running work even though its own process is idle.
func (m model) groupOf(task orchestrator.Task) taskGroup {
	_, label, _ := m.taskState(task)
	switch {
	case needsAttention(task):
		return groupAttention
	case label == "RUNNING" || label == "WAITING FOR SUBAGENTS":
		return groupRunning
	case task.Status == orchestrator.Queued:
		return groupQueued
	default:
		return groupDone
	}
}

// mainRows is the board's display order: attention, running, queued, then
// completed work. Newest stays first inside each group because the service
// snapshot is newest first and the sort is stable.
func (m model) mainRows() []taskListRow {
	rows := taskListRows(m.tasks)
	sort.SliceStable(rows, func(i, j int) bool {
		return m.groupOf(rows[i].task) < m.groupOf(rows[j].task)
	})
	if !m.doneCollapsed(rows) {
		return rows
	}
	visible := make([]taskListRow, 0, len(rows))
	for _, row := range rows {
		if m.groupOf(row.task) != groupDone {
			visible = append(visible, row)
		}
	}
	return visible
}

func (m model) hasActiveRows(rows []taskListRow) bool {
	for _, row := range rows {
		if m.groupOf(row.task) != groupDone {
			return true
		}
	}
	return false
}

// doneCollapsed keeps finished tasks out of the way only while there is live
// work to show instead. An idle board still lists everything it has.
func (m model) doneCollapsed(rows []taskListRow) bool {
	return !m.doneExpanded && m.hasActiveRows(rows)
}

// ensureVisibleSelection keeps the board selection on a row the current
// grouping actually renders.
func (m *model) ensureVisibleSelection() {
	rows := m.mainRows()
	if len(rows) == 0 {
		return
	}
	current := rootIndex(m.tasks, m.selected)
	for _, row := range rows {
		if row.index == current {
			return
		}
	}
	m.selected = rows[len(rows)-1].index
}

// mainRowsCollapsed reports whether finished tasks are folded.
func (m model) mainRowsCollapsed() bool { return m.doneCollapsed(taskListRows(m.tasks)) }

// toggleDone expands or folds the completed group. Selecting a hidden task
// moves the cursor to the last visible row so the board never renders an
// invisible selection.
func (m *model) toggleDone() {
	rows := taskListRows(m.tasks)
	if !m.hasActiveRows(rows) {
		m.notice.text = "No active tasks; completed rows are already shown."
		return
	}
	m.doneExpanded = !m.doneExpanded
	m.ensureVisibleSelection()
}

// shortDuration formats an age for a task row at terminal density.
func shortDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func lastAttempt(task orchestrator.Task) (orchestrator.Attempt, bool) {
	if len(task.Attempts) == 0 {
		return orchestrator.Attempt{}, false
	}
	return task.Attempts[len(task.Attempts)-1], true
}

// taskElapsed is the time the current or last attempt has been running. Queued
// and never-started tasks have no elapsed time worth showing.
func taskElapsed(task orchestrator.Task, now time.Time) (time.Duration, bool) {
	attempt, ok := lastAttempt(task)
	if !ok {
		return 0, false
	}
	start := attempt.StartedAt
	if start.IsZero() {
		return 0, false
	}
	if attempt.FinishedAt.IsZero() {
		return now.Sub(start), true
	}
	return attempt.FinishedAt.Sub(start), true
}

// taskModel is the selector recorded on the latest attempt.
func taskModel(task orchestrator.Task) string {
	attempt, ok := lastAttempt(task)
	if !ok || attempt.RequestedModel == "" {
		return ""
	}
	return shortModel(attempt.RequestedModel)
}

// lastActivity is the most recent observable sign of life: a tool call, a
// report, an attempt boundary or a state update.
func lastActivity(task orchestrator.Task) (time.Time, string) {
	var at time.Time
	var summary string
	seen := func(candidate time.Time, text string) {
		if candidate.After(at) {
			at, summary = candidate, text
		}
	}
	for _, activity := range task.Activities {
		stamp := activity.StartedAt
		if activity.FinishedAt.After(stamp) {
			stamp = activity.FinishedAt
		}
		seen(stamp, activity.Summary())
	}
	for _, report := range task.Reports {
		seen(report.CreatedAt, report.Kind+": "+report.Message)
	}
	for _, attempt := range task.Attempts {
		seen(attempt.StartedAt, "attempt started")
		seen(attempt.FinishedAt, "attempt finished")
	}
	seen(task.UpdatedAt, "")
	return at, summary
}

// staleAfter is how long a running task may show no activity before the board
// marks it. It is deliberately longer than a slow build so the marker means
// something.
const staleAfter = 10 * time.Minute

func (m model) diffStat(task orchestrator.Task) (added, removed int, ok bool) {
	entry, found := m.diffStats[task.ID]
	if !found || entry.key != diffStatKey(task) || entry.failed {
		return 0, 0, false
	}
	return entry.added, entry.removed, true
}

func diffStatKey(task orchestrator.Task) string {
	return strings.Join([]string{task.ID, task.ReviewCommit, task.WorktreeCommit}, "|")
}

// actionHint names the keys that resolve a row's attention state, so the
// board says what to do instead of only counting tasks.
func actionHint(task orchestrator.Task) string {
	switch {
	case task.AwaitingReview():
		return "v review · y merge · n reject"
	case task.ReviewStatus == orchestrator.ReviewRejected:
		return "f follow up"
	case task.Stage() == orchestrator.StageBlocked:
		return "f send context · p report"
	case task.Stage() == orchestrator.StageWaiting, task.Stage() == orchestrator.StageInputUndelivered:
		return "f send context"
	case retryable(task):
		return "r retry"
	}
	return ""
}
