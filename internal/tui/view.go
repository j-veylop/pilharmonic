package tui

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
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
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), r == '\u2028', r == '\u2029':
		default:
			b.WriteRune(r)
			col += lipgloss.Width(string(r))
		}
	}
	return b.String()
}

// singleLine flattens untrusted text for a one-row surface: terminal control
// characters are stripped and embedded newlines become spaces.
func singleLine(s string) string {
	return strings.ReplaceAll(terminalText(s), "\n", " ")
}

// layout is the single source of responsive geometry: the body height between
// header and footer, whether the sidebar shows, and each panel's width.
type layout struct {
	width, height int
	bodyHeight    int
	details       bool
	sidebar       bool
	taskWidth     int
	detailWidth   int
	composerDraft int // draft rows inside the docked input box
	composerRows  int // total rows of the docked input block, hint rows included
}

// size is the terminal size, defaulting to 80×24 before the first resize.
func (m model) size() (w, h int) {
	w, h = m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	return w, h
}

func (m model) layout() layout {
	w, h := m.size()
	l := layout{width: w, height: h}
	if w < 30 || h < 10 {
		return l
	}
	footerRows := strings.Count(m.footer(w), "\n") + 1
	if m.composerOpen() {
		extra := m.composerExtraRows()
		l.composerDraft = m.composerDraftRows(footerRows + extra)
		l.composerRows = l.composerDraft + 2 + extra
	}
	l.bodyHeight = h - 1 - footerRows - l.composerRows
	l.details = m.detailFocus || m.fullscreen
	if l.details {
		l.sidebar = w >= 76 && !m.fullscreen && !m.sidebarHidden && m.selectedHasSubtasks()
	}
	if l.sidebar {
		l.taskWidth = min(34, max(24, w/4))
		l.detailWidth = w - l.taskWidth - 1
	} else {
		l.detailWidth = w
	}
	return l
}

// panelInner is the content width inside a bordered panel.
func (layout) panelInner(width int) int { return width - 4 }

// detailRows is how many content rows a detail panel of height shows below its
// tab row, title and position line.
func (layout) detailRows(height int) int { return height - 5 }

func (m model) View() string {
	l := m.layout()
	w, h := l.width, l.height
	if w < 30 || h < 10 {
		prompt := "q quit"
		if m.quitConfirm {
			// The frame cannot hold the footer prompt, but the answer keys still work,
			// so the resize prompt explains them.
			prompt = "y " + quitButtonLabels[quitButtonQuit] + " · n " + quitButtonLabels[quitButtonStay]
		}
		return fit("PILHARMONIC\nResize terminal to at least 30 × 10\n"+prompt, w, h)
	}
	var body string
	if m.reviewDiff.target != "" {
		body = m.reviewDiffView(w, l.bodyHeight)
	} else {
		switch {
		case !l.details:
			body = m.taskPanel(w, l.bodyHeight)
		case l.sidebar:
			root, _ := m.selectedTask()
			root = m.rootTask(root)
			body = lipgloss.JoinHorizontal(lipgloss.Top,
				m.subtaskPanel(l.taskWidth, l.bodyHeight, l.panelInner(l.taskWidth), root, subtaskRows(m.tasks, root.ID)),
				" ", m.detailPanel(l.detailWidth, l.bodyHeight))
		default:
			body = m.detailPanel(w, l.bodyHeight)
		}
	}
	rows := []string{m.headerRow(w)}
	if body != "" {
		rows = append(rows, body)
	}
	if l.composerRows > 0 {
		rows = append(rows, m.composerBlock(l.composerDraft)...)
	}
	rows = append(rows, m.footer(w))
	screen := fit(strings.Join(rows, "\n"), w, h)
	screen = m.toastView(screen, w, h)
	// Menus draw over the board and help over menus.
	if m.login.menu {
		screen = m.loginMenuView(screen, w, h)
	}
	if m.models.menu {
		screen = m.modelMenuView(screen, w, h)
	}
	if m.setup.target != "" {
		screen = m.setupView(screen, w, h)
	}
	if !m.quitConfirm {
		screen = m.actionPromptView(screen, w, h)
	}
	if m.help.open {
		screen = m.helpView(screen, w, h)
	}
	// The quit dialog is drawn last: it owns the keyboard and mouse, so it must
	// be the topmost surface.
	if m.quitConfirm {
		screen = m.quitView(screen, w, h)
	}
	return screen
}

func (m model) attentionCount() int { return len(m.attentionTasks()) }

// headerMode is the surface that owns the keyboard, shown as a status pill.
func (m model) headerMode() string {
	switch {
	case m.router.open:
		return "REQUEST"
	case m.input.kind != "":
		return "INSERT"
	case m.detailFocus || m.fullscreen:
		return "DETAILS"
	}
	return "TASKS"
}

// headerRow is the wordmark and, right-aligned, the task counters and session
// total, joined by the diagonal filler Crush uses. The session total is dropped
// before a counter loses its wording.
func (m model) headerRow(width int) string {
	header := wordmark()
	available := max(1, width-lipgloss.Width(header)-1)
	options := m.headerCountOptions()
	summary, usage := options[len(options)-1], ""
fit:
	for _, option := range options {
		for _, variant := range m.headerUsageVariants() {
			if lipgloss.Width(option)+lipgloss.Width(variant) <= available {
				summary, usage = option, variant
				break fit
			}
		}
	}
	if usage != "" {
		usage = mutedStyle.Render(usage)
	}
	right := summary + usage
	fill := width - lipgloss.Width(header) - lipgloss.Width(right) - 2
	if fill < 3 {
		return fit(header+right, width, 1)
	}
	return fit(header+" "+gradientFill(strings.Repeat("╱", fill), 0)+" "+right, width, 1)
}

// statusPills is the mode and model badge row above the key hints. Badges drop
// right to left so the mode is always visible. It carries no moving parts, so
// the row never changes width while tasks run.
func (m model) statusPills(width int) string {
	mode := m.headerMode()
	pills := []string{pillStyle.Render(mode)}
	if m.service != nil {
		selection := m.service.ModelSelection()
		for _, class := range []struct{ name, model string }{{"BIG", selection.Big}, {"SMALL", selection.Small}} {
			if class.model == "" {
				continue
			}
			pills = append(pills, modelPillStyle(class.name).Render(class.name+" "+shortModel(class.model)))
		}
	}
	row := strings.Join(pills, " ")
	for len(pills) > 1 && lipgloss.Width(row) > width {
		pills = pills[:len(pills)-1]
		row = strings.Join(pills, " ")
	}
	return fit(row, width, 1)
}

// shortModel keeps the model ID without its provider prefix, which the pill
// name already implies.
func shortModel(selector string) string {
	if _, model, ok := strings.Cut(selector, "/"); ok {
		return model
	}
	return selector
}

// headerUsageVariants lists the session total, richest first and ending with
// an empty variant. An untouched session has no segment at all.
func (m model) headerUsageVariants() []string {
	if m.usage.TotalTokens == 0 {
		return []string{""}
	}
	variants := usageVariants(m.usage.InputTokens, m.usage.OutputTokens, m.usage.CostUSD, m.usage.Priced)
	for i, variant := range variants {
		if variant != "" {
			variants[i] = "  Σ " + variant
		}
	}
	return variants
}

// headerCountOptions lists the counter formats, richest first. Running is the
// displayed state, so a parent waiting for subagents is not counted; queued is
// the process status. The last form keeps only the attention count.
func (m model) headerCountOptions() []string {
	running, queued := 0, 0
	for _, task := range m.tasks {
		if task.Status == orchestrator.Queued {
			queued++
		}
		if _, label, _ := m.taskState(task); label == "RUNNING" {
			running++
		}
	}
	attention := m.attentionCount()
	actionText := "need action"
	if attention == 1 {
		actionText = "needs action"
	}
	tasks := fmt.Sprintf("  %d tasks", len(m.tasks))
	format := func(runningText, queuedText, attentionText string) string {
		return mutedStyle.Render(tasks) +
			lipgloss.NewStyle().Foreground(statusRunning).Render(runningText) +
			lipgloss.NewStyle().Foreground(statusWaiting).Render(queuedText) +
			accentStyle.Render(attentionText)
	}
	return []string{
		format(fmt.Sprintf("  ● %d running", running), fmt.Sprintf("  ◷ %d queued", queued), fmt.Sprintf("  ◆ %d %s", attention, actionText)),
		format(fmt.Sprintf("  ● %d run", running), fmt.Sprintf("  ◷ %d wait", queued), fmt.Sprintf("  ◆ %d action", attention)),
		format(fmt.Sprintf("  ●%d", running), fmt.Sprintf("  ◷%d", queued), fmt.Sprintf("  ◆%d", attention)),
		format("", "", fmt.Sprintf("  ◆%d", attention)),
	}
}

// boardHints is the hint line for one task: its actions first, then the
// operator essentials. Only the task's own actions and the quit key are
// unconditional; every other hint is dropped before the line needs a second row.
func boardHints(w int, task orchestrator.Task, attention bool) string {
	var hints []string
	add := func(hint string, optional bool) {
		candidate := append(append([]string{}, hints...), hint)
		if optional && hintRows(strings.Join(candidate, " · "), w) > 1 {
			return
		}
		hints = candidate
	}
	for _, hint := range boardTaskHints(task) {
		add(hint, false)
	}
	if attention {
		add(keyHint("!"), true)
	}
	add("? help", true)
	add(keyHint("q"), false)
	add(keyHint("/"), true)
	add(keyHint("i"), true)
	if w < 76 {
		add("j/k tasks", true)
		add("l details", true)
	}
	return strings.Join(hints, " · ")
}

// boardHintRows is the height the hint area reserves at width: the tallest hint
// line any task on the board needs. Moving the selection or a task changing
// state therefore never moves the body above the footer.
func (m model) boardHintRows(w int) int {
	attention := m.attentionCount() > 0
	rows := hintRows(boardHints(w, orchestrator.Task{}, attention), w)
	for _, task := range m.tasks {
		rows = max(rows, hintRows(boardHints(w, task, attention), w))
	}
	return rows
}

// hintRows counts the rows text occupies once the separators are rendered and
// the footer is hard-wrapped at width. A hint is only worth adding when it costs
// no extra row.
func hintRows(text string, w int) int {
	return len(strings.Split(ansi.Hardwrap(strings.ReplaceAll(text, " · ", "  ·  "), w, true), "\n"))
}

// withHelpHint advertises the help overlay where there is room for it. F1 is
// the one help key that works inside a text field, so the entry surfaces
// advertise that key rather than ?.
func withHelpHint(text string, w int) string {
	withHelp := text + " · f1 help"
	if hintRows(withHelp, w) <= hintRows(text, w) {
		return withHelp
	}
	return text
}

func (m model) footer(w int) string {
	var footerText string
	board := false
	switch {
	case m.reviewDiff.target != "":
		footerText = "j/k scroll · y approve + merge · n reject · esc close · ? help · q quit"
	case m.router.open && (m.detailFocus || m.fullscreen):
		footerText = withHelpHint("enter send · esc leave · ctrl+c quit", w)
	case m.router.open:
		footerText = withHelpHint("enter start · ctrl+s hold · esc leave · ctrl+c quit", w)
		if _, ok := m.routerMentionQuery(); ok {
			footerText = withHelpHint("tab complete · enter start · ctrl+s hold · esc leave", w)
		}
	case m.input.kind != "":
		footerText = withHelpHint("enter submit · ctrl+j/alt+enter newline · esc cancel", w)
	case m.tab == activityTab && m.activity.expanded:
		footerText = "esc collapse · " + keyHint("e") + " · ? help · " + keyHint("q")
	case m.tab == activityTab && (m.detailFocus || m.fullscreen):
		footerText = "enter expand · " + keyHint("e") + " · " + keyHint("tab") + " · ? help · " + keyHint("q")
	case m.detailFocus || m.fullscreen:
		footerText = "j/k scroll · " + keyHint("t") + " · " + keyHint("tab") + " · h tasks · ? help · " + keyHint("q")
	default:
		task, _ := m.selectedTask()
		footerText, board = boardHints(w, task, m.attentionCount() > 0), true
	}
	// The hint block keeps a fixed height: the board reserves the fullest hint
	// line any task needs. A shorter hint line or a task changing state must not
	// move the body above it. Notices and dialogs are overlays, not footer rows.
	line := hintRows(footerText, w)
	reserved := line
	if board {
		reserved = m.boardHintRows(w)
	}
	footer := renderHints(strings.Split(footerText, " · ")...)
	if pad := reserved - line; pad > 0 {
		footer += strings.Repeat("\n", pad)
	}
	if _, height := m.size(); height >= 14 {
		footer = m.statusPills(w) + "\n" + footer
	}
	return ansi.Hardwrap(footer, w, true)
}

// renderHints styles "key description" hints as one row.
func renderHints(hints ...string) string {
	parts := make([]string, len(hints))
	for i, hint := range hints {
		key, desc, _ := strings.Cut(hint, " ")
		parts[i] = keyStyle.Render(key) + mutedStyle.Render(" "+desc)
	}
	return strings.Join(parts, faintStyle.Render("  ·  "))
}

// fitHints drops trailing hints until the row fits width, so a label is never
// cut in half unless even the first one overflows.
func fitHints(width int, hints ...string) string {
	row := renderHints(hints...)
	for n := len(hints) - 1; n > 0 && lipgloss.Width(row) > width; n-- {
		row = renderHints(hints[:n]...)
	}
	return fit(row, width, 1)
}
