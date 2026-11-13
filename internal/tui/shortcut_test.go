package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
)

// keyWithString builds the KeyMsg bubbletea would deliver for a named key so
// tests exercise the same key strings the terminal reports.
func shortcutKey(t *testing.T, name string) tea.KeyMsg {
	t.Helper()
	switch name {
	case "ctrl+w":
		return tea.KeyMsg{Type: tea.KeyCtrlW}
	case "ctrl+u":
		return tea.KeyMsg{Type: tea.KeyCtrlU}
	case "ctrl+k":
		return tea.KeyMsg{Type: tea.KeyCtrlK}
	case "ctrl+a":
		return tea.KeyMsg{Type: tea.KeyCtrlA}
	case "ctrl+e":
		return tea.KeyMsg{Type: tea.KeyCtrlE}
	case "ctrl+d":
		return tea.KeyMsg{Type: tea.KeyCtrlD}
	case "ctrl+n":
		return tea.KeyMsg{Type: tea.KeyCtrlN}
	case "ctrl+p":
		return tea.KeyMsg{Type: tea.KeyCtrlP}
	case "ctrl+left":
		return tea.KeyMsg{Type: tea.KeyCtrlLeft}
	case "ctrl+right":
		return tea.KeyMsg{Type: tea.KeyCtrlRight}
	case "alt+backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace, Alt: true}
	case "alt+d":
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d"), Alt: true}
	case "alt+b":
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b"), Alt: true}
	case "alt+f":
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f"), Alt: true}
	case "alt+delete":
		return tea.KeyMsg{Type: tea.KeyDelete, Alt: true}
	case "alt+left":
		return tea.KeyMsg{Type: tea.KeyLeft, Alt: true}
	case "alt+right":
		return tea.KeyMsg{Type: tea.KeyRight, Alt: true}
	}
	t.Fatalf("unknown test key %q", name)
	return tea.KeyMsg{}
}

func TestEditorDeletesWordsWithStandardShortcuts(t *testing.T) {
	cases := []struct {
		key       string
		text      string
		want      string
		wantAfter int // cursor position when the deletion leaves a natural anchor
	}{
		{"ctrl+w", "hello big world", "hello big ", 10},
		{"ctrl+w", "hello big   ", "hello ", 6},
		{"ctrl+w", "one", "", 0},
		{"ctrl+w", "tab\tsep\tinput", "tab\tsep\t", 8},
		{"ctrl+w", "café naïve", "café ", 6},
		{"alt+backspace", "hello big world", "hello big ", 10},
		{"alt+backspace", "punctuation, stays", "punctuation, ", 13},
	}
	for _, tc := range cases {
		e := editor{text: tc.text, cursor: len(tc.text)}
		if !e.handleKey(shortcutKey(t, tc.key), 20, 4) {
			t.Fatalf("%s was not handled", tc.key)
		}
		if e.text != tc.want || e.cursor != tc.wantAfter {
			t.Fatalf("%s on %q: text=%q cursor=%d, want %q at %d", tc.key, tc.text, e.text, e.cursor, tc.want, tc.wantAfter)
		}
	}
}

func TestEditorDeleteWordStopsAtNewlines(t *testing.T) {
	e := editor{text: "first line\nsecond", cursor: len("first line\nsecond")}
	e.handleKey(shortcutKey(t, "ctrl+w"), 20, 6)
	if e.text != "first line\n" || e.cursor != len("first line\n") {
		t.Fatalf("ctrl+w crossed the line break: %q at %d", e.text, e.cursor)
	}
	e.handleKey(shortcutKey(t, "ctrl+w"), 20, 6)
	if e.text != "first line\n" {
		t.Fatalf("ctrl+w at a line start reached the previous line: %q", e.text)
	}
}

func TestEditorDeletesWordForward(t *testing.T) {
	e := editor{text: "keep drop keep", cursor: len("keep ")}
	for _, key := range []string{"alt+d", "alt+delete"} {
		e = editor{text: "keep drop keep", cursor: len("keep ")}
		e.handleKey(shortcutKey(t, key), 20, 4)
		if e.text != "keep keep" || e.cursor != len("keep ") {
			t.Fatalf("%s left %q at %d", key, e.text, e.cursor)
		}
	}
}

func TestEditorWordMotionKeepsTextIntact(t *testing.T) {
	keys := []string{"ctrl+left", "alt+left", "alt+b"}
	for _, key := range keys {
		e := editor{text: "alpha beta gamma", cursor: len("alpha beta gamma")}
		e.handleKey(shortcutKey(t, key), 30, 4)
		if e.text != "alpha beta gamma" || e.cursor != len("alpha beta ") {
			t.Fatalf("%s moved to %d (%q)", key, e.cursor, e.text)
		}
		e.handleKey(shortcutKey(t, key), 30, 4)
		if e.cursor != len("alpha ") {
			t.Fatalf("%s second step moved to %d", key, e.cursor)
		}
	}
	for _, key := range []string{"ctrl+right", "alt+right", "alt+f"} {
		e := editor{text: "alpha beta gamma", cursor: 0}
		e.handleKey(shortcutKey(t, key), 30, 4)
		if e.text != "alpha beta gamma" || e.cursor != len("alpha") {
			t.Fatalf("%s moved to %d", key, e.cursor)
		}
		e.handleKey(shortcutKey(t, key), 30, 4)
		if e.cursor != len("alpha beta") {
			t.Fatalf("%s second step moved to %d", key, e.cursor)
		}
	}
}

func TestEditorKillsToLineBounds(t *testing.T) {
	e := editor{text: "first line\nsecond line\nthird", cursor: len("first line\nsecond")}
	e.handleKey(shortcutKey(t, "ctrl+k"), 40, 6)
	if e.text != "first line\nsecond\nthird" {
		t.Fatalf("ctrl+k removed more than the line tail: %q", e.text)
	}
	e.handleKey(shortcutKey(t, "ctrl+k"), 40, 6)
	if e.text != "first line\nsecondthird" {
		t.Fatalf("ctrl+k at a line end did not join the next line: %q", e.text)
	}
	e = editor{text: "first line\nsecond", cursor: len("first line\nsecond")}
	e.handleKey(shortcutKey(t, "ctrl+u"), 40, 6)
	if e.text != "first line\n" || e.cursor != len("first line\n") {
		t.Fatalf("ctrl+u kept %q at %d", e.text, e.cursor)
	}
	e.handleKey(shortcutKey(t, "ctrl+u"), 40, 6)
	if e.text != "first line" {
		t.Fatalf("ctrl+u at a line start did not join the previous line: %q", e.text)
	}
}

func TestEditorKeepsHomeAndEndBindings(t *testing.T) {
	e := editor{text: "one\ntwo", cursor: len("one\ntw")}
	e.handleKey(shortcutKey(t, "ctrl+a"), 20, 4)
	if e.cursor != len("one\n") {
		t.Fatalf("ctrl+a moved to %d", e.cursor)
	}
	e.handleKey(shortcutKey(t, "ctrl+e"), 20, 4)
	if e.cursor != len("one\ntwo") {
		t.Fatalf("ctrl+e moved to %d", e.cursor)
	}
}

// TestComposerStandardShortcutsReachTheDraft checks the screen-level key
// routing: composers accept printable shortcuts as text, so the word-editing
// keys must arrive at the editor rather than be treated as commands.
func TestComposerStandardShortcutsReachTheDraft(t *testing.T) {
	m := model{width: 80, height: 24, input: composer{kind: newTaskInput, text: "keep drop", cursor: len("keep drop")}}
	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlW})
	if m.input.text != "keep " || m.input.cursor != len("keep ") {
		t.Fatalf("task composer ctrl+w left %q at %d", m.input.text, m.input.cursor)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace, Alt: true})
	m = next.(model)
	if m.input.text != "" {
		t.Fatalf("alt+backspace left %q", m.input.text)
	}

	r := model{width: 80, height: 24, router: routerState{open: true, draft: "keep drop", cursor: len("keep drop")}}
	r = key(r, tea.KeyMsg{Type: tea.KeyCtrlW})
	if r.router.draft != "keep " || r.router.cursor != len("keep ") {
		t.Fatalf("request form ctrl+w left %q at %d", r.router.draft, r.router.cursor)
	}
	if !r.router.open {
		t.Fatal("ctrl+w closed the request form")
	}
}

func TestEditorDeletesForwardWithDeleteKeys(t *testing.T) {
	for _, key := range []string{"ctrl+d"} {
		e := editor{text: "ab世cd", cursor: len("ab")}
		e.handleKey(shortcutKey(t, key), 20, 4)
		if e.text != "abcd" {
			t.Fatalf("%s removed %q", key, e.text)
		}
	}
	m := model{width: 80, height: 24, input: composer{kind: newTaskInput, text: "ab", cursor: 1}}
	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlD})
	if m.input.text != "a" || m.input.cursor != 1 {
		t.Fatalf("ctrl+d in a composer left %q at %d", m.input.text, m.input.cursor)
	}
}

func TestEditorMovesWrappedLinesWithCtrlNP(t *testing.T) {
	m := model{width: 40, height: 24, input: composer{kind: newTaskInput}}
	m = key(m, runeKey("first"))
	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = key(m, runeKey("second"))
	m = key(m, shortcutKey(t, "ctrl+p"))
	if m.input.cursor != len("first") {
		t.Fatalf("ctrl+p moved to %d", m.input.cursor)
	}
	m = key(m, shortcutKey(t, "ctrl+n"))
	if m.input.cursor != len("first\nsecon") {
		t.Fatalf("ctrl+n kept column 5 at %d", m.input.cursor)
	}
}

func TestEditorCursorColumnCountsWideRunes(t *testing.T) {
	e := editor{text: "日本 ab 語", cursor: len("日本 ")}
	line := ansi.Strip(e.viewLinesCursor(20, 2, true)[0])
	at := strings.Index(line, "▏")
	if at < 0 {
		t.Fatalf("no cursor in %q", line)
	}
	if column := lipgloss.Width(line[:at]); column != 5 {
		t.Fatalf("cursor sits at column %d, want 5: %q", column, line)
	}
}

func TestComposerBoxKeepsCellWidthForWideText(t *testing.T) {
	m := model{width: 40, height: 20, tasks: []orchestrator.Task{{ID: "task", Title: "work"}}}
	text := strings.Repeat("日本語のテキスト ", 6)
	m.input = composer{kind: newTaskInput, text: text, cursor: len(text)}
	for _, row := range m.composerBox(m.composerHeight()) {
		if width := lipgloss.Width(ansi.Strip(row)); width != 40 {
			t.Fatalf("box row is %d cells: %q", width, ansi.Strip(row))
		}
	}
}

func TestVimMotionsAndFocus(t *testing.T) {
	m := model{width: 120, height: 24, tasks: make([]orchestrator.Task, 20)}
	for i := range m.tasks {
		m.tasks[i].Output = strings.Repeat("line\n", 100)
	}
	for _, key := range []string{"5", "j"} {
		if !m.vimNavigation(key) {
			t.Fatal(key)
		}
	}
	if m.selected != 5 {
		t.Fatalf("5j selected %d", m.selected)
	}
	m.vimNavigation("G")
	if m.selected != 19 {
		t.Fatal("G")
	}
	m.vimNavigation("g")
	if m.selected != 19 {
		t.Fatal("first g moved")
	}
	m.vimNavigation("g")
	if m.selected != 0 {
		t.Fatal("gg")
	}
	m.vimNavigation("l")
	if !m.detailFocus {
		t.Fatal("l")
	}
	m.vimNavigation("ctrl+u")
	if m.historyOffset == 0 {
		t.Fatal("half page up")
	}
	m.vimNavigation("g")
	m.vimNavigation("g")
	if m.historyOffset != m.maxScroll() {
		t.Fatal("detail gg")
	}
	m.vimNavigation("G")
	if m.historyOffset != 0 {
		t.Fatal("detail G")
	}
	m.vimNavigation("h")
	if m.detailFocus {
		t.Fatal("h")
	}
}
func TestVimKeysRemainTextInComposer(t *testing.T) {
	m := model{input: composer{kind: newTaskInput}}
	for _, r := range "ggGhjkl123" {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(model)
	}
	if m.input.text != "ggGhjkl123" || m.vimG || m.vimCount != 0 {
		t.Fatalf("composer consumed motions: %+v", m)
	}
}

func TestCancelComposerClearsTargetAndDraft(t *testing.T) {
	m := model{input: composer{kind: threadInput, target: "previous", text: "discard me"}}
	m = key(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.input != (composer{}) {
		t.Fatalf("canceled input retained state: %+v", m.input)
	}
}

func TestBracketedPasteEntersComposers(t *testing.T) {
	paste := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q?\nsecond line"), Paste: true}

	m := model{input: composer{kind: newTaskInput}}
	m = key(m, paste)
	if m.input.text != "q?\nsecond line" || m.help.open {
		t.Fatalf("composer did not retain pasted text: %q", m.input.text)
	}

	m = model{router: routerState{open: true}}
	m = key(m, paste)
	if m.router.draft != "q?\nsecond line" || !m.router.open {
		t.Fatalf("router did not retain pasted text: %q", m.router.draft)
	}
}
