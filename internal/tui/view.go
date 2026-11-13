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

// singleLine flattens untrusted text for a one-row surface: terminal control
// characters are stripped and embedded newlines become spaces.
func singleLine(s string) string {
	return strings.ReplaceAll(terminalText(s), "\n", " ")
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
	composerDraft int // draft rows inside the docked input box
	composerRows  int // total rows of the docked input block, hint rows included
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
	footerRows := strings.Count(m.footer(w), "\n") + 1
	if m.composerOpen() {
		extra := m.composerExtraRows()
		l.composerDraft = m.composerDraftRows(footerRows + extra)
		l.composerRows = l.composerDraft + 2 + extra
	}
	l.bodyHeight = h - 1 - footerRows - l.composerRows
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
		prompt := "q quit"
		if m.quitConfirm {
			// The frame cannot hold the dialog, but the answer keys still work,
			// so the resize prompt explains them.
			prompt = "y quit · n/esc stay"
		}
		return fit("PILHARMONIC\nResize terminal to at least 30 × 10\n"+prompt, w, h)
	}
	top := m.headerRow(w)
	footer := m.footer(w)
	bodyHeight := l.bodyHeight
	var body string
	if m.router.open {
		body = m.routerPanel(w, bodyHeight)
	} else if l.sidebar {
		left := l.taskWidth
		right := l.detailWidth
		body = lipgloss.JoinHorizontal(lipgloss.Top, m.taskPanel(left, bodyHeight), " ", m.detailPanel(right, bodyHeight))
	} else {
		body = m.detailPanel(w, bodyHeight)
	}

	rows := []string{top}
	if body != "" {
		rows = append(rows, body)
	}
	if l.composerRows > 0 {
		rows = append(rows, m.composerBlock(w, l.composerDraft)...)
	}
	rows = append(rows, footer)
	screen := fit(strings.Join(rows, "\n"), w, h)
	// Menus compose over the board and help composes over everything, so the
	// overlay an operator opened is always the one on top.
	if m.login.menu {
		screen = m.loginMenuView(screen, w, h)
	}
	if m.models.menu {
		screen = m.modelMenuView(screen, w, h)
	}
	if m.agentClass.menu {
		screen = m.agentClassMenuView(screen, w, h)
	}
	if m.help.open {
		screen = m.helpView(screen, w, h)
	}
	// The quit confirmation answers an exit request from any of those surfaces,
	// so it is always the last frame drawn.
	if m.quitConfirm {
		screen = m.quitView(screen, w, h)
	}
	return screen
}
