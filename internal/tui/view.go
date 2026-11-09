package tui

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"orchestrator/internal/orchestrator"
)

// terminalText makes untrusted task data safe to lay out in a terminal. Tabs
// expand to spaces and cursor-control sequences cannot escape the panel.
func terminalText(s string) string {
	s = ansi.Strip(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var b strings.Builder
	col := 0
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteRune(r)
			col = 0
		case r == '\r':
			b.WriteRune('\n')
			col = 0
		case r == '\t':
			n := 4 - col%4
			b.WriteString(strings.Repeat(" ", n))
			col += n
		case unicode.IsControl(r):
		default:
			b.WriteRune(r)
			col += lipgloss.Width(string(r))
		}
	}
	return b.String()
}

// taskState gives each task one status word, glyph and semantic colour. The
// task sidebar renders only the glyph, while other surfaces pair it with the
// label so status stays readable without relying on colour alone.
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

// taskStatusIcon is the compact task-list status: a coloured glyph, with no
// status text. Distinct glyphs per state keep meaning visible without colour.
func taskStatusIcon(task orchestrator.Task) string {
	glyph, _, color := taskState(task)
	return lipgloss.NewStyle().Foreground(color).Bold(true).Render(glyph)
}

// taskMetaLine holds the secondary task details. Status is not repeated here:
// taskTitleRow starts every row with it, so narrow sidebars keep it visible.
// Child rows omit the redundant "subagent" suffix because the title marker
// and inherited-workspace badge already communicate ownership.
func taskMetaLine(task orchestrator.Task, width int) string {
	return fit("  "+mutedStyle.Render(taskRowExecutorLabel(task))+"  "+workspaceBadgeFor(task).render(), width, 1)
}

func taskRowExecutorLabel(task orchestrator.Task) string {
	if task.ParentID != "" {
		return task.Agent
	}
	return executorLabel(task)
}

// taskListRow pairs a task with its snapshot index and sidebar ownership
// marker. Grouping and indentation are display concerns; the service keeps its
// own ordering.
type taskListRow struct {
	index  int
	task   orchestrator.Task
	marker string
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
			index:  index,
			task:   task,
			marker: taskOwnershipMarker(task, len(children[task.ID]) > 0),
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

func taskOwnershipMarker(task orchestrator.Task, ownsChildren bool) string {
	marker := ""
	if task.ParentID != "" {
		marker = strings.Repeat("↳", min(max(1, task.Depth), 3))
	}
	if ownsChildren {
		marker += "▾"
	}
	if marker != "" {
		marker += " "
	}
	return marker
}

// taskTitleRow puts the coloured status glyph before the title. Selection uses
// the same rail and body styles as other selectable rows while keeping the
// status colour distinct.
func taskTitleRow(task orchestrator.Task, width int, selected bool) string {
	return taskTitleRowWithMarker(task, taskOwnershipMarker(task, false), width, selected)
}

func taskTitleRowWithMarker(task orchestrator.Task, marker string, width int, selected bool) string {
	text := marker + strings.ReplaceAll(terminalText(task.Title), "\n", " ")
	icon := taskStatusIcon(task)
	if !selected {
		return fit("  "+icon+" "+titleStyle.Render(text), width, 1)
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
	body := fit(icon+" "+text, width-2, 1)
	return selectionRailStyle.Render("▌ ") + selectionBodyStyle.Width(width-2).Render(body)
}

// toolStatusStyle gives tool activity a consistent visual language in the
// output transcript. Running rows use a placeholder that the renderer replaces
// with the current Bubble Tea spinner frame.
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
		return lipgloss.NewStyle().Foreground(cyan), m.runningGlyph()
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

func (m model) View() string {
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	if w < 30 || h < 10 {
		return fit("ORCHESTRATOR\nResize terminal to at least 30 × 10\nq quit", w, h)
	}
	running, queued := 0, 0
	for _, t := range m.tasks {
		if t.Status == orchestrator.Running {
			running++
		}
		if t.Status == orchestrator.Queued {
			queued++
		}
	}
	header := gradientText("◈ ORCHESTRATOR")
	summary := mutedStyle.Render(fmt.Sprintf("  %d tasks", len(m.tasks))) +
		lipgloss.NewStyle().Foreground(statusRunning).Render(fmt.Sprintf("  ● %d active", running)) +
		lipgloss.NewStyle().Foreground(statusWaiting).Render(fmt.Sprintf("  ◷ %d queued", queued))
	mode, modeColor := " TASKS ", accent
	if m.detailFocus || m.fullscreen {
		mode, modeColor = " DETAILS ", cyan
	}
	if m.input.kind != "" {
		mode, modeColor = " INSERT ", amber
	}
	if m.router {
		mode, modeColor = " ROUTER ", accent
	}
	mode = modeBadge(mode, modeColor)
	gap := max(1, w-lipgloss.Width(header+summary)-lipgloss.Width(mode))
	top := fit(header+summary+strings.Repeat(" ", gap)+mode, w, 1)
	footer := m.footer(w)
	bodyHeight := h - 1 - (strings.Count(footer, "\n") + 1)
	var body string
	if m.router {
		body = m.routerPanel(w, bodyHeight)
	} else if w >= 76 && !m.fullscreen {
		left := min(34, max(24, w/4))
		right := w - left - 1
		body = lipgloss.JoinHorizontal(lipgloss.Top, m.taskPanel(left, bodyHeight), " ", m.detailPanel(right, bodyHeight))
	} else {
		body = m.detailPanel(w, bodyHeight)
	}

	screen := fit(top+"\n"+body+"\n"+footer, w, h)
	if m.help {
		return m.helpView(screen, w, h)
	}
	return screen
}

func panel(content string, width, height int) string {
	if width < 4 || height < 2 {
		return fit(content, width, height)
	}
	return panelStyle.Width(width - 2).Height(height - 2).MaxWidth(width).MaxHeight(height).Render(fit(content, width-4, height-2))
}

func focusedPanel(content string, width, height int, focused bool) string {
	rendered := panel(content, width, height)
	if !focused || width < 4 || height < 2 {
		return rendered
	}
	return panelStyle.BorderForeground(accent).Width(width - 2).Height(height - 2).MaxWidth(width).MaxHeight(height).Render(fit(content, width-4, height-2))
}

func modeBadge(label string, color lipgloss.Color) string {
	return lipgloss.NewStyle().
		Foreground(color).
		Bold(true).
		Render(label)
}

func selectionRow(text string, width int) string {
	if width < 1 {
		return ""
	}
	if width == 1 {
		return selectionRailStyle.Render("▌")
	}
	if width == 2 {
		return selectionRailStyle.Render("▌ ")
	}
	body := selectionBodyStyle.Width(width - 2).Render(fit(text, width-2, 1))
	return selectionRailStyle.Render("▌ ") + body
}

func (m model) taskPanel(width, height int) string {
	inner := width - 4
	var lines []string
	lines = append(lines, accentStyle.Render("TASKS")+mutedStyle.Render(fmt.Sprintf("  / %02d", len(m.tasks))))
	lines = append(lines, "")
	if len(m.tasks) == 0 {
		lines = append(lines, mutedStyle.Render("No tasks yet."), mutedStyle.Render("n creates one."))
	} else {
		slots := (height - 4) / 2
		if slots < 1 {
			slots = 1
		}
		rows := taskListRows(m.tasks)
		selected := 0
		for i, row := range rows {
			if row.index == m.selected {
				selected = i
				break
			}
		}
		start := 0
		if selected >= slots {
			start = selected - slots + 1
		}
		for i := start; i < len(rows) && i < start+slots; i++ {
			row := rows[i]
			lines = append(lines,
				taskTitleRowWithMarker(row.task, row.marker, inner, row.index == m.selected),
				taskMetaLine(row.task, inner))
		}
	}
	return focusedPanel(strings.Join(lines, "\n"), width, height, !m.detailFocus && !m.fullscreen)
}

// detailGeometry matches the responsive layout used by View.
func (m model) detailGeometry() (int, int) {
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	if w < 30 || h < 10 {
		return 0, 0
	}
	body := h - 1 - (strings.Count(m.footer(w), "\n") + 1)
	if body < 3 {
		return 0, 0
	}
	if w >= 76 && !m.fullscreen {
		left := min(34, max(24, w/4))
		return w - left - 1, body
	}
	return w, body
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
	add(accentStyle.Render("MANDATE"))
	add(terminalText(t.Prompt))
	add(mutedStyle.Render("Agent: " + terminalText(t.Agent) + " · A to change"))
	if n := len(t.Attempts); n > 0 && t.Attempts[n-1].RequestedModel != "" {
		add(mutedStyle.Render("Last attempt model: " + terminalText(t.Attempts[n-1].RequestedModel)))
	}
	if t.ProfileName != "" {
		label := t.ProfileName
		if t.ProfileSpec != nil {
			label = profileLabel(*t.ProfileSpec)
		}
		add(mutedStyle.Render("Profile: " + terminalText(label) + " · P to change"))
	}
	if t.ParentID != "" {
		add(mutedStyle.Render(fmt.Sprintf("Subagent of %s · depth %d", terminalText(t.ParentID), t.Depth)))
	}
	add(mutedStyle.Render("Source: " + terminalText(t.Workspace)))
	worktreeGlyph := lipgloss.NewStyle().Foreground(cyan).Render(glyphWorktree)
	if t.Isolated {
		if t.ExecutionWorkspace == "" {
			add(accentStyle.Render(worktreeGlyph + " Workspace: isolated worktree requested (w to change)"))
			add(mutedStyle.Render("Committed HEAD only; uncommitted files are excluded."))
		} else {
			add(accentStyle.Render(worktreeGlyph + " Execution: " + terminalText(t.ExecutionWorkspace)))
			add(mutedStyle.Render("Isolated worktree · commit " + terminalText(t.IsolationCommit)))
		}
	} else if t.ParentID != "" {
		add(mutedStyle.Render(glyphInherited + " Execution: inherited from parent task"))
	} else {
		add(mutedStyle.Render(glyphShared + " Execution: shared workspace (w to isolate before start)"))
	}
	badge := workspaceBadgeFor(t)
	switch t.MergeStatus {
	case orchestrator.MergeMerged:
		add(lipgloss.NewStyle().Foreground(badge.color).Render(badge.glyph+" Merge: merged") + mutedStyle.Render(" · commit "+terminalText(t.MergeCommit)))
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
		add(accentStyle.Render("REVIEW PENDING") + mutedStyle.Render("  commit "+terminalText(t.ReviewCommit)+"  ·  v approve / V reject"))
	case orchestrator.ReviewApproved, orchestrator.ReviewRejected:
		label, color := "REVIEW APPROVED", statusCompleted
		if t.ReviewStatus == orchestrator.ReviewRejected {
			label, color = "REVIEW REJECTED", statusFailed
		}
		suffix := "  " + terminalText(t.ReviewedBy)
		if !t.ReviewedAt.IsZero() {
			suffix += "  " + t.ReviewedAt.Local().Format("Jan 02 15:04")
		}
		add(lipgloss.NewStyle().Foreground(color).Bold(true).Render(label) + mutedStyle.Render(suffix))
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
				add(fmt.Sprintf("%s  %s  %s", child.CreatedAt.Local().Format("Jan 02 15:04"), statusLabel(child.Status), terminalText(child.Title)))
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
			lines = append(lines, renderedOutputWithThinking(t.Output, width, m.runningGlyph(), m.thinking)...)
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
	inner := width - 4
	if len(m.tasks) == 0 {
		return panel(titleStyle.Render("TASK DETAILS")+"\n\n"+mutedStyle.Render("Select a task to view details."), width, height)
	}
	t := m.tasks[m.selected]
	titleLine := titleStyle.Render(strings.ReplaceAll(terminalText(t.Title), "\n", " ")) + "  " + taskLabel(t)
	lines := []string{m.detailTabRow(t, inner), fit(titleLine, inner, 1)}
	if action := m.currentAction(t); action != "" {
		lines[1] = lipgloss.NewStyle().Foreground(cyan).Render(action)
	}
	content := m.detailContent(inner)
	available := max(0, height-2-len(lines)-1)
	offset := min(max(0, m.historyOffset), max(0, len(content)-available))
	end := len(content) - offset
	start := max(0, end-available)
	if available > 0 {
		lines = append(lines, content[start:end]...)
	}
	label := "OUTPUT"
	if m.activity {
		label = "ACTIVITY"
	} else if m.history {
		label = "HISTORY"
	}
	focus := "l to focus · j/k scroll"
	if m.detailFocus || m.fullscreen {
		focus = "j/k scroll · h tasks"
	}
	if m.activity && !m.activityExpanded {
		focus = "j/k select · enter expand"
	}
	position := fmt.Sprintf("%s  %d–%d / %d  ·  %s", label, min(start+1, end), end, len(content), focus)
	lines = append(lines, mutedStyle.Render(position))
	return focusedPanel(strings.Join(lines, "\n"), width, height, m.detailFocus || m.fullscreen)
}

func (m model) footer(w int) string {
	footerText := "n new · / route · r run · ? help · q quit"
	switch {
	case m.router:
		footerText = "enter route · tab attention · esc tasks · ctrl+c quit"
	case m.activity:
		footerText = "t output · v filter · esc collapse · ? help · q quit"
	case m.detailFocus || m.fullscreen:
		footerText = "j/k scroll · T thinking · h tasks · ? help · q quit"
	case w < 76:
		footerText = "j/k tasks · l details · / route · ? help · q quit"
	}
	var hints []string
	for _, line := range strings.Split(footerText, "\n") {
		var parts []string
		for _, hint := range strings.Split(line, " · ") {
			key, desc, _ := strings.Cut(hint, " ")
			parts = append(parts, keyStyle.Render(key)+mutedStyle.Render(" "+desc))
		}
		hints = append(hints, strings.Join(parts, faintStyle.Render("  ·  ")))
	}
	footer := strings.Join(hints, "\n")
	if m.notice != "" {
		footer = accentStyle.Render("› "+strings.ReplaceAll(terminalText(m.notice), "\n", " ")) + "\n" + footer
	}
	if m.cleanup.kind == deleteTask {
		footer = accentStyle.Render("DELETE TASK + HISTORY  › "+strings.ReplaceAll(terminalText(m.cleanup.title), "\n", " ")) + "\n" + mutedStyle.Render("y delete  ·  n/esc cancel") + "\n" + mutedStyle.Render("Removes its worktree; source workspace is untouched.")
	}
	if m.cleanup.kind == deleteCompleted {
		footer = accentStyle.Render(fmt.Sprintf("DELETE %d COMPLETED TASK(S) + HISTORY", len(m.cleanup.ids))) + "\n" + mutedStyle.Render("y delete  ·  n/esc cancel") + "\n" + mutedStyle.Render("Tasks with unfinished children are kept; worktrees are removed.")
	}
	if m.cleanup.kind == discardTask {
		footer = accentStyle.Render("DISCARD RETAINED WORKTREE  › "+strings.ReplaceAll(terminalText(m.cleanup.title), "\n", " ")) + "\n" + mutedStyle.Render("y discard  ·  n/esc cancel") + "\n" + mutedStyle.Render("Refuses uncommitted or unmerged work; task history is kept.")
	}
	if m.cleanup.kind == discardMerged {
		footer = accentStyle.Render(fmt.Sprintf("DISCARD %d MERGED WORKTREE(S)", len(m.cleanup.ids))) + "\n" + mutedStyle.Render("y discard  ·  n/esc cancel") + "\n" + mutedStyle.Render("Skips uncommitted or unmerged work; task history is kept.")
	}
	if m.input.kind != "" {
		label := string(m.input.kind)
		draft := strings.ReplaceAll(terminalText(m.input.text), "\n", " ")
		room := w - lipgloss.Width(label+"  › ") - 1
		if n := lipgloss.Width(draft) - room; n > 0 {
			draft = ansi.TruncateLeft(draft, n+1, "…")
		}
		footer = accentStyle.Render(label+"  › ") + draft + "▏\n" + mutedStyle.Render("enter submit  ·  esc cancel")
	}
	if m.agentPicker.target != "" {
		footer = mutedStyle.Render("↑↓ choose agent · enter select · esc cancel")
	}
	if m.profilePicker.target != "" {
		footer = mutedStyle.Render("↑↓ choose profile · enter select · esc cancel")
	}
	return ansi.Hardwrap(footer, w, true)
}

// executorLabel shows the executor and, when chosen, its profile.
func executorLabel(t orchestrator.Task) string {
	if t.ParentID != "" {
		return t.Agent + " · subagent"
	}
	if t.ProfileName == "" || (t.ProfileSpec != nil && t.ProfileSpec.UsesExecutorDefaults()) {
		return t.Agent
	}
	return t.Agent + " · profile " + t.ProfileName
}

func profileLabel(p orchestrator.Profile) string {
	if p.UsesExecutorDefaults() {
		return "Executor defaults"
	}
	return p.Name
}

func (m model) profilePanel(width, height int) string {
	lines := []string{titleStyle.Render("SELECT PROFILE"), mutedStyle.Render("Queued task only"), ""}
	slots := max(1, (height-7)/2)
	start := max(0, m.profilePicker.index-slots+1)
	for i := start; i < len(m.profilePicker.choices) && i < start+slots; i++ {
		profile := m.profilePicker.choices[i]
		prefix := "  "
		style := titleStyle
		if i == m.profilePicker.index {
			prefix = ""
			style = accentStyle
		}
		row := style.Render(prefix + terminalText(profileLabel(profile)))
		if i == m.profilePicker.index {
			row = selectionRow(terminalText(profileLabel(profile)), width-4)
		}
		details := profile.Description
		if profile.Model != "" {
			details += " · model " + profile.Model
		}
		if profile.NoIntegrate {
			details += " · no auto-integration"
		}
		if profile.NoSubagents {
			details += " · no delegation"
		}
		if len(profile.Tools) > 0 {
			details += " · tools " + strings.Join(profile.Tools, ",")
		}
		lines = append(lines, row, mutedStyle.Render("  "+terminalText(strings.TrimPrefix(details, " · "))))
	}
	if len(m.profilePicker.choices) == 0 {
		lines = append(lines, mutedStyle.Render("No registered profiles."))
	}
	return panel(strings.Join(lines, "\n"), width, height)
}

func (m model) agentPanel(width, height int) string {
	lines := []string{titleStyle.Render("SELECT AGENT"), mutedStyle.Render("Queued task only"), ""}
	slots := max(1, (height-7)/2)
	start := max(0, m.agentPicker.index-slots+1)
	for i := start; i < len(m.agentPicker.choices) && i < start+slots; i++ {
		agent := m.agentPicker.choices[i]
		prefix := "  "
		style := titleStyle
		if i == m.agentPicker.index {
			prefix = ""
			style = accentStyle
		}
		capability := "One-shot · no live context"
		if agent.Capabilities.LiveContext {
			capability = "Live follow-up context"
		}
		if agent.Capabilities.ToolActivity {
			capability += " · tool activity"
		}
		if agent.Capabilities.StructuredReports {
			capability += " · structured reports"
		}
		label := terminalText(agent.Name)
		if agent.Description != "" {
			label += " · " + terminalText(agent.Description)
		}
		row := style.Render(prefix + label)
		if i == m.agentPicker.index {
			row = selectionRow(label, width-4)
		}
		lines = append(lines, row, mutedStyle.Render("  "+capability))
	}
	if len(m.agentPicker.choices) == 0 {
		lines = append(lines, mutedStyle.Render("No registered agents."))
	}
	return panel(strings.Join(lines, "\n"), width, height)
}
