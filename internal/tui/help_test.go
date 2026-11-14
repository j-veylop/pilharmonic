package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestHelpKeysChangePageScrollAndClose(t *testing.T) {
	last := len(helpTopics) - 1
	cases := []struct {
		key          tea.KeyMsg
		start        helpState
		page, scroll int
		open         bool
	}{
		{tea.KeyMsg{Type: tea.KeyRight}, helpState{open: true}, 1, 0, true},
		{runeKey("l"), helpState{open: true, scroll: 2}, 1, 0, true},
		{tea.KeyMsg{Type: tea.KeyLeft}, helpState{open: true, page: 2}, 1, 0, true},
		{tea.KeyMsg{Type: tea.KeyLeft}, helpState{open: true, scroll: 1}, 0, 1, true}, // no wrap; scroll kept
		{tea.KeyMsg{Type: tea.KeyRight}, helpState{open: true, page: last}, last, 0, true},
		{tea.KeyMsg{Type: tea.KeyDown}, helpState{open: true}, 0, 1, true},
		{tea.KeyMsg{Type: tea.KeyEnd}, helpState{open: true, scroll: 3}, last, 0, true},
		{tea.KeyMsg{Type: tea.KeyHome}, helpState{open: true, page: 3}, 0, 0, true},
		{runeKey("n"), helpState{open: true}, 0, 0, true}, // board keys do not leak through
		{tea.KeyMsg{Type: tea.KeyEsc}, helpState{open: true}, 0, 0, false},
		{runeKey("?"), helpState{open: true}, 0, 0, false},
		{runeKey("q"), helpState{open: true}, 0, 0, false},
	}
	for _, tc := range cases {
		m := key(model{width: 30, height: 10, help: tc.start}, tc.key)
		if m.help.page != tc.page || m.help.scroll != tc.scroll || m.help.open != tc.open || m.input.kind != "" || m.quitConfirm {
			t.Fatalf("%s from %+v: got %+v", tc.key, tc.start, m.help)
		}
	}
	if m := key(model{help: helpState{open: true}}, tea.KeyMsg{Type: tea.KeyCtrlC}); !m.quitConfirm {
		t.Fatal("ctrl+c inside help did not ask to quit")
	}
	m := model{width: 80, height: 14, help: helpState{open: true}}
	if next, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown}); next.(model).help.scroll == 0 {
		t.Fatal("help wheel did not scroll")
	}
}

func TestHelpGoldenFrame(t *testing.T) {
	m := model{width: 80, height: 24, help: helpState{open: true}}
	view := ansi.Strip(m.View())
	for _, want := range []string{"HELP · Concepts", "1/7", "▸Concepts", " Navigation", "Request", "Accepted work", "esc close", "h/l topics", "j/k scroll"} {
		if !strings.Contains(view, want) {
			t.Fatalf("help frame lost %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Focus tasks") {
		t.Fatal("concepts page shows another topic")
	}
	m.help.page = 1
	if view := ansi.Strip(m.View()); !strings.Contains(view, "HELP · Navigation") || !strings.Contains(view, "▸Navigation") {
		t.Fatalf("page change did not move the title and strip:\n%s", view)
	}
}

// Every topic fits its frame at every supported size, scrolled or not, and
// keyboard scrolling reaches the last line the overlay draws.
func TestHelpEveryTopicFitsAndScrollsToItsEnd(t *testing.T) {
	for _, size := range [][2]int{{30, 10}, {40, 12}, {50, 20}, {60, 20}, {80, 24}, {120, 40}} {
		w, h := size[0], size[1]
		for page, topic := range helpTopics {
			m := model{width: w, height: h, help: helpState{open: true, page: page}}
			mw, mh, content := m.helpModal(w, h)
			if content == nil || mw > w || mh > h || len(content) > mh-2 {
				t.Fatalf("%s at %v: modal %dx%d with %d rows", topic.title, size, mw, mh, len(content))
			}
			for row, line := range content {
				if lipgloss.Width(line) > mw-4 {
					t.Fatalf("%s at %v: row %d overflows: %q", topic.title, size, row, ansi.Strip(line))
				}
			}
			m.scrollHelpBody(1000)
			view := m.View()
			assertFits(t, view, w, h)
			lines := helpLines(topic, helpBodyWidth(mw))
			if last := strings.TrimSpace(ansi.Strip(lines[len(lines)-1])); !strings.Contains(ansi.Strip(view), last) {
				t.Fatalf("%s at %v: last line %q is unreachable", topic.title, size, last)
			}
			if strings.Contains(view, "\x1b[48;") {
				t.Fatalf("%s at %v: help sets a background colour", topic.title, size)
			}
		}
	}
}

// Descriptions share one column on wide frames and hang under each key on
// narrow ones; no text is lost to wrapping either way.
func TestHelpLinesAlignAndKeepEveryWord(t *testing.T) {
	column := helpKeyColumn()
	for _, width := range []int{16, 22, 40, 72} {
		for _, topic := range helpTopics {
			lines := helpLines(topic, width)
			text := ""
			for _, line := range lines {
				plain := ansi.Strip(line)
				if lipgloss.Width(plain) > width {
					t.Fatalf("width %d %s: %q overflows", width, topic.title, plain)
				}
				text += strings.Join(strings.Fields(plain), "")
			}
			for _, section := range topic.sections {
				for _, row := range section.rows {
					if desc := strings.Join(strings.Fields(row.desc), ""); !strings.Contains(text, desc) {
						t.Fatalf("width %d %s: lost %q", width, topic.title, row.desc)
					}
				}
			}
		}
	}
	for _, line := range helpLines(helpTopics[1], 72) {
		if plain := ansi.Strip(line); strings.HasPrefix(plain, "h / l") && ansi.Cut(plain, column, column+5) != "Focus" {
			t.Fatalf("description is not in column %d: %q", column, plain)
		}
	}
	narrow := ansi.Strip(strings.Join(helpLines(helpTopics[1], 16), "\n"))
	if !strings.Contains(narrow, "h / l\n  Focus") {
		t.Fatalf("narrow layout does not hang descriptions under their keys:\n%s", narrow)
	}
}

// The strip falls back from titles to positions to nothing as the frame narrows.
func TestHelpTopicStripNarrows(t *testing.T) {
	if got := ansi.Strip(helpTabs(72, 1)); !strings.Contains(got, "▸Navigation") || strings.Count(got, "▸") != 1 {
		t.Fatalf("wide strip: %q", got)
	}
	if got := ansi.Strip(helpTabs(40, 1)); !strings.Contains(got, "▸2") || strings.Contains(got, "Navigation") {
		t.Fatalf("narrow strip: %q", got)
	}
	if got := helpTabs(12, 1); got != "" {
		t.Fatalf("tiny strip: %q", ansi.Strip(got))
	}
	for _, width := range []int{72, 22, 12} {
		if row := ansi.Strip(fitHints(width, "esc close", "h/l topics", "j/k scroll")); lipgloss.Width(row) > width || !strings.HasPrefix(row, "esc close") {
			t.Fatalf("width %d hint row: %q", width, row)
		}
	}
}
