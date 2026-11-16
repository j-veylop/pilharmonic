package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// toastView draws transient notices as a top-right overlay. Nothing in the
// frame moves when a notice appears or expires.
func (m model) toastView(screen string, w, h int) string {
	var messages []string
	if m.notice.service != "" {
		messages = append(messages, warningStyle.Render("› "+singleLine(m.notice.service)))
	}
	if m.notice.text != "" {
		messages = append(messages, accentStyle.Render("› "+singleLine(m.notice.text)))
	}
	y := 1
	for _, message := range messages {
		if y >= h-3 {
			break
		}
		mw := min(56, w-4)
		if mw < 12 {
			return screen
		}
		content := ansi.Hardwrap(message, mw-4, true)
		mh := strings.Count(content, "\n") + 3
		box := focusedPanel(content, mw, mh, false)
		screen = placeOverlay(screen, box, w, h, w-mw-1, y)
		y += mh
	}
	return screen
}

// placeOverlay writes box over screen at x,y without dimming the background or
// changing the frame size.
func placeOverlay(screen, box string, w, h, x, y int) string {
	rows := strings.Split(screen, "\n")
	boxRows := strings.Split(box, "\n")
	for i, row := range boxRows {
		line := y + i
		if line < 0 || line >= len(rows) || line >= h {
			continue
		}
		plain := fit(rows[line], w, 1)
		left := ansi.Cut(plain, 0, max(0, x))
		right := ansi.Cut(plain, min(w, x+lipgloss.Width(row)), w)
		rows[line] = fit(left+row+right, w, 1)
	}
	return strings.Join(rows, "\n")
}

// actionPromptView shows confirmations that need a decision as dialogs instead
// of transient footer rows.
func (m model) actionPromptView(screen string, w, h int) string {
	var title, body, keys string
	switch {
	case m.quitConfirm:
		title = "QUIT  › Leave pilharmonic?"
		body = m.quitCountLine() + "\nHistory and worktrees are kept."
		keys = "y quit  ·  n/esc stay"
	case m.review.target != "":
		title = "REVIEW  › " + singleLine(m.review.title)
		body = "commit " + singleLine(m.review.commit)
		keys = "approve + merge (y)  ·  reject (n)  ·  esc"
	case m.cleanup.kind != "":
		name, answer, note := m.cleanup.prompt()
		title, body, keys = name, answer+"\n"+note, "y confirm  ·  n/esc cancel  ·  f1 help"
	default:
		return screen
	}
	mw := min(64, w-4)
	inner := max(12, mw-4)
	body = ansi.Wordwrap(body, inner, " -")
	title = ansi.Wordwrap(title, inner, " -")
	keys = ansi.Wordwrap(keys, inner, " -")
	lines := strings.Count(title, "\n") + strings.Count(body, "\n") + strings.Count(keys, "\n") + 3
	content := []string{accentStyle.Render(title), "", mutedStyle.Render(body), "", accentStyle.Render(keys)}
	mh := lines + 4
	if mh > h {
		mh = lines + 2
		content = []string{accentStyle.Render(title), mutedStyle.Render(body), accentStyle.Render(keys)}
	}
	return modalView(screen, w, h, mw, mh, content)
}
