package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// helpTopic is one page of the help overlay. Rows are structured so they wrap
// to any frame width.
type helpTopic struct {
	title    string
	intro    []string
	sections []helpSection
	note     []string
}

// helpSection groups related shortcuts under a heading. An empty heading
// continues the previous group without a label.
type helpSection struct {
	heading string
	rows    []helpRow
}

// helpRow is one shortcut and its description.
type helpRow struct {
	keys string
	desc string
}

var helpTopics = []helpTopic{
	{
		title: "Concepts",
		sections: []helpSection{{rows: []helpRow{
			{"Request", "Work submitted with /. One compiled-in authorization step decides acceptance and records its audit evidence."},
			{"Task", "Accepted work with its own mandate, executor, status and history."},
			{"Route", "The automatic acceptance step that picks an executor."},
			{"Action", "Tasks that need you: blocked, waiting, awaiting review, undelivered input, merge failed, failed or interrupted."},
			{"Attempt", "One run of a task. Retry keeps the task and adds an attempt."},
			{"Usage", "Tokens and estimated cost per attempt; the board header sums them for this session, subagents included."},
			{"Review", "Approval merges prepared work; rejection keeps it for a follow-up."},
		}}},
	},
	{
		title: "Navigation",
		sections: []helpSection{{rows: []helpRow{
			{"h / l", "Focus tasks / details"},
			{"j / k", "Move down / up; counts: 5j, 3k"},
			{"gg / G", "First / last task or top / bottom"},
			{"!", "Next task needing action (board)"},
			{"Ctrl-u / d", "Half page up / down"},
			{"Ctrl-b / f", "Full page up / down"},
			{"Ctrl-y / e", "One detail line up / down"},
			{"[ / ]", "One detail line up / down"},
			{"PgUp / PgDn", "Page details"},
			{"Home / End", "Top / bottom of details"},
			{"Mouse wheel", "Scroll details"},
			{"Enter", "Toggle task/detail focus"},
			{"z", "Toggle full-width details"},
			{"Esc", "Return / collapse expanded call"},
		}}},
	},
	{
		title: "Tasks",
		sections: []helpSection{{rows: []helpRow{
			{"@executor", "Prefix a request; Tab completes names"},
			{"n", "New task without routing"},
			{"/", "New request; acceptance is decided and recorded"},
			{"a", "Related task in selected thread"},
			{"b", "Spawn subagent from running task"},
			{"A", "Choose executor before starting"},
			{"P", "Choose profile (model, tools) before starting"},
			{"w", "Toggle worktree isolation before start"},
			{"r / R", "Start / retry task"},
			{"v / V", "Approve (merge) / reject prepared worktree commit"},
			{"c", "Cancel task"},
			{"d", "Delete inactive task + worktree (confirm y)"},
			{"D", "Delete completed tasks + worktrees (confirm y)"},
			{"x", "Discard selected merged worktree (confirm y)"},
			{"X", "Discard all merged worktrees (confirm y)"},
			{"f", "Send context to thread subscribers"},
			{"F", "Follow up task; a rejected worktree is kept for this"},
			{"s", "Pause / resume thread subscription"},
			{"p", "Report: kind + message progress, waiting, blocked, ready, failed"},
		}}},
	},
	{
		title: "Activity",
		sections: []helpSection{{rows: []helpRow{
			{"Tab", "Switch output / history"},
			{"t", "Toggle tool activity"},
			{"T", "Show / hide thinking blocks"},
			{"j / k", "Select activity in detail focus"},
			{"Enter", "Expand / collapse selected tool call"},
			{"f", "All calls / latest attempt / errors"},
			{"Ctrl-u / d", "Scroll expanded arguments and results"},
		}}},
	},
	{
		title: "Commands",
		intro: []string{
			"Type these into the NEW REQUEST form; they are never submitted as a request. Arguments follow the command name, as in /login <provider> or /model [big|small].",
		},
		sections: []helpSection{{
			heading: "SLASH COMMANDS",
			rows: []helpRow{
				{"/login", "Pick a provider"},
				{"/logout", "Forget a provider's stored key"},
				{"/providers", "List providers and whether a key is stored"},
				{"/model", "Choose the models, or open the menu"},
				{"/agents", "Choose an agent's model class"},
				{"/help", "Open this help"},
			},
		}},
	},
	{
		title: "Editing",
		intro: []string{
			"The request form and every composer dock as one input box above the footer hints; the draft wraps inside it and grows to six rows.",
		},
		sections: []helpSection{{rows: []helpRow{
			{"Ctrl-w", "Delete previous word (also Alt-backspace)"},
			{"Alt-d", "Delete next word"},
			{"Ctrl-k / u", "Delete to line end / line start"},
			{"Ctrl-d", "Delete next character (also Delete)"},
			{"Left / Right", "One character (also Ctrl-b / Ctrl-f)"},
			{"Ctrl-← / →", "One word (also Alt-b / Alt-f)"},
			{"Up / Down", "One wrapped line (also Ctrl-p / Ctrl-n)"},
			{"Home / End", "Line start / line end (also Ctrl-a / Ctrl-e)"},
			{"PgUp / PgDn", "Move through long input"},
			{"Enter", "Submit; Ctrl-j or Alt-enter inserts a newline"},
			{"Esc", "Cancel input; the request draft is kept"},
			{"Tab", "Focus the action or recent list; Shift-Tab reverses"},
			{"j / k", "Select a row in that list; Enter opens the task"},
		}}},
		note: []string{"Word deletion stays on the line you are editing."},
	},
	{
		title: "General",
		sections: []helpSection{
			{rows: []helpRow{
				{"m", "Choose the big and small models"},
				{"L", "Choose a provider and store its API key"},
				{"esc", "Close request form / return to tasks"},
				{"?", "Open / close this help (not while typing)"},
				{"F1", "Open / close this help from any screen"},
				{"q / Ctrl-c", "Quit; the dialog takes y/Enter to confirm, n/Esc to stay"},
				{"Drag details", "Select text; release copies via OSC 52"},
				{"Paste", "Use the terminal shortcut in composers"},
			}},
			{heading: "COMPOSERS & PICKERS", rows: []helpRow{
				{"Enter", "Submit input / select picker item"},
				{"Ctrl-j", "Insert newline (also Alt-enter)"},
				{"Esc", "Cancel input / close picker"},
				{"j/k or ↑↓", "Select in executor or profile picker"},
			}},
		},
		note: []string{"The Editing topic lists every cursor and word key."},
	},
}

const (
	helpMaxWidth  = 76
	helpMaxHeight = 28
	helpKeyGutter = 3
	// helpHangIndent indents descriptions under their keys when the frame is
	// too narrow for two columns of at least helpMinDescCols.
	helpHangIndent  = 2
	helpMinDescCols = 12
	helpBodyIndent  = 1
	// helpScrollGutter is reserved on every topic so text never re-wraps when
	// the operator switches to a topic that scrolls.
	helpScrollGutter = 2
	// helpChromeRows are the border, title, topic strip, gap under the header,
	// gap above the hints and the hint row. A frame without a strip has one fewer.
	helpChromeRows = 7
	// Every tab reserves the active marker's column so the strip never shifts.
	helpActiveTabMarker = "▸"
	helpInactiveTabLead = " "
)

// helpKeyColumn is the description column for every topic: the widest shortcut
// in the overlay plus a gutter, so descriptions never slide between topics.
func helpKeyColumn() int {
	widest := 0
	for _, topic := range helpTopics {
		for _, section := range topic.sections {
			for _, row := range section.rows {
				widest = max(widest, lipgloss.Width(row.keys))
			}
		}
	}
	return widest + helpKeyGutter
}

// helpLines wraps a topic to width as styled lines. Wrapped descriptions hang
// under the key column, or under a hanging indent below each key when the
// frame is too narrow for two columns.
func helpLines(topic helpTopic, width int) []string {
	width = max(1, width)
	keyWidth := helpKeyColumn()
	stacked := width-keyWidth < helpMinDescCols
	if stacked {
		keyWidth = helpHangIndent
	}
	var out []string
	blank := func() {
		if n := len(out); n > 0 && out[n-1] != "" {
			out = append(out, "")
		}
	}
	prose := func(paragraphs []string) {
		if len(paragraphs) == 0 {
			return
		}
		blank()
		for _, paragraph := range paragraphs {
			for _, part := range wrapHelp(paragraph, width) {
				out = append(out, mutedStyle.Render(part))
			}
		}
	}
	prose(topic.intro)
	blank()
	for _, section := range topic.sections {
		if section.heading != "" {
			blank()
			for _, part := range wrapHelp(section.heading, width) {
				line := helpHeadingStyle.Render(part)
				if rule := width - lipgloss.Width(part) - 1; rule > 0 {
					line += " " + helpTrackStyle.Render(strings.Repeat("─", rule))
				}
				out = append(out, line)
			}
		}
		for _, row := range section.rows {
			key := padHelp(keyStyle.Render(row.keys), keyWidth)
			if stacked {
				out = append(out, key)
				key = ""
			}
			for _, part := range wrapHelp(row.desc, max(1, width-keyWidth)) {
				out = append(out, padHelp(key, keyWidth)+helpDescStyle.Render(part))
				key = ""
			}
		}
	}
	prose(topic.note)
	return out
}

// wrapHelp wraps plain text to width cells, breaking a word only when it
// cannot fit on a line of its own.
func wrapHelp(text string, width int) []string {
	return strings.Split(ansi.Wrap(text, max(1, width), ""), "\n")
}

// padHelp pads a possibly styled string to width cells without truncating it.
func padHelp(s string, width int) string {
	return s + strings.Repeat(" ", max(0, width-lipgloss.Width(s)))
}

func (m model) helpKey(key string) model {
	switch key {
	case "ctrl+c":
		return m.requestQuit()
	case "?", "f1", "esc", "q":
		m.help.open = false
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
		m.help.page, m.help.scroll = 0, 0
	case "end", "G":
		m.help.page, m.help.scroll = len(helpTopics)-1, 0
	}
	return m
}

// scrollHelp pages between topics without wrapping; staying on the same topic
// keeps its scroll position.
func (m *model) scrollHelp(delta int) {
	if page := max(0, min(len(helpTopics)-1, m.help.page+delta)); page != m.help.page {
		m.help.page, m.help.scroll = page, 0
	}
}

// scrollHelpBody uses the overlay's own geometry so scrolling always reaches
// the last drawn line.
func (m *model) scrollHelpBody(delta int) {
	mw, _, bodyRows := helpFrame(m.size())
	total := len(helpLines(helpTopics[m.helpPageIndex()], helpBodyWidth(mw)))
	m.help.scroll = max(0, min(max(0, total-bodyRows), m.help.scroll+delta))
}

func (m model) helpPageIndex() int {
	return max(0, min(len(helpTopics)-1, m.help.page))
}

// helpBodyWidth is the wrap width inside a frame mw cells wide: the borders,
// body indent and scrollbar gutter are always subtracted.
func helpBodyWidth(mw int) int {
	return max(1, mw-4-helpBodyIndent-helpScrollGutter)
}

// helpTabs draws the topic strip with titles when they fit, positions when
// they do not, and nothing when even those overflow; the title row still names
// the topic.
func helpTabs(width, page int) string {
	titles, numbers := make([]string, len(helpTopics)), make([]string, len(helpTopics))
	for i, topic := range helpTopics {
		titles[i], numbers[i] = topic.title, strconv.Itoa(i+1)
	}
	for _, labels := range [][]string{titles, numbers} {
		row := ""
		for i, label := range labels {
			if i == page {
				row += activeTabStyle.Render(helpActiveTabMarker+label) + " "
			} else {
				row += inactiveTabStyle.Render(helpInactiveTabLead+label) + " "
			}
		}
		if lipgloss.Width(row) <= width {
			return row
		}
	}
	return ""
}

// helpFrame sizes the modal for a w by h terminal. The frame is as tall as the
// tallest topic needs at this width, so switching topics never resizes it.
func helpFrame(w, h int) (mw, mh, bodyRows int) {
	mw = min(helpMaxWidth, max(4, w-4))
	ceiling := min(helpMaxHeight, max(3, h-2))
	tallest := 1
	for _, topic := range helpTopics {
		tallest = max(tallest, len(helpLines(topic, helpBodyWidth(mw))))
	}
	chrome := helpChromeRows
	if helpTabs(mw-4, 0) == "" {
		chrome--
	}
	if ceiling <= chrome {
		return mw, ceiling, 0
	}
	bodyRows = min(tallest, ceiling-chrome)
	return mw, bodyRows + chrome, bodyRows
}

// helpScrollBar draws one row of the scrollbar column.
func helpScrollBar(row, bodyRows, start, total int) string {
	if total <= bodyRows {
		return " "
	}
	size := max(1, bodyRows*bodyRows/total)
	top := start * (bodyRows - size) / (total - bodyRows)
	if row >= top && row < top+size {
		return helpThumbStyle.Render("▐")
	}
	return helpTrackStyle.Render("│")
}

// helpTitleRow names the topic and right-aligns its position.
func (m model) helpTitleRow(width int) string {
	page := m.helpPageIndex()
	left := accentStyle.Render("HELP") + faintStyle.Render(" · ") + mutedStyle.Render(helpTopics[page].title)
	right := faintStyle.Render(fmt.Sprintf("%d/%d", page+1, len(helpTopics)))
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return fit(left, width, 1)
	}
	return left + strings.Repeat(" ", gap) + right
}

// helpModal builds the frame content for the current page, or nil when the
// terminal cannot hold a title, a body row and the hints.
func (m model) helpModal(w, h int) (mw, mh int, content []string) {
	mw, mh, bodyRows := helpFrame(w, h)
	if mw < 12 || bodyRows < 1 {
		return mw, mh, nil
	}
	inner, bodyWidth := mw-4, helpBodyWidth(mw)
	lines := helpLines(helpTopics[m.helpPageIndex()], bodyWidth)
	total := len(lines)
	start := min(max(0, m.help.scroll), max(0, total-bodyRows))
	content = append(content, m.helpTitleRow(inner))
	if strip := helpTabs(inner, m.helpPageIndex()); strip != "" {
		content = append(content, strip)
	}
	content = append(content, "")
	for row := 0; row < bodyRows; row++ {
		line := ""
		if i := start + row; i < total {
			line = fit(lines[i], bodyWidth, 1)
		}
		content = append(content, strings.Repeat(" ", helpBodyIndent)+padHelp(line, bodyWidth)+
			" "+helpScrollBar(row, bodyRows, start, total))
	}
	return mw, mh, append(content, "", fitHints(inner, "esc close", "h/l topics", "j/k scroll"))
}

func (m model) helpView(background string, w, h int) string {
	mw, mh, content := m.helpModal(w, h)
	return modalView(background, w, h, mw, mh, content)
}

// textEntryFocused reports whether the active surface owns printable keys, so
// ? stays text in a draft, an API key or a custom model ID. F1 always opens help.
func (m model) textEntryFocused() bool {
	return m.login.provider != "" || m.input.kind != "" ||
		(m.router.open && m.router.focus == routerFocusComposer) ||
		(m.models.menu && m.models.step == modelStepCustom)
}

// helpShortcut reports whether msg opens help here. Pasted text never does.
func (m model) helpShortcut(msg tea.KeyMsg) bool {
	key := msg.String()
	return !msg.Paste && (key == "f1" || key == "?" && !m.textEntryFocused())
}

// openHelp shows the overlay on its first topic above whatever is on screen,
// which closing help returns to unchanged.
func (m model) openHelp() model {
	m.help = helpState{open: true}
	m.quitConfirm = false
	m.vimG, m.vimCount = false, 0
	return m
}
