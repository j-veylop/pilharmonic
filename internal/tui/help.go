package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// A help topic is structured rather than pre-formatted, so one definition feeds
// both projections: helpLines projects it to plain text (tests and copy/paste)
// and the overlay projects the same lines to styled cells. Nothing is inferred
// from indentation, so a description can wrap without being mistaken for a
// heading.
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

// helpRow is either a two-column shortcut row or, when keys is empty, a prose
// line that spans the width.
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
			"Type these into the NEW REQUEST form; they are never submitted as a request.",
		},
		sections: []helpSection{{
			heading: "SLASH COMMANDS",
			rows: []helpRow{
				{desc: "/login — pick a provider (or /login <provider>)"},
				{desc: "/logout <provider>   Forget a provider's stored key"},
				{desc: "/providers     List providers and whether a key is stored"},
				{desc: "/model [big|small]   Choose the models, or open the menu"},
				{desc: "/agents        Choose an agent's model class"},
				{desc: "/help          Open this help"},
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
			{"Ctrl-<- / ->", "One word (also Alt-b / Alt-f)"},
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
				{"q / Ctrl-c", "Quit (closes help when open)"},
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
	// helpKeyGutter separates the shortcut column from its description column.
	helpKeyGutter = 2
	// helpHangIndent is the hanging indent used when the frame is too narrow for
	// two columns: the keys take one line and the description hangs below them.
	helpHangIndent = 2
	// helpMinDescCols is the narrowest description column worth aligning to.
	helpMinDescCols = 12
	// helpActiveTabMarker precedes the topic the operator is reading, so the
	// strip still marks it where the terminal cannot show the accent colour.
	helpActiveTabMarker = "▸ "
)

// helpLineKind separates the three things a help line can be, so the styled
// projection never has to guess from the text.
type helpLineKind int

const (
	helpProseLine helpLineKind = iota
	helpHeadingLine
	helpRowLine
)

// helpLine is one rendered line of a topic. keys is only set on the first line
// of a shortcut row; a wrapped continuation carries the description alone and
// is indented to the same column. lead is the already-present prefix of a prose
// row that reads as a command word.
type helpLine struct {
	kind helpLineKind
	keys string
	text string
	lead string
}

// helpLayout is a topic projected onto a width: the measured key column plus the
// lines it renders to. Both projections walk it in order, so the plain text the
// tests assert on and the styled cells on screen can never disagree.
type helpLayout struct {
	keyWidth int
	lines    []helpLine
}

// layoutHelpTopic wraps a topic to width. Descriptions align in one column and
// wrapped continuations hang under that column; on a frame too narrow for two
// columns each key takes its own line and the description hangs two cells in.
func layoutHelpTopic(topic helpTopic, width int) helpLayout {
	width = max(1, width)
	layout := helpLayout{}
	widest := 0
	for _, section := range topic.sections {
		for _, row := range section.rows {
			if row.keys != "" {
				widest = max(widest, lipgloss.Width(row.keys))
			}
		}
	}
	switch {
	case widest == 0:
		layout.keyWidth = 0
	case width-widest-helpKeyGutter >= helpMinDescCols:
		layout.keyWidth = widest + helpKeyGutter
	default:
		layout.keyWidth = helpHangIndent
	}
	stacked := widest > 0 && layout.keyWidth == helpHangIndent
	descWidth := max(1, width-layout.keyWidth)

	add := func(line helpLine) { layout.lines = append(layout.lines, line) }
	blank := func() {
		if n := len(layout.lines); n > 0 && (layout.lines[n-1].text != "" || layout.lines[n-1].keys != "") {
			add(helpLine{})
		}
	}
	for _, paragraph := range topic.intro {
		for _, part := range wrapHelp(paragraph, width) {
			add(helpLine{kind: helpProseLine, text: part})
		}
	}
	if len(topic.intro) > 0 {
		blank()
	}
	for _, section := range topic.sections {
		if section.heading != "" {
			blank()
			add(helpLine{kind: helpHeadingLine, text: section.heading})
		}
		for _, row := range section.rows {
			if row.keys == "" {
				for i, part := range wrapHelp(row.desc, descWidth) {
					line := helpLine{kind: helpProseLine, text: part}
					if i == 0 {
						line.lead = helpLead(part)
					}
					add(line)
				}
				continue
			}
			if stacked {
				add(helpLine{kind: helpRowLine, keys: row.keys})
				for _, part := range wrapHelp(row.desc, descWidth) {
					add(helpLine{kind: helpRowLine, text: part})
				}
				continue
			}
			for i, part := range wrapHelp(row.desc, descWidth) {
				line := helpLine{kind: helpRowLine, text: part}
				if i == 0 {
					line.keys = row.keys
				}
				add(line)
			}
		}
	}
	if len(topic.note) > 0 {
		blank()
		for _, paragraph := range topic.note {
			for _, part := range wrapHelp(paragraph, width) {
				add(helpLine{kind: helpProseLine, text: part})
			}
		}
	}
	return layout
}

// wrapHelp wraps plain text to width cells. Words stay whole when they fit on a
// line of their own and are broken only when they cannot, so a description can
// never be clipped by the frame.
func wrapHelp(text string, width int) []string {
	if strings.TrimSpace(text) == "" {
		return []string{""}
	}
	return strings.Split(ansi.Wrap(text, max(1, width), ""), "\n")
}

// helpLead returns the leading token of a prose row so the command word can be
// accented without changing the text the operator reads.
func helpLead(text string) string {
	if i := strings.IndexByte(text, ' '); i > 0 {
		return text[:i]
	}
	return ""
}

// padHelp pads a possibly styled cell string out to width cells. It never
// truncates, so a long key keeps every character it needs.
func padHelp(s string, width int) string {
	if w := lipgloss.Width(s); w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return s
}

// plain is the unstyled projection: the same lines and the same columns, with
// trailing padding removed.
func (l helpLayout) plain() []string {
	out := make([]string, 0, len(l.lines))
	for _, line := range l.lines {
		if line.kind != helpRowLine {
			out = append(out, line.text)
			continue
		}
		out = append(out, strings.TrimRight(padHelp(line.keys, l.keyWidth)+line.text, " "))
	}
	return out
}

// styled is the projection the overlay draws: keys in the accent style,
// descriptions in ink, headings apart from both and prose muted.
func (l helpLayout) styled() []string {
	out := make([]string, 0, len(l.lines))
	for _, line := range l.lines {
		switch line.kind {
		case helpHeadingLine:
			out = append(out, helpHeadingStyle.Render(line.text))
		case helpRowLine:
			cell := ""
			if line.keys != "" {
				cell = keyStyle.Render(line.keys)
			}
			cell = padHelp(cell, l.keyWidth)
			if line.text != "" {
				cell += helpDescStyle.Render(line.text)
			}
			out = append(out, cell)
		default:
			switch {
			case line.text == "":
				out = append(out, "")
			case line.lead != "" && strings.HasPrefix(line.text, line.lead):
				out = append(out, keyStyle.Render(line.lead)+mutedStyle.Render(strings.TrimPrefix(line.text, line.lead)))
			default:
				out = append(out, mutedStyle.Render(line.text))
			}
		}
	}
	return out
}

func (m model) helpKey(key string) (model, tea.Cmd) {
	switch key {
	case "ctrl+c":
		return m, tea.Quit
	case "?", "f1", "esc", "q":
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
	return layoutHelpTopic(helpTopics[page], max(1, width)).plain()
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
	_, bodyRows, _, lines := m.helpBody(w, h)
	m.helpScroll = max(0, min(max(0, len(lines)-bodyRows), m.helpScroll+delta))
}

// helpPageIndex is the clamped topic the overlay shows.
func (m model) helpPageIndex() int {
	return max(0, min(len(helpTopics)-1, m.helpPage))
}

// helpBody projects the current page for a terminal of w by h, returning the
// window rows, the width the body is wrapped to and the styled lines. The
// overlay and the scroll clamp both read it, so keyboard scrolling always
// reaches the last line the overlay draws.
func (m model) helpBody(w, h int) (mw, bodyRows, bodyWidth int, lines []string) {
	mw, _, bodyRows = helpFrame(w, h)
	inner := max(1, mw-4)
	topic := helpTopics[m.helpPageIndex()]
	layout := layoutHelpTopic(topic, inner)
	bodyWidth = inner
	// A topic taller than the window keeps a scrollbar column; wrapping narrower
	// only ever adds lines, so the column stays after the re-wrap.
	if len(layout.lines) > bodyRows {
		bodyWidth = max(1, inner-2)
		layout = layoutHelpTopic(topic, bodyWidth)
	}
	return mw, bodyRows, bodyWidth, layout.styled()
}

// helpFrame returns the modal size and the number of body rows that fit inside
// it for a terminal of w by h cells. The title row and the topic strip sit above
// the body and the hint row below it.
func helpFrame(w, h int) (mw, mh, bodyRows int) {
	mw = min(76, max(4, w-4))
	mh = min(28, max(3, h-2))
	return mw, mh, max(1, mh-5)
}

// helpTabs draws the topic strip: the active topic carries a marker and the
// accent style, the others are muted. A strip too wide for the frame falls back
// to the position chip so it can never overflow the modal.
func (m model) helpTabs(width int) string {
	var tabs []string
	page := max(0, min(len(helpTopics)-1, m.helpPage))
	for i, topic := range helpTopics {
		style := inactiveTabStyle.Padding(0, 1)
		label := topic.title
		if i == page {
			style = activeTabStyle.Padding(0, 1)
			label = helpActiveTabMarker + label
		}
		tabs = append(tabs, style.Render(label))
	}
	row := lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
	if lipgloss.Width(row) > width {
		return fit(accentStyle.Render("‹ "+helpTopics[page].title+" ›")+mutedStyle.Render(fmt.Sprintf(" %d/%d", page+1, len(helpTopics))), width, 1)
	}
	return row
}

// helpHintRow keeps the footer hints inside the modal: keys in the accent style,
// descriptions muted, and a shorter set of hints when the frame is narrow.
func (m model) helpHintRow(width int) string {
	hints := func(pairs [][2]string) string {
		var parts []string
		for _, pair := range pairs {
			parts = append(parts, keyStyle.Render(pair[0])+mutedStyle.Render(" "+pair[1]))
		}
		return strings.Join(parts, faintStyle.Render(" · "))
	}
	full := hints([][2]string{{"h/l", "tabs"}, {"j/k", "scroll"}, {"esc", "close"}})
	if lipgloss.Width(full) <= width {
		return full
	}
	short := hints([][2]string{{"esc", "close"}, {"h/l", "tabs"}})
	if lipgloss.Width(short) <= width {
		return short
	}
	return fit(hints([][2]string{{"esc", "close"}}), width, 1)
}

// helpScrollBar draws the scrollbar column so the body shows where the window
// sits in the topic instead of a marker line eating a content row.
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

// helpModal builds the content of the frame for the current page: the title row,
// the topic strip, one window of body rows and the hint row. Every returned line
// already fits the content area, and there are never more of them than the frame
// can draw.
func (m model) helpModal(w, h int) (mw, mh int, content []string) {
	mw, mh, _ = helpFrame(w, h)
	if mw < 12 || mh < 6 {
		// Below this the frame cannot hold a title, a strip, one body row and
		// the hint row, so the overlay steps out of the way entirely.
		return mw, mh, nil
	}
	page := m.helpPageIndex()
	inner := mw - 4
	_, bodyRows, bodyWidth, lines := m.helpBody(w, h)
	// A narrower body is the scrollbar column, which only appears when the
	// topic is taller than the window.
	scrolling := bodyWidth < inner
	total := len(lines)
	start := min(max(0, m.helpScroll), max(0, total-bodyRows))
	header := accentStyle.Render("HELP") + mutedStyle.Render(fmt.Sprintf("  Page %d / %d", page+1, len(helpTopics)))
	content = append(content, fit(header, inner, 1), m.helpTabs(inner))
	for row := 0; row < bodyRows; row++ {
		line := ""
		if i := start + row; i < total {
			line = fit(lines[i], bodyWidth, 1)
		}
		if scrolling {
			line = padHelp(line, bodyWidth) + " " + helpScrollBar(row, bodyRows, start, total)
		}
		content = append(content, line)
	}
	content = append(content, m.helpHintRow(inner))
	return mw, mh, content
}

func (m model) helpView(background string, w, h int) string {
	mw, mh, content := m.helpModal(w, h)
	if content == nil {
		return fit(background, w, h)
	}
	modal := focusedPanel(strings.Join(content, "\n"), mw, mh, true)
	bg := strings.Split(background, "\n")
	box := strings.Split(modal, "\n")
	x, y := (w-mw)/2, (h-mh)/2
	result := make([]string, max(0, h))
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
