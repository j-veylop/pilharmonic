package tui

import (
	"fmt"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"strings"
)

const shortcutHelp = `NAVIGATION
h / l          Focus tasks / details
j / k          Move down / up; counts: 5j, 3k
gg / G         First / last task or top / bottom
Ctrl-u / d     Half page up / down
Ctrl-b / f     Full page up / down
Ctrl-y / e     One detail line up / down
PgUp / PgDn    Page details
Home / End     Top / bottom of details
Mouse wheel    Scroll details
Enter          Toggle pane focus
z              Expand details to full width
Esc            Return / close expanded activity

TASKS
n              New task
a              Related task in selected thread
A              Choose executor before scheduling
w              Toggle Git worktree isolation before scheduling
r / R          Start / requeue unsuccessful task
c              Cancel task
d              Delete inactive task (confirm y)
f              Send context to thread subscribers
s              Pause / resume thread subscription
p              Report: kind followed by message
               progress, waiting, blocked, ready, failed

VIEWS & ACTIVITY
Tab            Switch output / history
t              Toggle tool activity
j / k          Select activity in detail focus
Enter          Expand / collapse selected tool call
v              All calls / latest attempt / errors
Ctrl-u / d     Scroll expanded arguments and results

GENERAL
?              Open / close this help
q              Quit (closes help when open)
Ctrl-c         Quit application

COMPOSERS & PICKERS
Enter          Submit input / confirm agent
Esc            Cancel input / close picker
j / k          Select agent in picker
Typing         Normal text in task/report input`

var helpTopics = []string{"Navigation", "Tasks", "Activity", "General"}

func (m model) helpLines(width int) []string {
	starts := []string{"NAVIGATION\n", "TASKS\n", "VIEWS & ACTIVITY\n", "GENERAL\n"}
	page := max(0, min(len(starts)-1, m.helpPage))
	start := strings.Index(shortcutHelp, starts[page]) + len(starts[page])
	end := len(shortcutHelp)
	if page+1 < len(starts) {
		end = strings.Index(shortcutHelp, starts[page+1])
	}
	return strings.Split(ansi.Hardwrap(strings.TrimSpace(shortcutHelp[start:end]), max(1, width), true), "\n")
}
func (m *model) scrollHelp(delta int) {
	m.helpPage = max(0, min(len(helpTopics)-1, m.helpPage+delta))
	m.helpScroll = 0
}
func (m *model) scrollHelpBody(delta int) {
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	available := max(1, min(28, h-2)-5)
	m.helpScroll = max(0, min(max(0, len(m.helpLines(min(76, w-4)-4))-available), m.helpScroll+delta))
}
func (m model) helpTabs(width int) string {
	var tabs []string
	page := max(0, min(len(helpTopics)-1, m.helpPage))
	for i, title := range helpTopics {
		style := mutedStyle.Padding(0, 1)
		if i == page {
			style = lipgloss.NewStyle().Foreground(ink).Background(surface).Bold(true).Padding(0, 1).Underline(true)
		}
		tabs = append(tabs, style.Render(title))
	}
	row := lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
	if lipgloss.Width(row) > width {
		return fit(accentStyle.Render("‹ "+helpTopics[page]+" ›")+mutedStyle.Render(fmt.Sprintf(" %d/4", page+1)), width, 1)
	}
	return row
}
func (m model) helpView(background string, w, h int) string {
	mw, mh := min(76, w-4), min(28, h-2)
	available := max(1, mh-5)
	lines := m.helpLines(mw - 4)
	page := max(0, min(len(helpTopics)-1, m.helpPage))
	start := min(max(0, m.helpScroll), max(0, len(lines)-available))
	var content []string
	content = append(content, accentStyle.Render("KEYBOARD SHORTCUTS")+mutedStyle.Render(fmt.Sprintf("  Page %d / 4", page+1)), m.helpTabs(mw-4))
	for _, line := range lines[start:min(len(lines), start+available)] {
		if line != "" && !strings.HasPrefix(line, " ") && !strings.Contains(line, "  ") {
			line = titleStyle.Render(line)
		} else {
			line = mutedStyle.Render(line)
		}
		content = append(content, line)
	}
	for len(content) < mh-3 {
		content = append(content, "")
	}
	content = append(content, accentStyle.Render("h/l tabs · j/k / wheel scroll · esc close"))
	modal := focusedPanel(strings.Join(content, "\n"), mw, mh, true)
	bg := strings.Split(background, "\n")
	box := strings.Split(modal, "\n")
	x, y := (w-mw)/2, (h-mh)/2
	result := make([]string, h)
	for row := range result {
		line := ""
		if row < len(bg) {
			line = ansi.Strip(bg[row])
		}
		line = lipgloss.PlaceHorizontal(w, lipgloss.Left, line)
		if row >= y && row < y+len(box) {
			result[row] = mutedStyle.Faint(true).Render(ansi.Cut(line, 0, x)) + box[row-y] + mutedStyle.Faint(true).Render(ansi.Cut(line, x+mw, w))
		} else {
			result[row] = mutedStyle.Faint(true).Render(line)
		}
	}
	return fit(strings.Join(result, "\n"), w, h)
}
