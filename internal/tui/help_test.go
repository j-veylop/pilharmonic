package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestHelpModalCapturesInputAndScrolls(t *testing.T) {
	m := model{width: 80, height: 14, help: true}
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
	if m.helpScroll == 0 {
		t.Fatal("help wheel did not scroll")
	}
	m.helpScroll = 0
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(model)
	if m.helpScroll == 0 {
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
	if m.help {
		t.Fatal("escape did not close help")
	}
}
func TestHelpModalBounds(t *testing.T) {
	for _, size := range [][2]int{{30, 10}, {50, 20}, {80, 24}, {120, 40}} {
		m := model{width: size[0], height: size[1], help: true}
		for _, offset := range []int{0, 1000} {
			m.helpPage = offset
			view := m.View()
			if lipgloss.Width(view) > size[0] || lipgloss.Height(view) > size[1] {
				t.Fatalf("overflow at %v", size)
			}
		}
	}
}

func TestHelpPagination(t *testing.T) {
	m := model{width: 80, height: 24, help: true}
	if !strings.Contains(m.View(), "Page 1 /") {
		t.Fatal("missing page indicator")
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = next.(model)
	if m.helpPage != 1 || !strings.Contains(m.View(), "Page 2 /") {
		t.Fatal("right did not advance exactly one page")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = next.(model)
	if m.helpPage != 0 {
		t.Fatal("page up")
	}
	m.scrollHelp(-1)
	if m.helpPage != 0 {
		t.Fatal("wrapped before first page")
	}
	m.scrollHelp(10000)
	last := m.helpPage
	m.scrollHelp(1)
	if m.helpPage != last {
		t.Fatal("wrapped past last page")
	}
	if !strings.Contains(m.View(), "Select in executor") {
		t.Fatal("final page lost final shortcut")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyHome})
	m = next.(model)
	if m.helpPage != 0 {
		t.Fatal("home")
	}
}

func TestHelpTopicsAreLogicalAndIndependent(t *testing.T) {
	m := model{width: 80, height: 24, help: true}
	concepts := strings.Join(m.helpLines(70), "\n")
	if !strings.Contains(concepts, "Accepted work") || !strings.Contains(concepts, "need you") || strings.Contains(concepts, "Focus tasks") {
		t.Fatal("concepts missing or contains another topic")
	}
	m.scrollHelp(1)
	navigation := strings.Join(m.helpLines(70), "\n")
	if !strings.Contains(navigation, "Focus tasks") || strings.Contains(navigation, "New task") {
		t.Fatal("navigation contains another topic")
	}
	m.scrollHelp(1)
	tasks := strings.Join(m.helpLines(70), "\n")
	if !strings.Contains(tasks, "New task") || strings.Contains(tasks, "Toggle tool activity") {
		t.Fatal("tasks contains another topic")
	}
	m.scrollHelp(1)
	if !strings.Contains(strings.Join(m.helpLines(70), "\n"), "Toggle tool activity") {
		t.Fatal("activity missing")
	}
	m.scrollHelpBody(5)
	m.scrollHelp(1)
	if m.helpScroll != 0 {
		t.Fatal("topic switch retained old scroll")
	}
	if !strings.Contains(m.helpTabs(70), "Navigation") || !strings.Contains(m.helpTabs(70), "General") {
		t.Fatal("horizontal tabs missing")
	}
}

// No topic may draw outside the modal it opened in, at any size the interface
// supports and at either end of the scroll range.
func TestHelpEveryTopicFitsTheModal(t *testing.T) {
	sizes := [][2]int{{30, 10}, {50, 20}, {80, 24}, {120, 40}}
	for _, size := range sizes {
		for page, topic := range helpTopics {
			for _, scroll := range []int{0, 1000} {
				m := model{width: size[0], height: size[1], help: true, helpPage: page, helpScroll: scroll}
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
				if want := helpHeadingStyle.Render(line.text); styled[i] != want {
					t.Fatalf("%s: heading %q is not drawn with the heading style: %q", topic.title, line.text, ansi.Strip(styled[i]))
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

// The strip marks the active topic, mutes the others and falls back to a
// position chip that still fits the frame.
func TestHelpTopicStripMarksTheActiveTopic(t *testing.T) {
	inactive := func(title string) string { return inactiveTabStyle.Padding(0, 1).Render(title) }
	if activeTabStyle.GetForeground() == inactiveTabStyle.GetForeground() && activeTabStyle.GetUnderline() == inactiveTabStyle.GetUnderline() {
		t.Fatal("the active topic is not styled apart from the inactive ones")
	}
	m := model{width: 80, height: 24, help: true}
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
	if got := ansi.Strip(m.helpTabs(20)); !strings.Contains(got, "‹ Navigation › 2/7") {
		t.Fatalf("narrow strip lost its position chip: %q", got)
	}
}

// The hint row stays inside the frame and keeps its most useful hint at every
// width.
func TestHelpHintRowFitsTheFrame(t *testing.T) {
	m := model{width: 80, height: 24, help: true}
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
	m := model{width: 30, height: 10, help: true}
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
	short := model{width: 120, height: 40, help: true, helpPage: 3}
	_, _, fitted := short.helpModal(120, 40)
	if joined := strings.Join(fitted, "\n"); strings.Contains(joined, "▐") || strings.Contains(joined, "│") {
		t.Fatalf("a topic that fits still draws a scrollbar: %q", ansi.Strip(joined))
	}
}

// The overlay is text on the existing background: no cell may set a background
// colour of its own.
func TestHelpUsesNoBackgroundColour(t *testing.T) {
	for page := range helpTopics {
		for _, size := range [][2]int{{30, 10}, {80, 24}, {120, 40}} {
			m := model{width: size[0], height: size[1], help: true, helpPage: page}
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
	for _, size := range [][2]int{{30, 10}, {40, 12}, {50, 20}, {80, 24}, {120, 40}} {
		for page, topic := range helpTopics {
			m := model{width: size[0], height: size[1], help: true, helpPage: page}
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
