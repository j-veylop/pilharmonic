package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestHelpModalCapturesInputAndScrolls(t *testing.T) {
	m := model{width: 80, height: 14, help: helpState{open: true}}
	view := m.View()
	if !strings.Contains(view, "HELP") {
		t.Fatal(view)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = next.(model)
	if m.input.kind != "" {
		t.Fatal("help key started task composer")
	}
	next, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	m = next.(model)
	if m.help.scroll == 0 {
		t.Fatal("help wheel did not scroll")
	}
	m.help.scroll = 0
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(model)
	if m.help.scroll == 0 {
		t.Fatal("help did not scroll from keyboard")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = next.(model)
	m.scrollHelpBody(1000)
	if !strings.Contains(m.View(), "Select in executor") {
		t.Fatal("help cannot reach last shortcut")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if m.help.open {
		t.Fatal("escape did not close help")
	}
}
func TestHelpModalBounds(t *testing.T) {
	for _, size := range [][2]int{{30, 10}, {50, 20}, {80, 24}, {120, 40}} {
		m := model{width: size[0], height: size[1], help: helpState{open: true}}
		for _, offset := range []int{0, 1000} {
			m.help.page = offset
			view := m.View()
			if lipgloss.Width(view) > size[0] || lipgloss.Height(view) > size[1] {
				t.Fatalf("overflow at %v", size)
			}
		}
	}
}

func TestHelpPagination(t *testing.T) {
	m := model{width: 80, height: 24, help: helpState{open: true}}
	if title := ansi.Strip(m.View()); !strings.Contains(title, "HELP · Concepts") || !strings.Contains(title, "1/7") {
		t.Fatal("missing topic title and position")
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = next.(model)
	if m.help.page != 1 {
		t.Fatal("right did not advance exactly one page")
	}
	if title := ansi.Strip(m.View()); !strings.Contains(title, "HELP · Navigation") || !strings.Contains(title, "2/7") {
		t.Fatalf("right did not page the title row: %q", title)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = next.(model)
	if m.help.page != 0 {
		t.Fatal("page up")
	}
	m.scrollHelp(-1)
	if m.help.page != 0 {
		t.Fatal("wrapped before first page")
	}
	m.scrollHelp(10000)
	last := m.help.page
	m.scrollHelp(1)
	if m.help.page != last {
		t.Fatal("wrapped past last page")
	}
	if !strings.Contains(m.View(), "Select in executor") {
		t.Fatal("final page lost final shortcut")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyHome})
	m = next.(model)
	if m.help.page != 0 {
		t.Fatal("home")
	}
}

// plainHelp projects the current help topic to plain text, matching the
// overlay's layout without the styling.
func plainHelp(m model, width int) []string {
	page := max(0, min(len(helpTopics)-1, m.help.page))
	return layoutHelpTopic(helpTopics[page], max(1, width)).plain()
}

func TestHelpTopicsAreLogicalAndIndependent(t *testing.T) {
	m := model{width: 80, height: 24, help: helpState{open: true}}
	concepts := strings.Join(plainHelp(m, 70), "\n")
	if !strings.Contains(concepts, "Accepted work") || !strings.Contains(concepts, "need you") || strings.Contains(concepts, "Focus tasks") {
		t.Fatal("concepts missing or contains another topic")
	}
	m.scrollHelp(1)
	navigation := strings.Join(plainHelp(m, 70), "\n")
	if !strings.Contains(navigation, "Focus tasks") || strings.Contains(navigation, "New task") {
		t.Fatal("navigation contains another topic")
	}
	m.scrollHelp(1)
	tasks := strings.Join(plainHelp(m, 70), "\n")
	if !strings.Contains(tasks, "New task") || strings.Contains(tasks, "Toggle tool activity") {
		t.Fatal("tasks contains another topic")
	}
	m.scrollHelp(1)
	if !strings.Contains(strings.Join(plainHelp(m, 70), "\n"), "Toggle tool activity") {
		t.Fatal("activity missing")
	}
	m.scrollHelpBody(5)
	m.scrollHelp(1)
	if m.help.scroll != 0 {
		t.Fatal("topic switch retained old scroll")
	}
	if !strings.Contains(m.helpTabs(70), "Navigation") || !strings.Contains(m.helpTabs(70), "General") {
		t.Fatal("horizontal tabs missing")
	}
}

// No topic may draw outside the modal it opened in, at any size the interface
// supports and at either end of the scroll range.
func TestHelpEveryTopicFitsTheModal(t *testing.T) {
	sizes := [][2]int{{30, 10}, {40, 15}, {50, 20}, {60, 20}, {80, 24}, {120, 40}}
	for _, size := range sizes {
		for page, topic := range helpTopics {
			for _, scroll := range []int{0, 1000} {
				m := model{width: size[0], height: size[1], help: helpState{open: true, page: page, scroll: scroll}}
				mw, mh, content := m.helpModal(size[0], size[1])
				if mw > size[0] || mh > size[1] {
					t.Fatalf("%s at %v: modal %dx%d leaves the screen", topic.title, size, mw, mh)
				}
				if content == nil {
					t.Fatalf("%s at %v: help drew no frame", topic.title, size)
				}
				if len(content) > mh-2 {
					t.Fatalf("%s at %v: %d content rows in a %d row frame", topic.title, size, len(content), mh)
				}
				for row, line := range content {
					if lipgloss.Width(line) > mw-4 {
						t.Fatalf("%s at %v: content row %d is %d cells wide, frame allows %d: %q",
							topic.title, size, row, lipgloss.Width(line), mw-4, ansi.Strip(line))
					}
				}
				view := m.View()
				if lipgloss.Width(view) > size[0] || lipgloss.Height(view) > size[1] {
					t.Fatalf("%s at %v: rendered %dx%d", topic.title, size, lipgloss.Width(view), lipgloss.Height(view))
				}
				for row, line := range strings.Split(view, "\n") {
					if lipgloss.Width(line) > size[0] {
						t.Fatalf("%s at %v: row %d is %d cells wide: %q", topic.title, size, row, lipgloss.Width(line), ansi.Strip(line))
					}
				}
				if plain := ansi.Strip(view); !strings.Contains(plain, "HELP") {
					t.Fatalf("%s at %v: the frame lost its title: %q", topic.title, size, plain)
				}
			}
		}
	}
}

// A description longer than the description column wraps onto continuation lines
// that hang under the column instead of being clipped.
func TestHelpLongDescriptionWrapsWithoutClipping(t *testing.T) {
	const desc = "Approval merges the prepared worktree commit and records the review evidence, so a rejected task keeps its worktree for a follow-up attempt instead of losing it."
	topic := helpTopic{
		title:    "Long",
		sections: []helpSection{{heading: "HEADING", rows: []helpRow{{keys: "Ctrl-x", desc: desc}}}},
	}
	for _, width := range []int{18, 24, 36, 60, 100} {
		layout := layoutHelpTopic(topic, width)
		plain := layout.plain()
		for i, line := range plain {
			if lipgloss.Width(line) > width {
				t.Fatalf("width %d: line %d is %d cells: %q", width, i, lipgloss.Width(line), line)
			}
		}
		for i, line := range layout.styled() {
			if lipgloss.Width(line) > width {
				t.Fatalf("width %d: styled line %d overflows: %q", width, i, ansi.Strip(line))
			}
		}
		// Every word survives the wrap.
		var kept []string
		for _, line := range layout.lines {
			if line.kind == helpRowLine && line.text != "" {
				kept = append(kept, line.text)
			}
		}
		if got, want := strings.Join(strings.Fields(strings.Join(kept, " ")), " "), strings.Join(strings.Fields(desc), " "); got != want {
			t.Fatalf("width %d: wrapping dropped text:\n got %q\nwant %q", width, got, want)
		}
		if lines := len(plain); lines < 3 {
			t.Fatalf("width %d: long description did not wrap: %q", width, plain)
		}
	}
}

// Descriptions line up in one column, and a wrapped continuation hangs under
// that column rather than under the keys.
func TestHelpKeyColumnAlignmentIsStable(t *testing.T) {
	cell := func(s string, at int) string { return ansi.Cut(s, at, at+1) }
	for _, width := range []int{40, 72, 100} {
		for _, topic := range helpTopics {
			layout := layoutHelpTopic(topic, width)
			if layout.keyWidth == 0 {
				continue // prose-only topic: there is no column to align
			}
			plain := layout.plain()
			rows := 0
			for i, line := range layout.lines {
				if line.kind != helpRowLine || line.text == "" {
					continue
				}
				rows++
				if line.keys == "" {
					if pad := ansi.Cut(plain[i], 0, layout.keyWidth); pad != strings.Repeat(" ", layout.keyWidth) {
						t.Fatalf("width %d %s: continuation is not indented to the key column: %q", width, topic.title, plain[i])
					}
				}
				if got := cell(plain[i], layout.keyWidth); got != cell(line.text, 0) {
					t.Fatalf("width %d %s: description does not start in the key column (col %d) of %q",
						width, topic.title, layout.keyWidth, plain[i])
				}
			}
			if rows == 0 {
				t.Fatalf("width %d %s: layout produced no rows", width, topic.title)
			}
		}
	}
}

// Headings are styled apart from shortcut rows so a section reads as a label
// rather than as another key.
func TestHelpSectionHeadingsAreDistinctFromShortcutRows(t *testing.T) {
	// Styles render without escape codes when the test terminal has no colour,
	// so the palettes are compared directly as well as through the projection.
	if helpHeadingStyle.GetForeground() == keyStyle.GetForeground() || helpHeadingStyle.GetForeground() == helpDescStyle.GetForeground() {
		t.Fatal("section headings share a colour with the shortcut rows")
	}
	if keyStyle.GetForeground() == helpDescStyle.GetForeground() {
		t.Fatal("shortcut keys and descriptions share a colour")
	}
	if !helpHeadingStyle.GetBold() || !keyStyle.GetBold() {
		t.Fatal("headings and shortcut keys must both be emphasised")
	}
	headings := map[string]string{
		"COMPOSERS & PICKERS": "General",
		"SLASH COMMANDS":      "Commands",
	}
	for _, topic := range helpTopics {
		layout := layoutHelpTopic(topic, 72)
		styled := layout.styled()
		for i, line := range layout.lines {
			switch line.kind {
			case helpHeadingLine:
				want := helpHeadingStyle.Render(line.text) + strings.Repeat(" ", helpHeadingRule)
				if !strings.HasPrefix(styled[i], want) {
					t.Fatalf("%s: heading %q is not drawn with the heading style: %q", topic.title, line.text, ansi.Strip(styled[i]))
				}
				if !strings.HasPrefix(styled[i], want+helpTrackStyle.Render("─")) {
					t.Fatalf("%s: heading %q lost its rule: %q", topic.title, line.text, ansi.Strip(styled[i]))
				}
				if got := lipgloss.Width(styled[i]); got != 72 {
					t.Fatalf("%s: heading rule ends at %d cells, not at the body width: %q", topic.title, got, ansi.Strip(styled[i]))
				}
				if strings.HasPrefix(ansi.Strip(styled[i]), " ") {
					t.Fatalf("%s: heading %q is indented like a description", topic.title, line.text)
				}
				delete(headings, line.text)
			case helpRowLine:
				if line.keys == "" {
					continue
				}
				if !strings.HasPrefix(ansi.Strip(styled[i]), line.keys) {
					t.Fatalf("%s: keys %q left the key column: %q", topic.title, line.keys, ansi.Strip(styled[i]))
				}
			}
		}
	}
	for heading, topic := range headings {
		t.Fatalf("%s lost its %q heading", topic, heading)
	}
}

// The strip marks the active topic, mutes the others and falls back to topic
// positions, then to nothing, as the frame narrows.
func TestHelpTopicStripMarksTheActiveTopic(t *testing.T) {
	inactive := func(title string) string {
		return inactiveTabStyle.Render(helpInactiveTabLead+title) + helpTabGap
	}
	if activeTabStyle.GetForeground() == inactiveTabStyle.GetForeground() && activeTabStyle.GetUnderline() == inactiveTabStyle.GetUnderline() {
		t.Fatal("the active topic is not styled apart from the inactive ones")
	}
	m := model{width: 80, height: 24, help: helpState{open: true}}
	strip := m.helpTabs(72)
	if !strings.Contains(strip, helpActiveTabMarker+"Concepts") {
		t.Fatalf("strip does not mark the active topic: %q", ansi.Strip(strip))
	}
	if strings.Contains(strip, helpActiveTabMarker+"Navigation") {
		t.Fatalf("strip marks more than one topic: %q", ansi.Strip(strip))
	}
	if strings.Count(strip, helpActiveTabMarker) != 1 {
		t.Fatalf("strip has %d active markers: %q", strings.Count(strip, helpActiveTabMarker), ansi.Strip(strip))
	}
	for _, title := range []string{"Navigation", "General"} {
		if !strings.Contains(strip, inactive(title)) {
			t.Fatalf("strip does not mute %q: %q", title, ansi.Strip(strip))
		}
	}
	m.scrollHelp(1)
	if moved := m.helpTabs(72); !strings.Contains(moved, helpActiveTabMarker+"Navigation") || strings.Contains(moved, helpActiveTabMarker+"Concepts") {
		t.Fatalf("strip did not follow the page: %q", ansi.Strip(moved))
	}
	for _, width := range []int{72, 40, 20, 12} {
		narrow := m.helpTabs(width)
		if lipgloss.Width(narrow) > width {
			t.Fatalf("strip is %d cells in a %d cell frame: %q", lipgloss.Width(narrow), width, ansi.Strip(narrow))
		}
	}
	// A frame too narrow for the titles falls back to the topic positions, then
	// to nothing at all: the title row already names the topic it is showing.
	if got := ansi.Strip(m.helpTabs(40)); !strings.Contains(got, helpActiveTabMarker+"2") || strings.Contains(got, "Navigation") {
		t.Fatalf("narrow strip lost its positions: %q", got)
	}
	if got := m.helpTabs(12); got != "" {
		t.Fatalf("a frame with no room for a strip still drew one: %q", ansi.Strip(got))
	}
}

// The hint row stays inside the frame and keeps its most useful hint at every
// width.
func TestHelpHintRowFitsTheFrame(t *testing.T) {
	m := model{width: 80, height: 24, help: helpState{open: true}}
	for _, width := range []int{72, 44, 22, 12} {
		row := m.helpHintRow(width)
		if lipgloss.Width(row) > width {
			t.Fatalf("hint row is %d cells in a %d cell frame: %q", lipgloss.Width(row), width, ansi.Strip(row))
		}
		if !strings.Contains(ansi.Strip(row), "esc close") {
			t.Fatalf("width %d: hint row lost the close hint: %q", width, ansi.Strip(row))
		}
	}
}

// A topic taller than the window shows a scrollbar column inside the frame, and
// the body never grows past the rows the frame reserved for it.
func TestHelpScrollsLongTopicsInsideTheFrame(t *testing.T) {
	m := model{width: 30, height: 10, help: helpState{open: true}}
	_, _, top := m.helpModal(30, 10)
	if !strings.Contains(strings.Join(top, "\n"), helpThumbStyle.Render("▐")) {
		t.Fatalf("overflowing topic has no scroll affordance: %q", ansi.Strip(strings.Join(top, "\n")))
	}
	m.scrollHelpBody(1000)
	_, _, bottom := m.helpModal(30, 10)
	if len(bottom) != len(top) {
		t.Fatalf("scrolling changed the frame height: %d then %d rows", len(top), len(bottom))
	}
	if !strings.Contains(strings.Join(bottom, "\n"), helpThumbStyle.Render("▐")) {
		t.Fatal("the scrollbar disappeared at the end of the topic")
	}
	if strings.Join(top, "\n") == strings.Join(bottom, "\n") {
		t.Fatal("scrolling did not move the body")
	}
	// A topic that fits needs no scrollbar.
	short := model{width: 120, height: 40, help: helpState{open: true, page: 3}}
	_, _, fitted := short.helpModal(120, 40)
	if joined := strings.Join(fitted, "\n"); strings.Contains(joined, "▐") || strings.Contains(joined, "│") {
		t.Fatalf("a topic that fits still draws a scrollbar: %q", ansi.Strip(joined))
	}
}

// The overlay is text on the existing background: no cell may set a background
// colour of its own. The test binary runs without a colour profile, which drops
// every SGR code from a projection, so the styles are also checked directly.
func TestHelpUsesNoBackgroundColour(t *testing.T) {
	styles := []struct {
		name  string
		style lipgloss.Style
	}{
		{"shortcut keys", keyStyle},
		{"descriptions", helpDescStyle},
		{"headings", helpHeadingStyle},
		{"scroll track", helpTrackStyle},
		{"scroll thumb", helpThumbStyle},
		{"active tab", activeTabStyle},
		{"inactive tab", inactiveTabStyle},
		{"title and marker", accentStyle},
		{"muted prose", mutedStyle},
		{"faint rules", faintStyle},
		{"hint key", helpHints.Styles.ShortKey},
		{"hint description", helpHints.Styles.ShortDesc},
		{"hint separator", helpHints.Styles.ShortSeparator},
		{"hint ellipsis", helpHints.Styles.Ellipsis},
	}
	for _, entry := range styles {
		if _, unset := entry.style.GetBackground().(lipgloss.NoColor); !unset {
			t.Fatalf("the %s style sets a background colour", entry.name)
		}
	}
	for page := range helpTopics {
		for _, size := range [][2]int{{30, 10}, {40, 15}, {80, 24}, {120, 40}} {
			m := model{width: size[0], height: size[1], help: helpState{open: true, page: page}}
			if view := m.View(); strings.Contains(view, "\x1b[48;") {
				t.Fatalf("%s at %v: help renders a background colour", helpTopics[page].title, size)
			}
		}
	}
}

// Keyboard scrolling must reach the last line the overlay draws. The scroll
// clamp and the overlay share one geometry, so the tail of a long topic can
// never be stranded behind the frame, including when the scrollbar column
// narrows the body.
func TestHelpScrollReachesTheLastDrawnLine(t *testing.T) {
	for _, size := range [][2]int{{30, 10}, {40, 12}, {40, 15}, {50, 20}, {60, 20}, {80, 24}, {120, 40}} {
		for page, topic := range helpTopics {
			m := model{width: size[0], height: size[1], help: helpState{open: true, page: page}}
			m.scrollHelpBody(1000)
			_, _, _, lines := m.helpBody(size[0], size[1])
			last := ""
			for i := len(lines) - 1; i >= 0; i-- {
				if trimmed := strings.TrimSpace(ansi.Strip(lines[i])); trimmed != "" {
					last = trimmed
					break
				}
			}
			if last == "" {
				continue
			}
			if view := ansi.Strip(m.View()); !strings.Contains(view, last) {
				t.Fatalf("%v %s: last line %q is unreachable\n%s", size, topic.title, last, view)
			}
		}
	}
}

// helpOriginForTest is the screen row and column the overlay's title lands on,
// which pins where the modal itself sits.
func helpOriginForTest(m model) (row, col int) {
	for i, line := range strings.Split(ansi.Strip(m.View()), "\n") {
		if at := strings.Index(line, "HELP"+helpHintSeparator); at >= 0 {
			return i, at
		}
	}
	return -1, -1
}

// Paging must not move the overlay. The frame keeps its size and position, the
// strip keeps its cells, the body keeps its wrap width and one key column serves
// every topic, so nothing the operator is reading slides sideways or rewraps
// when they change topic.
func TestHelpGeometryIsStableAcrossTopics(t *testing.T) {
	for _, size := range [][2]int{{30, 12}, {40, 16}, {60, 20}, {80, 24}, {120, 40}} {
		first := model{width: size[0], height: size[1], help: helpState{open: true}}
		wantMW, wantMH, wantRows := helpFrame(size[0], size[1])
		_, wantBodyRows, wantWidth, _ := first.helpBody(size[0], size[1])
		wantStrip := lipgloss.Width(first.helpTabs(helpContentWidth(wantMW)))
		wantColumn := layoutHelpTopic(helpTopics[0], wantWidth).keyWidth
		wantRow, wantCol := helpOriginForTest(first)
		if wantRow < 0 {
			t.Fatalf("%v: the overlay drew no title row", size)
		}
		for page, topic := range helpTopics {
			m := model{width: size[0], height: size[1], help: helpState{open: true, page: page}}
			mw, mh, rows := helpFrame(size[0], size[1])
			if mw != wantMW || mh != wantMH || rows != wantRows {
				t.Fatalf("%v %s: frame became %dx%d/%d rows, was %dx%d/%d",
					size, topic.title, mw, mh, rows, wantMW, wantMH, wantRows)
			}
			_, bodyRows, bodyWidth, _ := m.helpBody(size[0], size[1])
			if bodyRows != wantBodyRows || bodyWidth != wantWidth {
				t.Fatalf("%v %s: body became %d rows of %d cells, was %d of %d",
					size, topic.title, bodyRows, bodyWidth, wantBodyRows, wantWidth)
			}
			if got := lipgloss.Width(m.helpTabs(helpContentWidth(mw))); got != wantStrip {
				t.Fatalf("%v %s: topic strip is %d cells, not the %d cells of %s",
					size, topic.title, got, wantStrip, helpTopics[0].title)
			}
			if got := layoutHelpTopic(topic, bodyWidth).keyWidth; got != wantColumn {
				t.Fatalf("%v %s: descriptions start in column %d, not the shared column %d",
					size, topic.title, got, wantColumn)
			}
			if row, col := helpOriginForTest(m); row != wantRow || col != wantCol {
				t.Fatalf("%v %s: the overlay moved from row %d, column %d to row %d, column %d",
					size, topic.title, wantRow, wantCol, row, col)
			}
			// Every body row spans the same cells whether or not the topic
			// scrolls, so the scrollbar column never re-wraps the text.
			_, _, content := m.helpModal(size[0], size[1])
			if content == nil {
				t.Fatalf("%v %s: the overlay drew no frame", size, topic.title)
			}
			inner := helpContentWidth(mw)
			for row := len(content) - 2 - bodyRows; row < len(content)-2; row++ {
				if got := lipgloss.Width(content[row]); got != inner {
					t.Fatalf("%v %s: body row %d is %d cells in a %d cell frame: %q",
						size, topic.title, row, got, inner, ansi.Strip(content[row]))
				}
			}
		}
	}
}

// The scrollbar column is reserved on every topic, not just the ones that
// scroll, so a short topic never uses the cells the scrollbar needs and the text
// does not jump sideways when the next topic overflows.
func TestHelpBodyReservesTheScrollbarColumn(t *testing.T) {
	for _, size := range [][2]int{{30, 10}, {40, 15}, {60, 20}, {80, 24}, {120, 40}} {
		for page, topic := range helpTopics {
			m := model{width: size[0], height: size[1], help: helpState{open: true, page: page}}
			mw, bodyRows, bodyWidth, lines := m.helpBody(size[0], size[1])
			inner := helpContentWidth(mw)
			if want := inner - helpBodyIndent - helpScrollGutter; bodyWidth != want {
				t.Fatalf("%v %s: body wraps at %d cells, the reserve leaves %d", size, topic.title, bodyWidth, want)
			}
			if len(lines) <= bodyRows {
				// A topic that fits still keeps the gutter: every drawn row spans
				// the whole content area, and its last cells stay empty.
				_, _, content := m.helpModal(size[0], size[1])
				if content == nil {
					t.Fatalf("%v %s: the overlay drew no frame", size, topic.title)
				}
				body := content[len(content)-2-bodyRows : len(content)-2]
				for row, line := range body {
					if got := lipgloss.Width(line); got != inner {
						t.Fatalf("%v %s: body row %d is %d cells in a %d cell frame: %q",
							size, topic.title, row, got, inner, ansi.Strip(line))
					}
					if tail := ansi.Cut(line, inner-helpScrollGutter+1, inner); strings.TrimSpace(tail) != "" {
						t.Fatalf("%v %s: body row %d uses the scrollbar column: %q",
							size, topic.title, row, ansi.Strip(line))
					}
				}
			}
		}
	}
}

// Every topic aligns its descriptions to one column, and that column is the one
// the overlay measured across all topics: paging never slides a description
// sideways.
func TestHelpDescriptionsShareOneColumn(t *testing.T) {
	for _, size := range [][2]int{{60, 20}, {80, 24}, {120, 40}} {
		mw, _, _ := helpFrame(size[0], size[1])
		want := helpKeyColumn()
		if want == 0 {
			t.Fatal("no topic lists a shortcut")
		}
		for page, topic := range helpTopics {
			m := model{width: size[0], height: size[1], help: helpState{open: true, page: page}}
			_, _, bodyWidth, _ := m.helpBody(size[0], size[1])
			if got := layoutHelpTopic(topic, bodyWidth).keyWidth; got != want {
				t.Fatalf("%v %s: descriptions start in column %d, not the shared column %d",
					size, topic.title, got, want)
			}
		}
		// The narrowest frame the interface supports must still hold that
		// column without breaking a shortcut in two.
		if size[0] == 60 && helpBodyWidth(mw) < want {
			t.Fatalf("a %d cell frame wraps the body to %d cells, narrower than its %d cell key column",
				size[0], helpBodyWidth(mw), want)
		}
	}
	if bodyWidth := helpBodyWidth(min(helpMaxWidth, 30-4)); bodyWidth < helpKeyColumn() {
		t.Fatalf("the narrowest frame wraps the body to %d cells, under the %d cell key column",
			bodyWidth, helpKeyColumn())
	}
}

// On a frame too narrow for two columns each key takes its own line and its
// description hangs one hang-indent in, and the projection still fits the width.
func TestHelpStacksWhenTheFrameIsNarrow(t *testing.T) {
	for _, width := range []int{18, 22, 26} {
		for _, topic := range helpTopics {
			layout := layoutHelpTopic(topic, width)
			if layout.keyWidth != helpHangIndent {
				t.Fatalf("width %d %s: key column is %d, not the hang indent %d",
					width, topic.title, layout.keyWidth, helpHangIndent)
			}
			for i, line := range layout.lines {
				if line.kind != helpRowLine {
					continue
				}
				if line.keys == "" {
					if pad := ansi.Cut(layout.plain()[i], 0, helpHangIndent); pad != strings.Repeat(" ", helpHangIndent) {
						t.Fatalf("width %d %s: description does not hang under the key: %q",
							width, topic.title, layout.plain()[i])
					}
					continue
				}
				if line.text != "" {
					t.Fatalf("width %d %s: key %q shares its line with a description", width, topic.title, line.keys)
				}
			}
			for i, line := range layout.plain() {
				if lipgloss.Width(line) > width {
					t.Fatalf("width %d %s: line %d is %d cells: %q", width, topic.title, i, lipgloss.Width(line), line)
				}
			}
		}
	}
}

// The scrollbar maps the window onto the rail: the thumb starts at the top, ends
// at the bottom and never leaves the column.
func TestHelpScrollBarTracksTheWindow(t *testing.T) {
	const bodyRows, total = 10, 30
	if got := helpScrollBar(0, bodyRows, 0, total); got != helpThumbStyle.Render("▐") {
		t.Fatalf("top of the topic has no thumb: %q", ansi.Strip(got))
	}
	if got := helpScrollBar(bodyRows-1, bodyRows, total-bodyRows, total); got != helpThumbStyle.Render("▐") {
		t.Fatalf("end of the topic has no thumb at the bottom: %q", ansi.Strip(got))
	}
	if got := helpScrollBar(0, bodyRows, total-bodyRows, total); strings.Contains(got, "▐") {
		t.Fatal("the thumb stayed at the top at the end of the topic")
	}
	for row := 0; row < bodyRows; row++ {
		if got := lipgloss.Width(helpScrollBar(row, bodyRows, 0, total)); got != 1 {
			t.Fatalf("row %d of the rail is %d cells wide", row, got)
		}
	}
	if got := helpScrollBar(0, bodyRows, 0, bodyRows); strings.TrimSpace(got) != "" {
		t.Fatalf("a topic that fits drew a rail: %q", ansi.Strip(got))
	}
}

// Nothing the overlay draws may need more cells than the narrowest frame the
// interface allows: View refuses terminals under 30 columns, so the body is
// never narrower than this, and neither a heading nor a shortcut is broken.
func TestHelpNarrowestFrameFitsItsLabels(t *testing.T) {
	bodyWidth := helpBodyWidth(min(helpMaxWidth, 30-4))
	if bodyWidth < helpKeyColumn() {
		t.Fatalf("the narrowest body is %d cells, under the %d cell key column", bodyWidth, helpKeyColumn())
	}
	for _, topic := range helpTopics {
		for _, section := range topic.sections {
			for _, row := range section.rows {
				if w := lipgloss.Width(row.keys); w > bodyWidth {
					t.Fatalf("%s: shortcut %q needs %d of %d cells", topic.title, row.keys, w, bodyWidth)
				}
			}
			if w := lipgloss.Width(section.heading); w > bodyWidth {
				t.Fatalf("%s: heading %q needs %d of %d cells", topic.title, section.heading, w, bodyWidth)
			}
		}
	}
}
