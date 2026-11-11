package tui

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
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
// label so status stays readable without relying on colour alone. This form
// describes the task on its own; the terminal model adds waiting-for-subagents
// when the task is running with active children.
func (m model) View() string {
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	if w < 30 || h < 10 {
		return fit("PILHARMONIC\nResize terminal to at least 30 × 10\nq quit", w, h)
	}
	header := gradientText("◈ PILHARMONIC")
	mode, modeColor := " TASKS ", accent
	if m.detailFocus || m.fullscreen {
		mode, modeColor = " DETAILS ", cyan
	}
	if m.input.kind != "" {
		mode, modeColor = " INSERT ", amber
	}
	if m.router {
		mode, modeColor = " REQUEST ", accent
	}
	mode = modeBadge(mode, modeColor)
	summary := m.headerSummary(max(1, w-lipgloss.Width(header)-lipgloss.Width(mode)-1))
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
