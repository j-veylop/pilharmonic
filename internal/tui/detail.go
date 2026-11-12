package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
)

func (m model) detailGeometry() (int, int) {
	l := m.layout()
	if l.width < 30 || l.height < 10 || l.bodyHeight < 3 {
		return 0, 0
	}
	return l.detailWidth, l.bodyHeight
}

// detailContent wraps every line by display cells so long output remains readable.
func (m model) detailContent(width int) []string {
	if len(m.tasks) == 0 || m.selected < 0 || m.selected >= len(m.tasks) || width < 1 {
		return nil
	}
	t := m.tasks[m.selected]
	if m.activity {
		return m.activityContent(width)
	}
	var lines []string
	add := func(s string) { lines = append(lines, strings.Split(ansi.Hardwrap(s, width, true), "\n")...) }
	configurable := t.Status == orchestrator.Queued && !t.ScheduleRequested
	add(accentStyle.Render("MANDATE"))
	add(terminalText(t.Prompt))
	agentLine := "Agent: " + terminalText(t.Agent)
	if configurable {
		agentLine += " · A to change"
	}
	add(mutedStyle.Render(agentLine))
	if n := len(t.Attempts); n > 0 && t.Attempts[n-1].RequestedModel != "" {
		add(mutedStyle.Render("Last attempt model: " + terminalText(t.Attempts[n-1].RequestedModel)))
	}
	if t.ProfileName != "" {
		label := t.ProfileName
		if t.ProfileSpec != nil {
			label = profileLabel(*t.ProfileSpec)
		}
		profileLine := "Profile: " + terminalText(label)
		if configurable {
			profileLine += " · P to change"
		}
		add(mutedStyle.Render(profileLine))
	}
	if t.ParentID != "" {
		add(mutedStyle.Render(fmt.Sprintf("Subagent of %s · depth %d", terminalText(t.ParentID), t.Depth)))
	}
	add(mutedStyle.Render("Source: " + terminalText(t.Workspace)))
	worktreeGlyph := lipgloss.NewStyle().Foreground(cyan).Render(glyphWorktree)
	if t.Isolated {
		if t.ExecutionWorkspace == "" {
			line := worktreeGlyph + " Workspace: isolated worktree requested"
			if configurable {
				line += " (w to change)"
			}
			add(accentStyle.Render(line))
			add(mutedStyle.Render("Committed HEAD only; uncommitted files are excluded."))
		} else {
			add(accentStyle.Render(worktreeGlyph + " Execution: " + terminalText(t.ExecutionWorkspace)))
			add(mutedStyle.Render("Isolated worktree · commit " + terminalText(t.IsolationCommit)))
		}
	} else if t.ParentID != "" {
		add(mutedStyle.Render(glyphInherited + " Execution: inherited from parent task"))
	} else {
		line := glyphShared + " Execution: shared workspace"
		if configurable {
			line += " (w to isolate before start)"
		}
		add(mutedStyle.Render(line))
	}
	badge := workspaceBadgeFor(t)
	switch t.MergeStatus {
	case orchestrator.MergeMerged:
		add(lipgloss.NewStyle().Foreground(badge.color).Render(badge.glyph+" Merge: merged") + mutedStyle.Render(" · commit "+terminalText(t.MergeCommit)))
		if t.MergeWarning != "" {
			add(lipgloss.NewStyle().Foreground(statusWaiting).Render("Merge warning  ") + terminalText(t.MergeWarning))
		}
	case orchestrator.MergeUpToDate:
		add(mutedStyle.Render(badge.glyph + " Merge: no changes to merge"))
	case orchestrator.MergeFailed:
		detail := "MERGE FAILED  "
		if t.MergeConflict() {
			detail = "MERGE FAILED (conflict)  "
		}
		add(lipgloss.NewStyle().Foreground(badge.color).Render(badge.glyph + " " + detail + terminalText(t.MergeError)))
	}
	switch t.ReviewStatus {
	case orchestrator.ReviewPending:
		add(accentStyle.Render("REVIEW PENDING") + mutedStyle.Render("  commit "+terminalText(t.ReviewCommit)+"  ·  merge blocked until approval  ·  v approve + merge / V reject"))
	case orchestrator.ReviewApproved:
		suffix := "  " + terminalText(t.ReviewedBy)
		if !t.ReviewedAt.IsZero() {
			suffix += "  " + t.ReviewedAt.Local().Format("Jan 02 15:04")
		}
		add(lipgloss.NewStyle().Foreground(statusCompleted).Bold(true).Render("REVIEW APPROVED") + mutedStyle.Render(suffix))
		if t.MergeStatus == "" {
			add(mutedStyle.Render("Merge scheduled for the approved commit; the result appears here."))
		}
	case orchestrator.ReviewRejected:
		suffix := "  " + terminalText(t.ReviewedBy)
		if !t.ReviewedAt.IsZero() {
			suffix += "  " + t.ReviewedAt.Local().Format("Jan 02 15:04")
		}
		add(lipgloss.NewStyle().Foreground(statusFailed).Bold(true).Render("REVIEW REJECTED") + mutedStyle.Render(suffix))
		line := "F adds follow-up context and requeues the task."
		if t.ExecutionWorkspace != "" {
			line = "Worktree retained; " + line
		}
		add(mutedStyle.Render(line))
	}
	if t.ReviewNote != "" {
		add(mutedStyle.Render("Review note: " + terminalText(t.ReviewNote)))
	}
	if children := m.childrenOf(t.ID); len(children) > 0 {
		add(mutedStyle.Render(fmt.Sprintf("Subagents: %d", len(children))))
	}
	if report, ok := t.CurrentReport(); ok {
		source := ""
		if report.Source != "" && report.Source != "operator" {
			source = "  " + mutedStyle.Render("["+terminalText(report.Source)+"]")
		}
		add(accentStyle.Render(strings.ToUpper(report.Kind)) + source + "  " + terminalText(report.Message))
	}
	if t.ContextPaused {
		add(mutedStyle.Render("Context paused (s to resume)"))
	}
	if t.Error != "" {
		add("")
		add(lipgloss.NewStyle().Foreground(statusFailed).Bold(true).Render("ERROR  " + terminalText(t.Error)))
	}
	if m.history {
		add("")
		add(accentStyle.Render("HISTORY"))
		if len(t.Context) > 0 {
			add(accentStyle.Render("CONTEXT"))
		}
		for _, c := range t.Context {
			delivery := c.Delivery
			if delivery == "" {
				delivery = "legacy"
			}
			description := "Context " + c.ID + "  " + delivery + "  " + terminalText(c.Text)
			if c.DeliveryError != "" {
				description += "  " + terminalText(c.DeliveryError)
			}
			add(description)
		}
		if len(t.Deliveries) > 0 {
			add(accentStyle.Render("DELIVERIES") + mutedStyle.Render("  per attempt"))
		}
		for _, d := range t.Deliveries {
			attempt := d.AttemptID
			if attempt == "" {
				attempt = "unstarted"
			}
			description := fmt.Sprintf("%s  attempt %s  %s  %s", d.UpdatedAt.Local().Format("15:04:05"), attempt, d.TriggerID, d.State)
			if d.Error != "" {
				description += "  " + terminalText(d.Error)
			}
			add(mutedStyle.Render(description))
		}
		if children := m.childrenOf(t.ID); len(children) > 0 {
			add(accentStyle.Render("SUBAGENTS"))
			for _, child := range children {
				add(fmt.Sprintf("%s  %s  %s", child.CreatedAt.Local().Format("Jan 02 15:04"), m.taskLabel(child), terminalText(child.Title)))
			}
		}
		if len(t.Attempts) > 0 {
			add(accentStyle.Render("ATTEMPTS"))
		}
		for _, a := range t.Attempts {
			add(fmt.Sprintf("%s  %s  %s", a.StartedAt.Local().Format("Jan 02 15:04"), statusLabel(a.Status), terminalText(a.ID)))
		}
		for _, e := range t.Events {
			add(mutedStyle.Render(e.Time.Local().Format("15:04:05")) + "  " + terminalText(e.Kind) + "  " + terminalText(e.Message))
		}
	} else {
		add("")
		add(accentStyle.Render("OUTPUT"))
		if t.Output == "" {
			add(mutedStyle.Render("Waiting for output…"))
		} else {
			lines = append(lines, m.renderedOutputFor(t.Output, width, m.runningGlyph(), m.thinking)...)
		}
	}
	return lines
}
func (m model) childrenOf(id string) []orchestrator.Task {
	var children []orchestrator.Task
	for _, task := range m.tasks {
		if task.ParentID == id {
			children = append(children, task)
		}
	}
	return children
}

func (m model) detailLineCount() int { w, _ := m.detailGeometry(); return len(m.detailContent(w - 4)) }
func (m model) detailPage() int {
	_, h := m.detailGeometry()
	if h-5 < 1 {
		return 1
	}
	return h - 5
}
func (m model) maxScroll() int { return max(0, m.detailLineCount()-m.detailPage()) }

func transcriptTab(label string, active bool) string {
	if active {
		return activeTabStyle.Render(" " + label + " ")
	}
	return inactiveTabStyle.Render(" " + label + " ")
}

func (m model) detailTabRow(t orchestrator.Task, width int) string {
	row := transcriptTab("OUTPUT", !m.history && !m.activity) + faintStyle.Render("│") +
		transcriptTab("HISTORY", m.history && !m.activity) + faintStyle.Render("  ·  ") +
		mutedStyle.Render(terminalText(executorLabel(t)))
	return fit(row, width, 1)
}

func (m model) detailPanel(width, height int) string {
	if m.agentPicker.target != "" {
		return m.agentPanel(width, height)
	}
	if m.profilePicker.target != "" {
		return m.profilePanel(width, height)
	}
	inner := m.layout().panelInner(width)
	if len(m.tasks) == 0 {
		return panel(titleStyle.Render("TASK DETAILS")+"\n\n"+mutedStyle.Render("Select a task to view details."), width, height)
	}
	t := m.tasks[m.selected]
	titleLine := titleStyle.Render(strings.ReplaceAll(terminalText(t.Title), "\n", " ")) + "  " + m.taskLabel(t)
	if action := m.currentAction(t); action != "" {
		titleLine = lipgloss.NewStyle().Foreground(cyan).Render(action)
	}
	if position, total, ok := m.selectedPosition(); ok && !m.sidebarVisible() && inner >= 8 {
		titleLine = mutedStyle.Render(fmt.Sprintf("TASK %d/%d  ", position, total)) + titleLine
	}
	lines := []string{m.detailTabRow(t, inner), fit(titleLine, inner, 1)}
	content := m.detailContent(inner)
	available := max(0, m.layout().detailRows(height))
	offset := min(max(0, m.historyOffset), max(0, len(content)-available))
	end := len(content) - offset
	start := max(0, end-available)
	if available > 0 {
		lines = append(lines, m.highlightSelection(content, start, end, inner)...)
	}
	label := "OUTPUT"
	if m.activity {
		label = "ACTIVITY"
	} else if m.history {
		label = "HISTORY"
	}
	focused := m.detailFocus || m.fullscreen
	focus := "l to focus · j/k scroll"
	if focused {
		focus = "j/k scroll · h tasks"
	}
	if m.activity {
		switch {
		case m.activityExpanded && focused:
			focus = "j/k scroll · esc collapse"
		case m.activityExpanded:
			focus = "l to focus · esc collapse"
		case focused:
			focus = "j/k select · enter expand"
		default:
			focus = "l to focus · enter expand"
		}
	}
	position := fmt.Sprintf("%s  %d–%d / %d  ·  %s", label, min(start+1, end), end, len(content), focus)
	lines = append(lines, mutedStyle.Render(position))
	return focusedPanel(strings.Join(lines, "\n"), width, height, m.detailFocus || m.fullscreen)
}

func (m model) selectedTask() (orchestrator.Task, bool) {
	if m.selected < 0 || m.selected >= len(m.tasks) {
		return orchestrator.Task{}, false
	}
	return m.tasks[m.selected], true
}

// retryable mirrors the task states Service.Retry accepts. Integration that is
// already pending is excluded because Retry refuses it.
func retryable(task orchestrator.Task) bool {
	if task.DiscardPending || task.MergeStatus == orchestrator.MergeWaiting {
		return false
	}
	if task.Status == orchestrator.Completed {
		return task.MergeStatus == orchestrator.MergeFailed
	}
	return task.Status == orchestrator.Failed || task.Status == orchestrator.Interrupted || task.Status == orchestrator.Canceled
}

// selectedTaskHints advertises only operations the existing command guards and
// service methods accept for the selected task state.
func (m model) selectedTaskHints() []string {
	task, ok := m.selectedTask()
	if !ok {
		return nil
	}
	var hints []string
	if task.AwaitingReview() {
		hints = append(hints, "v approve + merge", "V reject")
	}
	if task.ReviewStatus == orchestrator.ReviewRejected {
		hints = append(hints, "F follow up")
	}
	switch {
	case task.Status == orchestrator.Running:
		hints = append(hints, "c cancel", "t activity")
	case task.Status == orchestrator.Queued && !task.ScheduleRequested:
		hints = append(hints, "r run")
	case task.Status == orchestrator.Queued:
		hints = append(hints, "c cancel")
	}
	if retryable(task) {
		hints = append(hints, "R retry")
	}
	return hints
}
