package tui

import (
	"strings"
	"time"
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
		l.composerDraft = m.composerDraftRows()
		l.composerRows = l.composerDraft + 1 + extra
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

// panelInner is the content width inside a borderless, two-cell-margin pane.
func (layout) panelInner(width int) int { return width - 4 }

// detailRows is how many content rows a detail panel of height shows below its
// tab row, title and position line.
func (layout) detailRows(height int) int { return height - 3 }

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
	if m.skills.open {
		screen = m.skillMenuView(screen, w, h)
	}
	if m.lsps.open {
		screen = m.lspMenuView(screen, w, h)
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
	return paintCanvas(screen, w, h)
}

// paintCanvas washes the whole frame in the pastel application background.
// Each rendered line is padded to the frame width, and the background is
// re-applied after every style reset so nested styling cannot punch holes in
// the surface.
func paintCanvas(screen string, width, height int) string {
	bg := canvasEscape()
	if bg == "" || width < 1 || height < 1 {
		return screen
	}
	lines := strings.Split(screen, "\n")
	for len(lines) < height {
		lines = append(lines, "")
	}
	lines = lines[:height]
	for i, line := range lines {
		line = repaintAfterResets(line, bg)
		if pad := width - ansi.StringWidth(line); pad > 0 {
			line += strings.Repeat(" ", pad)
		}
		lines[i] = bg + line + "\x1b[0m"
	}
	return strings.Join(lines, "\n")
}

// repaintAfterResets reapplies the canvas background after every SGR reset.
// Lipgloss emits both `\x1b[0m` and the bare `\x1b[m`, and a reset that is not
// followed by the canvas escape leaves later cells on the terminal's default
// background.
func repaintAfterResets(line, bg string) string {
	for _, reset := range []string{"\x1b[0m", "\x1b[m", "\x1b[00m"} {
		line = strings.ReplaceAll(line, reset, reset+bg)
	}
	return line
}

func (m model) attentionCount() int { return len(m.attentionTasks()) }

// inputFocused reports whether the composer owns the keyboard. The task list
// drops its selection row while typing so the input is the only active thing.
func (m model) inputFocused() bool { return m.router.open || m.input.kind != "" }

// headerRow is the wordmark and the rate badge. Task state and usage belong on
// the rows that carry it, so the header stays a flat brand line.
func (m model) headerRow(width int) string {
	header := wordmark()
	if badge := m.headerRateBadge(time.Now()); badge != "" {
		header += "  " + badge
	}
	return fit(header, width, 1)
}

// headerRateBadge names the discount window in force for the provider of a
// selected model, so peak and off-peak stay visible after the model menu
// closes. Peak carries a warning and off-peak a check; all-day providers carry
// no badge.
func (m model) headerRateBadge(now time.Time) string {
	if m.service == nil {
		return ""
	}
	selection := m.service.ModelSelection()
	selectors := []string{selection.Big, selection.Small}
	for _, selector := range selection.Agents {
		selectors = append(selectors, selector)
	}
	for _, selector := range selectors {
		label := modelRateLabel(selector, now)
		if label == "" {
			continue
		}
		provider, _, _ := strings.Cut(selector, "/")
		icon, style := warningStyle.Render("⚠"), warningStyle
		if label == orchestrator.RateOffPeak {
			icon, style = successStyle.Render("✓"), successStyle
		}
		return mutedStyle.Render(provider) + " " + icon + " " + style.Render(label)
	}
	return ""
}

// shortModel keeps the model ID without its provider prefix, which the pill
// name already implies.
func shortModel(selector string) string {
	if _, model, ok := strings.Cut(selector, "/"); ok {
		return model
	}
	return selector
}

// modelSummary names the big and small models where the board can show them.
// The small model is dropped before the big one when space runs out.
func (m model) modelSummary(width int) string {
	if m.service == nil || width < 4 {
		return ""
	}
	selection := m.service.ModelSelection()
	var parts []string
	for _, class := range []struct{ name, model string }{{"BIG", selection.Big}, {"SMALL", selection.Small}} {
		if class.model == "" {
			continue
		}
		parts = append(parts, faintStyle.Render(class.name)+" "+mutedStyle.Render(shortModel(class.model)))
	}
	join := func(parts []string) string { return strings.Join(parts, faintStyle.Render("  ·  ")) }
	row := join(parts)
	for len(parts) > 1 && lipgloss.Width(row) > width {
		parts = parts[:len(parts)-1]
		row = join(parts)
	}
	if lipgloss.Width(row) > width {
		return ""
	}
	return row
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
		footerText = "j/k scroll · y approve + merge · n reject · ctrl+o nvim · ctrl+g lazygit · ctrl+t terminal · esc close · q quit"
	case m.login.provider != "":
		footerText = withHelpHint("enter save · esc cancel", w)
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
	return ansi.Hardwrap(footer, w, true)
}

// renderHints styles "key description" hints as one row.
func renderHints(hints ...string) string {
	parts := make([]string, len(hints))
	for i, hint := range hints {
		key, desc, _ := strings.Cut(hint, " ")
		parts[i] = keyStyle.Render(key) + faintStyle.Render(" "+desc)
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
