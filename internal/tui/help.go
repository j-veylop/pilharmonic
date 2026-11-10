package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var helpTopics = []struct{ title, body string }{
	{"Navigation", `h / l          Focus tasks / details
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
Esc            Return / close expanded activity`},
	{"Tasks", `@executor      Prefix a router request; Tab completes names
n              New task draft
/              Route new work
a              Related task in selected thread
b              Spawn subagent from running task
A              Choose executor before scheduling
P              Choose execution profile (model, tools) before scheduling
w              Toggle isolation (changes default to a worktree)
r / R          Start / requeue task
v / V          Approve / reject prepared worktree commit
c              Cancel task
d              Delete inactive task + worktree (confirm y)
D              Delete completed tasks + worktrees (confirm y)
x              Discard selected merged worktree (confirm y)
X              Discard all merged worktrees (confirm y)
f              Send context to thread subscribers
F              Follow up selected task
s              Pause / resume thread subscription
p              Report: kind + message
               progress, waiting, blocked, ready, failed`},
	{"Activity", `Tab            Switch output / history
t              Toggle tool activity
T              Show / hide thinking blocks
j / k          Select activity in detail focus
Enter          Expand / collapse selected tool call
v              All calls / latest attempt / errors
Ctrl-u / d     Scroll expanded arguments and results`},
	{"General", `esc            Close router / return to tasks
?              Open / close this help
q              Quit (closes help when open)
Ctrl-c         Quit application
Drag details   Select text; release copies via OSC 52
Paste          Use the terminal shortcut in composers

COMPOSERS & PICKERS
Enter          Submit input / confirm agent or profile
Ctrl-j         Insert newline in composer
Alt-enter      Insert newline in composer
Arrows         Move cursor / wrapped line
Home / End     Line start / end
PgUp / PgDn    Move through long input
Esc            Cancel input / close picker
j / k          Select in executor or profile picker
Typing         Text in composers`},
}

func (m model) helpKey(key string) (model, tea.Cmd) {
	switch key {
	case "ctrl+c":
		return m, tea.Quit
	case "?", "esc", "q":
		m.help = false
	case "l", "right", " ", "tab":
		m.scrollHelp(1)
	case "h", "left", "shift+tab":
		m.scrollHelp(-1)
	case "j", "down", "ctrl+e":
		m.scrollHelpBody(1)
	case "k", "up", "ctrl+y":
		m.scrollHelpBody(-1)
	case "pgdown", "ctrl+f", "ctrl+d":
		m.scrollHelpBody(8)
	case "pgup", "ctrl+b", "ctrl+u":
		m.scrollHelpBody(-8)
	case "home", "g":
		m.helpPage, m.helpScroll = 0, 0
	case "end", "G":
		m.helpPage, m.helpScroll = len(helpTopics)-1, 0
	}
	return m, nil
}

func (m model) helpLines(width int) []string {
	page := max(0, min(len(helpTopics)-1, m.helpPage))
	return strings.Split(ansi.Hardwrap(helpTopics[page].body, max(1, width), true), "\n")
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
	for i, topic := range helpTopics {
		style := inactiveTabStyle.Padding(0, 1)
		if i == page {
			style = activeTabStyle.Padding(0, 1)
		}
		tabs = append(tabs, style.Render(topic.title))
	}
	row := lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
	if lipgloss.Width(row) > width {
		return fit(accentStyle.Render("‹ "+helpTopics[page].title+" ›")+mutedStyle.Render(fmt.Sprintf(" %d/%d", page+1, len(helpTopics))), width, 1)
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
	content = append(content, accentStyle.Render("KEYBOARD SHORTCUTS")+mutedStyle.Render(fmt.Sprintf("  Page %d / %d", page+1, len(helpTopics))), m.helpTabs(mw-4))
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
	content = append(content, accentStyle.Render("h/l tabs · j/k scroll · esc close"))
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
