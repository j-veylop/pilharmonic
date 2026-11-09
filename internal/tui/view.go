package tui

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"orchestrator/internal/orchestrator"
)

var (
	ink         = lipgloss.Color("#E7EAF3")
	muted       = lipgloss.Color("#8B96AC")
	accent      = lipgloss.Color("#A99BFF")
	border      = lipgloss.Color("#303B50")
	surface     = lipgloss.Color("#222B40")
	cyan        = lipgloss.Color("#79D8C3")
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(ink)
	mutedStyle  = lipgloss.NewStyle().Foreground(muted)
	accentStyle = lipgloss.NewStyle().Foreground(accent).Bold(true)
	panelStyle  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).Padding(0, 1)
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

func taskLabel(task orchestrator.Task) string {
	if task.Status == orchestrator.Queued && task.ScheduleRequested {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#F2C879")).Bold(true).Render("● WAITING")
	}
	if stage := task.Stage(); stage == "blocked" || stage == "awaiting review" || stage == "waiting" || stage == "merge failed" {
		return accentStyle.Render("● " + strings.ToUpper(stage))
	}
	return statusLabel(task.Status)
}

func statusLabel(status orchestrator.Status) string {
	color := muted
	switch status {
	case orchestrator.Running:
		color = lipgloss.Color("#79D8C3")
	case orchestrator.Completed:
		color = lipgloss.Color("#98DAA6")
	case orchestrator.Failed, orchestrator.Interrupted:
		color = lipgloss.Color("#FF8F9B")
	case orchestrator.Queued:
		color = lipgloss.Color("#F2C879")
	}
	return lipgloss.NewStyle().Foreground(color).Bold(true).Render("● " + strings.ToUpper(string(status)))
}

// toolStatusStyle gives tool activity a consistent visual language in the
// output transcript and the activity view. Running rows use a placeholder that
// the renderer replaces with the current Bubble Tea spinner frame.
func toolStatusStyle(status string) (lipgloss.Style, string) {
	switch status {
	case "running":
		return lipgloss.NewStyle().Foreground(cyan), runningGlyphPlaceholder
	case "finished":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#98DAA6")), "✓"
	case "failed":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#FF8F9B")), "✗"
	case "interrupted":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#FF8F9B")), "⊘"
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
	header := lipgloss.NewStyle().Background(accent).Foreground(lipgloss.Color("#151827")).Bold(true).Padding(0, 1).Render("◈ ORCHESTRATOR")
	summary := mutedStyle.Render(fmt.Sprintf("  %d tasks", len(m.tasks))) + lipgloss.NewStyle().Foreground(cyan).Render(fmt.Sprintf("  ● %d active", running)) + mutedStyle.Render(fmt.Sprintf("  ◷ %d queued", queued))
	mode := " TASKS "
	if m.detailFocus || m.fullscreen {
		mode = " DETAILS "
	}
	if m.input {
		mode = " INSERT "
	}
	if m.router {
		mode = " ROUTER "
	}
	mode = lipgloss.NewStyle().Foreground(accent).Background(surface).Bold(true).Render(mode)
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
func selectionRow(text string, width int) string {
	return lipgloss.NewStyle().Foreground(ink).Background(surface).Bold(true).Width(max(0, width)).Render(fit(text, width, 1))
}

func (m model) taskPanel(width, height int) string {
	inner := width - 4
	var lines []string
	lines = append(lines, accentStyle.Render("TASKS")+mutedStyle.Render(fmt.Sprintf("  / %02d", len(m.tasks))))
	lines = append(lines, "")
	if len(m.tasks) == 0 {
		lines = append(lines, mutedStyle.Render("Nothing here yet."), mutedStyle.Render("Press n to create a task."))
	} else {
		slots := (height - 4) / 2
		if slots < 1 {
			slots = 1
		}
		start := 0
		if m.selected >= slots {
			start = m.selected - slots + 1
		}
		for i := start; i < len(m.tasks) && i < start+slots; i++ {
			t := m.tasks[i]
			prefix := "  "
			style := titleStyle
			if i == m.selected {
				prefix = "▸ "
				style = accentStyle
			}
			mark := ""
			if t.ParentID != "" {
				mark = "↳ "
			}
			row := fit(style.Render(prefix+mark+strings.ReplaceAll(terminalText(t.Title), "\n", " ")), inner, 1)
			if i == m.selected {
				row = selectionRow(prefix+mark+strings.ReplaceAll(terminalText(t.Title), "\n", " "), inner)
			}
			lines = append(lines, row, fit("  "+taskLabel(t)+mutedStyle.Render("  "+t.Agent), inner, 1))
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
	add(mutedStyle.Render("Agent: " + terminalText(t.Agent) + " (A to change before start)"))
	if t.ParentID != "" {
		add(mutedStyle.Render(fmt.Sprintf("Subagent of %s · depth %d", terminalText(t.ParentID), t.Depth)))
	}
	add(mutedStyle.Render("Source: " + terminalText(t.Workspace)))
	if t.Isolated {
		if t.ExecutionWorkspace == "" {
			add(accentStyle.Render("Workspace: isolated Git worktree requested (w to change)"))
			add(mutedStyle.Render("Starts from committed HEAD; uncommitted files are not copied."))
		} else {
			add(accentStyle.Render("Execution: " + terminalText(t.ExecutionWorkspace)))
			add(mutedStyle.Render("Isolated worktree · commit " + terminalText(t.IsolationCommit)))
		}
	} else if t.ParentID != "" {
		add(mutedStyle.Render("Execution: inherited from parent task"))
	} else {
		add(mutedStyle.Render("Execution: shared source workspace (w to isolate before start)"))
	}
	switch t.MergeStatus {
	case orchestrator.MergeMerged:
		add(accentStyle.Render("Merge: merged into source") + mutedStyle.Render(" · "+terminalText(t.MergeCommit)))
	case orchestrator.MergeUpToDate:
		add(mutedStyle.Render("Merge: no isolated changes to merge"))
	case orchestrator.MergeFailed:
		add(lipgloss.NewStyle().Foreground(lipgloss.Color("#FF8F9B")).Render("MERGE FAILED  " + terminalText(t.MergeError)))
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
		add(mutedStyle.Render("Thread context subscription paused (s to resume)"))
	}
	if t.Error != "" {
		add("")
		add(lipgloss.NewStyle().Foreground(lipgloss.Color("#FF8F9B")).Render("ERROR  " + terminalText(t.Error)))
	}
	if m.history {
		add("")
		add(accentStyle.Render("HISTORY"))
		if len(t.Context) > 0 {
			add(accentStyle.Render("CONTEXT") + mutedStyle.Render("  current summary per input"))
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
			lines = append(lines, renderedOutputWithGlyph(t.Output, width, m.runningGlyph())...)
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

func (m model) detailPanel(width, height int) string {
	if m.agentTask != "" {
		return m.agentPanel(width, height)
	}
	inner := width - 4
	if len(m.tasks) == 0 {
		return panel(titleStyle.Render("TASK DETAILS")+"\n\n"+mutedStyle.Render("Select a task to inspect its progress."), width, height)
	}
	t := m.tasks[m.selected]
	outputTab, historyTab := accentStyle.Render(" OUTPUT "), mutedStyle.Render(" HISTORY ")
	if m.history {
		outputTab, historyTab = mutedStyle.Render(" OUTPUT "), accentStyle.Render(" HISTORY ")
	}
	activityTab := mutedStyle.Render(" ACTIVITY [t] ")
	if m.activity {
		outputTab = mutedStyle.Render(" OUTPUT ")
		historyTab = mutedStyle.Render(" HISTORY ")
		activityTab = accentStyle.Render(" ACTIVITY [t] ")
	}
	lines := []string{outputTab + mutedStyle.Render("│") + historyTab + mutedStyle.Render("│") + activityTab + mutedStyle.Render("  ·  "+t.Agent), titleStyle.Render(strings.ReplaceAll(terminalText(t.Title), "\n", " ")) + "  " + taskLabel(t)}
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
	footerText := "n new · / route · r run · h/l focus · z expand · tab history · t activity · ? help · q quit"
	switch {
	case m.router:
		footerText = routerFooterHint()
	case m.activity:
		footerText = "t output · j/k select · enter expand · esc collapse · wheel/^u/^d scroll · h tasks · ? help"
	case m.detailFocus || m.fullscreen:
		footerText = "j/k scroll · ^u/^d page · gg/G · h tasks · ? help · q quit"
	case w < 76:
		footerText = "j/k tasks · l details · / route · z expand · ? help · q quit"
	}
	var hints []string
	for _, line := range strings.Split(footerText, "\n") {
		var parts []string
		for _, hint := range strings.Split(line, " · ") {
			key, desc, _ := strings.Cut(hint, " ")
			parts = append(parts, lipgloss.NewStyle().Foreground(accent).Bold(true).Render(key)+mutedStyle.Render(" "+desc))
		}
		hints = append(hints, strings.Join(parts, mutedStyle.Render("  ·  ")))
	}
	footer := strings.Join(hints, "\n")
	if m.notice != "" {
		footer = accentStyle.Render("› "+strings.ReplaceAll(terminalText(m.notice), "\n", " ")) + "\n" + footer
	}
	if m.deleteTaskID != "" {
		footer = accentStyle.Render("DELETE TASK + HISTORY  › "+strings.ReplaceAll(terminalText(m.deleteTaskTitle), "\n", " ")) + "\n" + mutedStyle.Render("y delete  ·  n/esc cancel") + "\n" + mutedStyle.Render("Workspace files are kept.")
	}
	if m.input {
		label := "NEW TASK"
		if m.reportTask != "" {
			label = "REPORT (kind message)"
		}
		if m.contextTask != "" {
			label = "THREAD CONTEXT"
		} else if m.subagentTask != "" {
			label = "SUBAGENT TASK"
		} else if m.relatedTask != "" {
			label = "RELATED TASK"
		}
		draft := strings.ReplaceAll(terminalText(m.draft), "\n", " ")
		room := w - lipgloss.Width(label+"  › ") - 1
		if n := lipgloss.Width(draft) - room; n > 0 {
			draft = ansi.TruncateLeft(draft, n+1, "…")
		}
		footer = accentStyle.Render(label+"  › ") + draft + "▏\n" + mutedStyle.Render("enter submit  ·  esc cancel")
	}
	if m.agentTask != "" {
		footer = mutedStyle.Render("↑↓ choose agent · enter select · esc cancel")
	}
	return ansi.Hardwrap(footer, w, true)
}

func (m model) agentPanel(width, height int) string {
	lines := []string{titleStyle.Render("SELECT AGENT"), mutedStyle.Render("For this queued task only"), ""}
	slots := max(1, (height-7)/2)
	start := max(0, m.agentIndex-slots+1)
	for i := start; i < len(m.agentChoices) && i < start+slots; i++ {
		agent := m.agentChoices[i]
		prefix := "  "
		style := titleStyle
		if i == m.agentIndex {
			prefix = "▸ "
			style = accentStyle
		}
		capability := "One-shot execution · no live context"
		if agent.LiveContext {
			capability = "Execution + live follow-up context"
		}
		if agent.Capabilities.ToolActivity {
			capability += " · tool activity"
		}
		if agent.Capabilities.StructuredReports {
			capability += " · structured reports"
		}
		row := style.Render(prefix + terminalText(agent.Name))
		if i == m.agentIndex {
			row = selectionRow(prefix+terminalText(agent.Name), width-4)
		}
		lines = append(lines, row, mutedStyle.Render("  "+capability))
	}
	if len(m.agentChoices) == 0 {
		lines = append(lines, mutedStyle.Render("No registered agents."))
	}
	return panel(strings.Join(lines, "\n"), width, height)
}
