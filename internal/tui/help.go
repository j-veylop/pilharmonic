package tui

import (
	"fmt"
	"strconv"
	"strings"

	keys "github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// A help topic is structured rather than pre-formatted, so one definition feeds
// both projections: helpLines projects it to plain text, which the tests and the
// slash-command test read, and the overlay projects the same lines to styled
// cells. Nothing is inferred from indentation, so a description can wrap without
// being mistaken for a heading.
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
	// helpMaxWidth and helpMaxHeight bound the modal on a generous terminal.
	helpMaxWidth  = 76
	helpMaxHeight = 28
	// helpKeyGutter is the breathing room between the shortcut column and its
	// description column.
	helpKeyGutter = 3
	// helpHangIndent is the hanging indent used when the frame is too narrow for
	// two columns: the keys take one line and the description hangs below them.
	helpHangIndent = 2
	// helpMinDescCols is the narrowest description column worth aligning to.
	helpMinDescCols = 12
	// helpBodyIndent is the left margin every body line shares, so the text never
	// touches the frame.
	helpBodyIndent = 1
	// helpScrollGutter is the column reserved for the scrollbar. It is reserved
	// on every topic, not just the long ones, so the text keeps one column and
	// never re-wraps when the operator switches topics.
	helpScrollGutter = 2
	// helpChromeRows is everything the frame draws around the body: the two
	// border rows, the title row, the topic strip, the gap under the header, the
	// gap above the hint row and the hint row itself. A frame too narrow for a
	// strip does not reserve its row either.
	helpChromeRows       = 7
	helpChromeRowsNarrow = 6
	// helpHeadingRule is the gap between a section heading and its rule.
	helpHeadingRule = 1
	// helpActiveTabMarker precedes the topic the operator is reading, so the
	// strip still marks it where the terminal cannot show the accent colour.
	// helpInactiveTabLead keeps every other tab exactly as wide as the marked
	// one, so the strip never shifts when the active topic changes, and
	// helpTabGap keeps a label from running into the next tab.
	helpActiveTabMarker = "▸"
	helpInactiveTabLead = " "
	helpTabGap          = " "
	// helpHintSeparator joins the footer hints drawn by the help bubble.
	helpHintSeparator = " · "
	helpHintEllipsis  = "…"
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
// is indented to the same column.
type helpLine struct {
	kind helpLineKind
	keys string
	text string
}

// helpLayout is a topic projected onto a width: the measured key column plus the
// lines it renders to. Both projections walk it in order, so the plain text the
// tests assert on and the styled cells on screen can never disagree.
type helpLayout struct {
	width    int
	keyWidth int
	lines    []helpLine
}

// topicKeyWidth is the widest shortcut one topic lists.
func topicKeyWidth(topic helpTopic) int {
	widest := 0
	for _, section := range topic.sections {
		for _, row := range section.rows {
			if row.keys != "" {
				widest = max(widest, lipgloss.Width(row.keys))
			}
		}
	}
	return widest
}

// helpKeyColumn is the column every topic aligns its descriptions to: the widest
// shortcut anywhere in the overlay, plus its gutter. One column for the whole
// overlay means a description never slides sideways when the operator changes
// topic.
func helpKeyColumn() int {
	widest := 0
	for _, topic := range helpTopics {
		widest = max(widest, topicKeyWidth(topic))
	}
	if widest == 0 {
		return 0
	}
	return widest + helpKeyGutter
}

// helpShortcutColumns decides how a topic lays out its shortcut column at width:
// the shared column when it leaves a usable description column, a hanging indent
// when it does not, and no column at all for a topic written in prose.
func helpShortcutColumns(topic helpTopic, width int) (keyWidth int, stacked bool) {
	if topicKeyWidth(topic) == 0 {
		return 0, false
	}
	// A topic defined outside the overlay may list a shortcut wider than the
	// shared column; it widens its own column instead of pushing the key into
	// its description.
	keyWidth = max(helpKeyColumn(), topicKeyWidth(topic)+helpKeyGutter)
	if width-keyWidth >= helpMinDescCols {
		return keyWidth, false
	}
	return helpHangIndent, true
}

// layoutHelpTopic wraps a topic to width. Descriptions align in one column and
// wrapped continuations hang under that column; on a frame too narrow for two
// columns each key takes its own line and the description hangs two cells in.
func layoutHelpTopic(topic helpTopic, width int) helpLayout {
	width = max(1, width)
	layout := helpLayout{width: width}
	var stacked bool
	layout.keyWidth, stacked = helpShortcutColumns(topic, width)
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
			// A heading is a label, not a paragraph, but it still wraps rather
			// than leaving the frame: the rule follows each part that does not
			// fill the width on its own.
			for _, part := range wrapHelp(section.heading, width) {
				add(helpLine{kind: helpHeadingLine, text: part})
			}
		}
		for _, row := range section.rows {
			if row.keys == "" {
				for _, part := range wrapHelp(row.desc, width) {
					add(helpLine{kind: helpProseLine, text: part})
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
// descriptions in ink, headings as a labelled rule and prose muted.
func (l helpLayout) styled() []string {
	out := make([]string, 0, len(l.lines))
	for _, line := range l.lines {
		switch line.kind {
		case helpHeadingLine:
			out = append(out, l.heading(line.text))
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
			if line.text == "" {
				out = append(out, "")
				continue
			}
			out = append(out, mutedStyle.Render(line.text))
		}
	}
	return out
}

// heading draws a section heading as a label followed by a faint rule that
// reaches the body width, so a section reads as a divider between groups
// instead of as another shortcut.
func (l helpLayout) heading(text string) string {
	label := helpHeadingStyle.Render(text)
	ruleCols := l.width - lipgloss.Width(text) - helpHeadingRule
	if ruleCols < 1 {
		return label
	}
	return label + strings.Repeat(" ", helpHeadingRule) + helpTrackStyle.Render(strings.Repeat("─", ruleCols))
}

func (m model) helpKey(key string) (model, tea.Cmd) {
	switch key {
	case "ctrl+c":
		return m.requestQuit(), nil
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
	return m, nil
}

func (m model) helpLines(width int) []string {
	page := max(0, min(len(helpTopics)-1, m.help.page))
	return layoutHelpTopic(helpTopics[page], max(1, width)).plain()
}

func (m *model) scrollHelp(delta int) {
	page := max(0, min(len(helpTopics)-1, m.help.page+delta))
	if page == m.help.page {
		// Already at that end of the strip: keep the line the operator is on
		// rather than jumping them back to the top of the topic.
		return
	}
	m.help.page, m.help.scroll = page, 0
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
	if bodyRows < 1 {
		m.help.scroll = 0
		return
	}
	m.help.scroll = max(0, min(max(0, len(lines)-bodyRows), m.help.scroll+delta))
}

// helpPageIndex is the clamped topic the overlay shows.
func (m model) helpPageIndex() int {
	return max(0, min(len(helpTopics)-1, m.help.page))
}

// helpContentWidth is the frame width minus its borders.
func helpContentWidth(mw int) int { return max(1, mw-4) }

// helpBodyWidth is the width a topic is wrapped to inside a frame of mw cells.
// The body indent and the scrollbar gutter are always subtracted, whether or not
// the topic scrolls, so a topic keeps its line breaks from page to page.
func helpBodyWidth(mw int) int {
	return max(1, helpContentWidth(mw)-helpBodyIndent-helpScrollGutter)
}

// helpTabTitles and helpTabNumbers are the two label sets the strip can use.
func helpTabTitles() []string {
	titles := make([]string, len(helpTopics))
	for i, topic := range helpTopics {
		titles[i] = topic.title
	}
	return titles
}

func helpTabNumbers() []string {
	numbers := make([]string, len(helpTopics))
	for i := range helpTopics {
		numbers[i] = strconv.Itoa(i + 1)
	}
	return numbers
}

// helpTabRow renders the strip for one page. Every tab reserves the marker
// column the active one uses, so the row is exactly as wide whichever topic is
// active and the strip never shifts under the operator's eyes.
func helpTabRow(labels []string, page int) string {
	tabs := make([]string, 0, len(labels))
	for i, label := range labels {
		if i == page {
			tabs = append(tabs, activeTabStyle.Render(helpActiveTabMarker+label)+helpTabGap)
			continue
		}
		tabs = append(tabs, inactiveTabStyle.Render(helpInactiveTabLead+label)+helpTabGap)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
}

// helpTabLabels picks the label set for a content width: topic titles when they
// fit, positions when they do not, and nothing when even those overflow, because
// the title row already names the current topic. The choice depends on the width
// alone, so paging never swaps the strip for a different one.
func helpTabLabels(inner int) []string {
	for _, labels := range [][]string{helpTabTitles(), helpTabNumbers()} {
		if lipgloss.Width(helpTabRow(labels, 0)) <= inner {
			return labels
		}
	}
	return nil
}

// helpBody projects the current page for a terminal of w by h, returning the
// frame width, the window rows, the width the body is wrapped to and the styled
// lines. The overlay and the scroll clamp both read it, so keyboard scrolling
// always reaches the last line the overlay draws.
func (m model) helpBody(w, h int) (mw, bodyRows, bodyWidth int, lines []string) {
	mw, _, bodyRows = helpFrame(w, h)
	return mw, bodyRows, helpBodyWidth(mw), m.helpBodyStyled(mw)
}

// helpBodyStyled projects the current page inside a frame of mw cells.
func (m model) helpBodyStyled(mw int) []string {
	return layoutHelpTopic(helpTopics[m.helpPageIndex()], helpBodyWidth(mw)).styled()
}

// helpFrame returns the modal size and the number of body rows that fit inside
// it for a terminal of w by h cells. The title row, the topic strip, the gap
// under it and the hint row sit around the body. The frame is only as tall as
// the tallest topic needs, measured for this width, so a short topic does not sit
// on a slab of empty rows and switching topics never resizes the modal.
func helpFrame(w, h int) (mw, mh, bodyRows int) {
	mw = min(helpMaxWidth, max(4, w-4))
	ceiling := min(helpMaxHeight, max(3, h-2))
	bodyWidth := helpBodyWidth(mw)
	tallest := 1
	for _, topic := range helpTopics {
		tallest = max(tallest, len(layoutHelpTopic(topic, bodyWidth).lines))
	}
	chrome := helpChromeRowsNarrow
	if helpTabLabels(helpContentWidth(mw)) != nil {
		chrome = helpChromeRows
	}
	if ceiling <= chrome {
		// Not even one body row fits between the header and the hint row, so
		// there is nothing for the overlay to draw.
		return mw, ceiling, 0
	}
	bodyRows = min(tallest, ceiling-chrome)
	return mw, bodyRows + chrome, bodyRows
}

// helpTabs draws the topic strip: the active topic carries a marker and the
// accent style, the others are muted. When the frame is too narrow for any strip
// there is none, and the title row carries the topic name on its own.
func (m model) helpTabs(width int) string {
	if labels := helpTabLabels(width); labels != nil {
		return helpTabRow(labels, m.helpPageIndex())
	}
	return ""
}

// helpHintRow keeps the footer hints inside the modal. The bubbles help view
// renders the row and marks a truncated tail with an ellipsis, and a hint is
// dropped whole when the rendered row would still overflow the frame, so a
// narrow frame never has to cut a label in half.
func (m model) helpHintRow(width int) string {
	view := helpHints
	view.Width = max(1, width)
	bindings := helpHintBindings()
	row := view.ShortHelpView(bindings)
	for len(bindings) > 1 && lipgloss.Width(row) > width {
		bindings = bindings[:len(bindings)-1]
		row = view.ShortHelpView(bindings)
	}
	return fit(row, width, 1)
}

// helpHintBindings are the overlay's own keys, most useful first so a narrow
// frame truncates the tail rather than the way out.
func helpHintBindings() []keys.Binding {
	return []keys.Binding{
		keys.NewBinding(keys.WithKeys("esc", "?", "f1", "q"), keys.WithHelp("esc", "close")),
		keys.NewBinding(keys.WithKeys("h", "l"), keys.WithHelp("h/l", "topics")),
		keys.NewBinding(keys.WithKeys("j", "k"), keys.WithHelp("j/k", "scroll")),
	}
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

// helpTitleRow names the overlay and the topic being read, and puts the position
// against the right edge where it stays put while the topics page past.
func (m model) helpTitleRow(width int) string {
	page := m.helpPageIndex()
	left := accentStyle.Render("HELP") + faintStyle.Render(helpHintSeparator) + mutedStyle.Render(helpTopics[page].title)
	right := faintStyle.Render(fmt.Sprintf("%d/%d", page+1, len(helpTopics)))
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return fit(left, width, 1)
	}
	return left + strings.Repeat(" ", gap) + right
}

// helpModal builds the content of the frame for the current page: the title row,
// the topic strip, the gap under it, one window of body rows, the gap above the
// hint row and the hint row itself. Every returned line already fits the content
// area, and there are never more of them than the frame can draw.
func (m model) helpModal(w, h int) (mw, mh int, content []string) {
	mw, mh, bodyRows := helpFrame(w, h)
	if mw < 12 || bodyRows < 1 {
		// Below this the frame cannot hold a title, the header, a body row and
		// the hint row, so the overlay steps out of the way entirely.
		return mw, mh, nil
	}
	inner := helpContentWidth(mw)
	bodyWidth := helpBodyWidth(mw)
	lines := m.helpBodyStyled(mw)
	total := len(lines)
	start := min(max(0, m.help.scroll), max(0, total-bodyRows))
	content = append(content, m.helpTitleRow(inner))
	if strip := m.helpTabs(inner); strip != "" {
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
	content = append(content, "", m.helpHintRow(inner))
	return mw, mh, content
}

func (m model) helpView(background string, w, h int) string {
	mw, mh, content := m.helpModal(w, h)
	if content == nil {
		return fit(background, w, h)
	}
	modal := focusedPanel(strings.Join(content, "\n"), mw, mh, true)
	return overlay(background, modal, w, h, mw, mh)
}

// Help is the one surface an operator can reach from every screen. F1 opens it
// wherever they are, including inside a text field; ? opens it on every surface
// where the character is not part of the draft being typed.
//
// The overlay never clears the state underneath it, so closing help returns to
// the board, request form, composer, picker or menu exactly as it was left.

// textEntryFocused reports whether the active surface is a text field that
// owns printable characters. Help must not steal ? from a draft, from an API
// key prompt or from a custom model ID.
func (m model) textEntryFocused() bool {
	if m.login.provider != "" {
		return true
	}
	if m.input.kind != "" {
		return true
	}
	if m.router.open && m.router.focus == routerFocusComposer {
		return true
	}
	if m.models.menu && m.models.step == modelStepCustom {
		return true
	}
	return false
}

// helpShortcut reports whether msg opens the help overlay on the current
// surface. Pasted text is never a shortcut, even when it is exactly "?".
func (m model) helpShortcut(msg tea.KeyMsg) bool {
	if msg.Paste {
		return false
	}
	switch msg.String() {
	case "f1":
		return true
	case "?":
		return !m.textEntryFocused()
	}
	return false
}

// openHelp shows the overlay above whatever is on screen. Navigation starts
// over so the shortcut always lands on the first topic.
func (m model) openHelp() model {
	m.help.open = true
	m.help.page, m.help.scroll = 0, 0
	m.quitConfirm = false
	m.vimG, m.vimCount = false, 0
	return m
}
