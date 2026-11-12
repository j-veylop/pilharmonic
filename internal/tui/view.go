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
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), r == '\u2028', r == '\u2029':
		default:
			b.WriteRune(r)
			col += lipgloss.Width(string(r))
		}
	}
	return b.String()
}

// layout is the single source of the terminal's responsive geometry: the body
// height left between header and footer, whether the task sidebar shows, and
// the width each panel receives. Panels derive their inner rows and columns
// from it so the breakpoint is not duplicated.
type layout struct {
	width, height int
	bodyHeight    int
	sidebar       bool
	taskWidth     int
	detailWidth   int
}

func (m model) layout() layout {
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	l := layout{width: w, height: h}
	if w < 30 || h < 10 {
		return l
	}
	l.bodyHeight = h - 1 - (strings.Count(m.footer(w), "\n") + 1)
	l.sidebar = w >= 76 && !m.fullscreen
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

// taskState gives each task one status word, glyph and semantic colour. The
// task sidebar renders only the glyph, while other surfaces pair it with the
// label so status stays readable without relying on colour alone. This form
// describes the task on its own; the terminal model adds waiting-for-subagents
// when the task is running with active children.
func (m model) View() string {
	l := m.layout()
	w, h := l.width, l.height
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
	bodyHeight := l.bodyHeight
	var body string
	if m.router {
		body = m.routerPanel(w, bodyHeight)
	} else if l.sidebar {
		left := l.taskWidth
		right := l.detailWidth
		body = lipgloss.JoinHorizontal(lipgloss.Top, m.taskPanel(left, bodyHeight), " ", m.detailPanel(right, bodyHeight))
	} else {
		body = m.detailPanel(w, bodyHeight)
	}

	screen := fit(top+"\n"+body+"\n"+footer, w, h)
	if m.loginMenu {
		return m.loginMenuView(screen, w, h)
	}
	if m.modelMenu {
		return m.modelMenuView(screen, w, h)
	}
	if m.agentClassMenu {
		return m.agentClassMenuView(screen, w, h)
	}
	if m.help {
		return m.helpView(screen, w, h)
	}
	return screen
}
