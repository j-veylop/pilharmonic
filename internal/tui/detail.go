package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/j-veylop/pilharmonic/internal/orchestrator"
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
	t, ok := m.selectedTask()
	if !ok || width < 1 {
		return nil
	}
	if m.tab == activityTab {
		return m.activityContent(width)
	}
	var lines []string
	add := func(rows ...string) {
		for _, s := range rows {
			lines = append(lines, strings.Split(ansi.Hardwrap(s, width, true), "\n")...)
		}
	}
	if m.tab == infoTab {
		m.taskSummary(t, add)
		mergeSummary(t, add)
		reviewSummary(t, add)
		if children := m.childrenOf(t.ID); len(children) > 0 {
			add(mutedStyle.Render(fmt.Sprintf("Subagents: %d", len(children))))
		}
		m.taskHistory(t, width, add)
		return lines
	}
	// The latest attempt already displays its own error in the conversation.
	if t.Error != "" && (len(t.Attempts) == 0 || t.Attempts[len(t.Attempts)-1].Error != t.Error) {
		add(lipgloss.NewStyle().Foreground(statusFailed).Bold(true).Render("ERROR  " + terminalText(t.Error)))
	}
	if t.ReviewStatus == orchestrator.ReviewPending {
		add(accentStyle.Render("REVIEW PENDING") + mutedStyle.Render("  commit "+terminalText(t.ReviewCommit)+"  ·  merge blocked until approval  ·  v to review"))
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
	if len(lines) > 0 {
		add("")
	}
	return append(lines, m.conversationContent(t, width)...)
}

// taskSummary shows the mandate and what will run it. Usage is the task's own
// recorded history; the price line describes the rate in force now.
func (m model) taskSummary(t orchestrator.Task, add func(...string)) {
	configurable := t.Status == orchestrator.Queued && !t.ScheduleRequested
	change := func(line, hint string) string {
		if configurable {
			line += hint
		}
		return mutedStyle.Render(line)
	}
	add(accentStyle.Render("MANDATE"), terminalText(t.Prompt), change("Agent: "+terminalText(t.Agent), " · o to change"))
	if n := len(t.Attempts); n > 0 && t.Attempts[n-1].RequestedModel != "" {
		add(mutedStyle.Render("Last attempt model: " + terminalText(t.Attempts[n-1].RequestedModel)))
	}
	if usage := t.Usage(); !usage.IsZero() {
		add(mutedStyle.Render("Usage: " + terminalText(taskUsageLine(usage))))
		if price, ok := priceLine(usage.Model, time.Now()); ok {
			add(mutedStyle.Render(terminalText(price)))
		}
	}
	if t.ProfileName != "" {
		add(change("Profile: "+terminalText(taskProfileLabel(t)), " · o to change"))
	}
	if t.ParentID != "" {
		add(mutedStyle.Render(fmt.Sprintf("Subagent of %s · depth %d", terminalText(t.ParentID), t.Depth)))
	}
	add(mutedStyle.Render("Source: " + terminalText(t.Workspace)))
	worktreeGlyph := accentStyle.Render(glyphWorktree)
	switch {
	case t.Isolated && t.ExecutionWorkspace == "":
		line := worktreeGlyph + " Workspace: isolated worktree requested"
		if configurable {
			line += " (o to change)"
		}
		add(accentStyle.Render(line), mutedStyle.Render("Committed HEAD only; uncommitted files are excluded."))
	case t.Isolated:
		add(accentStyle.Render(worktreeGlyph+" Execution: "+terminalText(t.ExecutionWorkspace)),
			mutedStyle.Render("Isolated worktree · commit "+terminalText(t.IsolationCommit)))
	case t.ParentID != "":
		add(mutedStyle.Render(glyphInherited + " Execution: inherited from parent task"))
	default:
		add(change(glyphShared+" Execution: shared workspace", " (o to change)"))
	}
}

// mergeSummary shows the outcome of merging isolated work.
func mergeSummary(t orchestrator.Task, add func(...string)) {
	badge := workspaceBadgeFor(t)
	style := lipgloss.NewStyle().Foreground(badge.color)
	switch t.MergeStatus {
	case orchestrator.MergeMerged:
		add(style.Render(badge.glyph+" Merge: merged") + mutedStyle.Render(" · commit "+terminalText(t.MergeCommit)))
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
		add(style.Render(badge.glyph + " " + detail + terminalText(t.MergeError)))
	}
}

func reviewSummary(t orchestrator.Task, add func(...string)) {
	reviewed := "  " + terminalText(t.ReviewedBy)
	if !t.ReviewedAt.IsZero() {
		reviewed += "  " + t.ReviewedAt.Local().Format("Jan 02 15:04")
	}
	switch t.ReviewStatus {
	case orchestrator.ReviewApproved:
		add(lipgloss.NewStyle().Foreground(statusCompleted).Bold(true).Render("REVIEW APPROVED") + mutedStyle.Render(reviewed))
		if t.MergeStatus == "" {
			add(mutedStyle.Render("Merge scheduled for the approved commit; the result appears here."))
		}
	case orchestrator.ReviewRejected:
		add(lipgloss.NewStyle().Foreground(statusFailed).Bold(true).Render("REVIEW REJECTED") + mutedStyle.Render(reviewed))
		line := "f adds follow-up and resumes the conversation in a new attempt."
		if t.ExecutionWorkspace != "" {
			line = "Worktree retained; " + line
		}
		add(mutedStyle.Render(line))
	}
	if t.ReviewNote != "" {
		add(mutedStyle.Render("Review note: " + terminalText(t.ReviewNote)))
	}
}

func (m model) taskHistory(t orchestrator.Task, width int, add func(...string)) {
	add("", accentStyle.Render("HISTORY"))
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
		// One run reads as one line: its usage is shortened to fit, not wrapped.
		row := fmt.Sprintf("%s  %s  %s", a.StartedAt.Local().Format("Jan 02 15:04"), statusLabel(a.Status), terminalText(a.ID))
		add(row + attemptUsageSuffix(a.Usage, width-lipgloss.Width(row)))
	}
	for _, e := range t.Events {
		add(mutedStyle.Render(e.Time.Local().Format("15:04:05")) + "  " + terminalText(e.Kind) + "  " + terminalText(e.Message))
	}
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

func (m model) detailTabRow(t orchestrator.Task, width int) string {
	row := ""
	for i, name := range detailTabNames {
		if i > 0 {
			row += "  "
		}
		row += selectionButton(name, detailTab(i) == m.tab)
	}
	row += faintStyle.Render("  ·  ") + mutedStyle.Render(terminalText(executorLabel(t)))
	return fit(row, width, 1)
}

// detailWindow is the [start, end) slice of n content lines shown in available
// rows, scrolled up from the bottom by historyOffset.
func (m model) detailWindow(n, available int) (start, end int) {
	end = n - min(max(0, m.historyOffset), max(0, n-available))
	return max(0, end-available), end
}

func (m model) detailPanel(width, height int) string {
	l := m.layout()
	inner := l.panelInner(width)
	t, ok := m.selectedTask()
	if !ok {
		return surfacePanel(m.landingContent(inner), width, height)
	}
	titleLine := titleStyle.Render(singleLine(t.Title)) + "  " + m.taskLabel(t)
	if action := m.currentAction(t); action != "" {
		titleLine = accentStyle.Render(action)
	}
	if position, total, ok := m.selectedPosition(); ok && !l.sidebar && inner >= 8 {
		titleLine = mutedStyle.Render(fmt.Sprintf("TASK %d/%d  ", position, total)) + titleLine
	}
	lines := []string{m.detailTabRow(t, inner), fit(titleLine, inner, 1)}
	content := m.detailContent(inner)
	available := max(0, l.detailRows(height))
	start, end := m.detailWindow(len(content), available)
	if available > 0 {
		lines = append(lines, m.highlightSelection(content, start, end, inner)...)
	}
	position := fmt.Sprintf("%s  %d–%d / %d", detailTabNames[m.tab], min(start+1, end), end, len(content))
	lines = append(lines, mutedStyle.Render(position))
	return surfacePanel(strings.Join(lines, "\n"), width, height)
}

// landingContent is the empty board: identity, examples and the first keys.
// Like Crush's landing page it sits at the left margin and reads top to bottom.
func (m model) landingContent(width int) string {
	ruleWidth := max(1, min(40, width))
	lines := []string{
		wordmark(),
		mutedStyle.Render("Local agents with recorded mandates and attempts"),
	}
	if models := m.modelSummary(width); models != "" {
		lines = append(lines, "", models)
	}
	lines = append(lines,
		"",
		faintStyle.Render(strings.Repeat("─", ruleWidth)),
		sectionStyle.Render("GET STARTED"),
		mutedStyle.Render("Describe the work in the request box."),
		mutedStyle.Render("Name an executor with @name."),
		mutedStyle.Render("Choose a model with /model."),
	)
	return strings.Join(lines, "\n")
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

// boardTaskHints advertises only operations the existing command guards and
// service methods accept for one task state.
func boardTaskHints(task orchestrator.Task) []string {
	var keys []string
	if task.AwaitingReview() {
		keys = append(keys, "v")
	}
	if task.ReviewStatus == orchestrator.ReviewRejected {
		keys = append(keys, "f")
	}
	switch {
	case task.Status == orchestrator.Running:
		keys = append(keys, "c", "tab")
	case task.Status == orchestrator.Queued && !task.ScheduleRequested:
		keys = append(keys, "o", "r")
	case task.Status == orchestrator.Queued:
		keys = append(keys, "c")
	}
	if retryable(task) {
		keys = append(keys, "r")
	}
	hints := make([]string, len(keys))
	for i, key := range keys {
		hints[i] = keyHint(key)
	}
	return hints
}
