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

// Every topic fits its frame at every supported size, scrolled or not, keeps
// every description word when wrapped, and keyboard scrolling reaches the last
// line the overlay draws.
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
			text := ""
			for _, line := range lines {
				plain := ansi.Strip(line)
				if lipgloss.Width(plain) > helpBodyWidth(mw) {
					t.Fatalf("%s at %v: %q overflows", topic.title, size, plain)
				}
				text += strings.Join(strings.Fields(plain), "")
			}
			for _, section := range topic.sections {
				for _, row := range section.rows {
					if desc := strings.Join(strings.Fields(row.desc), ""); !strings.Contains(text, desc) {
						t.Fatalf("%s at %v: lost %q", topic.title, size, row.desc)
					}
				}
			}
		}
	}
}

// Descriptions share one column on wide frames and hang under each key on
// narrow ones.
func TestHelpLinesAlignOrHangDescriptions(t *testing.T) {
	column := helpKeyColumn()
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

func TestKeyTableBindsEachKeyOnce(t *testing.T) {
	seen := map[string]bool{}
	for _, b := range boardKeys {
		if seen[b.key] {
			t.Fatalf("%q is bound twice", b.key)
		}
		seen[b.key] = true
	}
	for _, removed := range []string{"n", "A", "P", "w", "R", "V", "F", "L", "T", "[", "]", "home", "end"} {
		if seen[removed] {
			t.Fatalf("removed key %q is still bound", removed)
		}
	}
}
