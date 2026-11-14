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

// layout is the single source of responsive geometry: the body height between
// header and footer, whether the sidebar shows, and each panel's width.
type layout struct {
	width, height int
	bodyHeight    int
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
	var body string
	switch {
	case m.router.open:
		body = m.routerPanel(w, l.bodyHeight)
	case l.sidebar:
		body = lipgloss.JoinHorizontal(lipgloss.Top, m.taskPanel(l.taskWidth, l.bodyHeight), " ", m.detailPanel(l.detailWidth, l.bodyHeight))
	default:
		body = m.detailPanel(w, l.bodyHeight)
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
	// Menus draw over the board, help over menus, and the quit dialog last.
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
	if m.quitConfirm {
		screen = m.quitView(screen, w, h)
	}
	return screen
}
