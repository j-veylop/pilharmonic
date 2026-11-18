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
	title string
	// short labels the topic strip when the full titles do not fit. It stays
	// recognizable so the menu never degrades to bare topic numbers.
	short    string
	intro    []string
	sections []helpSection
	note     []string
}

// helpSection groups related shortcuts under a heading. An empty heading
// continues the previous group without a label. topic names the key table
// bindings listed before rows.
type helpSection struct {
	heading string
	topic   string
	rows    []helpRow
}

// helpRow is one shortcut and its description.
type helpRow struct {
	keys string
	desc string
}

func (s helpSection) keys() []helpRow {
	var rows []helpRow
	for _, b := range boardKeys {
		if b.topic != "" && b.topic == s.topic && b.help != "" {
			rows = append(rows, helpRow{b.key, b.help})
		}
	}
	return append(rows, s.rows...)
}

var helpTopics = []helpTopic{
	{
		title: "Concepts",
		short: "Con",
		sections: []helpSection{{rows: []helpRow{
			{"Request", "Work typed into the request box (/). One compiled-in authorization step decides acceptance and records its audit evidence."},
			{"Task", "Accepted work with its own mandate, executor, status and history."},
			{"Task number", "A task's one-based place in the task list; children are numbered beneath their parent and narrow layouts show the same number as TASK n/N."},
			{"Route", "The automatic acceptance step that picks an executor."},
			{"Held", "A task kept queued with Ctrl-s so o can set it up before r starts it."},
			{"Action", "Tasks that need you: blocked, waiting, awaiting review, undelivered input, merge failed, failed or interrupted."},
			{"Attempt", "One run of a task. Retry keeps the task and adds an attempt."},
			{"Skill", "Operator instructions an agent loads on demand; /skills enables or disables one for the next attempt."},
			{"Language check", "Recognized Go, Node/TypeScript, Rust and Python toolchains run after edits; /lsp toggles them."},
			{"Usage", "Prompt, output and cache tokens with estimated cost per attempt, recorded as each model call finishes; a main-view task row totals that task and its subtasks."},
			{"Review", "v opens the exact prepared commit diff; y approves and merges it, n rejects with a reason, and Esc closes. During review, Ctrl-o opens the review task workspace in nvim; Ctrl-g opens lazygit. Ctrl-t opens a terminal in the worktree, on the board or while reviewing, so the project's own checks can run there before approving."},
		}}},
	},
	{
		title: "Navigation",
		short: "Nav",
		sections: []helpSection{{topic: "Navigation", rows: []helpRow{
			{"h / l", "Focus tasks / details"},
			{"j / k", "Move down / up; counts: 5j, 3k; selects a call in focused ACTIVITY"},
			{"gg / G", "First / last task or top / bottom"},
			{"Ctrl-u / d", "Half page up / down"},
			{"PgUp / PgDn", "Page details"},
			{"Mouse wheel", "Scroll details"},
		}}},
	},
	{
		title:    "Tasks",
		short:    "Task",
		sections: []helpSection{{topic: "Tasks"}},
	},
	{
		title:    "Details",
		short:    "Det",
		sections: []helpSection{{topic: "Details"}},
	},
	{
		title: "Commands",
		short: "Cmd",
		intro: []string{
			"Type these into the REQUEST box; they are never submitted as a request. Arguments follow the command name, as in /login <provider> or /model [big|small]. Attached files are read once at submission and appended to the request as text.",
			"Use /model <agent> to choose big, small, or a custom model from a configured provider.",
			"For ChatGPT subscription access, run /login openai subscription. This runs the installed codex login browser flow; then use /model big and choose openai-subscription.",
		},
		sections: []helpSection{{
			heading: "SLASH COMMANDS",
			rows: []helpRow{
				{"/login", "Pick a provider: OpenAI API key or ChatGPT subscription"},
				{"/logout", "Forget a provider's stored key"},
				{"/providers", "List providers and whether a key is stored"},
				{"/model", "Choose the models, or open the menu"},
				{"/skills", "Toggle skills globally or per agent (Tab switches)"},
				{"/lsp", "Enable or disable recognized language checks for this workspace"},
				{"/attach", "Attach a local file to the next request (/detach removes it)"},
				{"/theme", "Cycle or set the palette: auto, dark or light"},
				{"/help", "Open this help"},
			},
		}},
	},
	{
		title: "Editing",
		short: "Edi",
		intro: []string{
			"The request box always stays docked above the footer hints; i focuses it and Esc leaves it without hiding the draft. Every composer reuses the same box, and the draft wraps inside it and grows to six rows.",
		},
		sections: []helpSection{{rows: []helpRow{
			{"Enter", "Submit; the request box routes and starts the task"},
			{"Ctrl-s", "Request box: hold the task queued for setup"},
			{"Ctrl-o", "Edit the draft in $EDITOR"},
			{"Ctrl-j", "Insert a newline (also Alt-enter)"},
			{"Tab", "Complete a leading @executor in the request box"},
			{"Esc", "Cancel input; the request draft is kept"},
			{"Ctrl-w", "Delete previous word (also Alt-backspace)"},
			{"Alt-d", "Delete next word"},
			{"Ctrl-k / u", "Delete to line end / line start"},
			{"Ctrl-d", "Delete next character (also Delete)"},
			{"Left / Right", "One character (also Ctrl-b / Ctrl-f)"},
			{"Ctrl-← / →", "One word (also Alt-b / Alt-f)"},
			{"Up / Down", "One wrapped line (also Ctrl-p / Ctrl-n)"},
			{"Home / End", "Line start / line end (also Ctrl-a / Ctrl-e)"},
			{"PgUp / PgDn", "Move through long input"},
		}}},
		note: []string{"Word deletion stays on the line you are editing."},
	},
	{
		title: "General",
		short: "Gen",
		sections: []helpSection{{topic: "General", rows: []helpRow{
			{"?", "Open / close this help (not while typing)"},
			{"F1", "Open / close this help from any screen"},
			{"j/k or ↑↓", "Move in the setup popup and menus; Enter chooses, Esc goes back"},
			{"Drag details", "Select text; release copies via OSC 52"},
			{"Paste", "Use the terminal shortcut in composers; a paste never answers a prompt"},
		}}},
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
)

// helpKeyColumn is the description column for every topic: the widest shortcut
// in the overlay plus a gutter, so descriptions never slide between topics.
func helpKeyColumn() int {
	widest := 0
	for _, topic := range helpTopics {
		for _, section := range topic.sections {
			for _, row := range section.keys() {
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
		for _, row := range section.keys() {
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

// helpTabs draws the topic strip with full titles when they fit, compact
// labels when they do not, and positions when even those overflow; the title
// row still names the topic. Labels are kept when at all possible because a
// strip of bare numbers does not say what each topic is.
func helpTabs(width, page int) string {
	titles, shorts, numbers := make([]string, len(helpTopics)), make([]string, len(helpTopics)), make([]string, len(helpTopics))
	for i, topic := range helpTopics {
		titles[i], shorts[i], numbers[i] = topic.title, topic.short, strconv.Itoa(i+1)
	}
	for _, labels := range [][]string{titles, shorts, numbers} {
		row := ""
		for i, label := range labels {
			if i > 0 {
				row += " "
			}
			row += selectionButton(label, i == page)
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
	return mw, mh, append(content, "", fitHints(inner, "esc close", "←/→ topics", "↑/↓ scroll"))
}

func (m model) helpView(background string, w, h int) string {
	mw, mh, content := m.helpModal(w, h)
	return modalView(background, w, h, mw, mh, content)
}

// textEntryFocused reports whether the active surface owns printable keys, so
// ? stays text in a draft, an API key or a custom model ID. F1 always opens help.
func (m model) textEntryFocused() bool {
	return m.login.provider != "" || m.input.kind != "" ||
		m.router.open ||
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
