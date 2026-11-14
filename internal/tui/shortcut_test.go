package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
)

// TestComposerStandardShortcutsReachTheDraft checks the screen-level key
// routing: composers accept printable shortcuts as text, so the word-editing
// keys must arrive at the editor rather than be treated as commands.
func TestComposerStandardShortcutsReachTheDraft(t *testing.T) {
	m := model{width: 80, height: 24, input: composer{kind: newTaskInput, editor: editor{text: "keep drop", cursor: len("keep drop")}}}
	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlW})
	if m.input.text != "keep " || m.input.cursor != len("keep ") {
		t.Fatalf("task composer ctrl+w left %q at %d", m.input.text, m.input.cursor)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace, Alt: true})
	m = next.(model)
	if m.input.text != "" {
		t.Fatalf("alt+backspace left %q", m.input.text)
	}

	r := model{width: 80, height: 24, router: routerState{open: true, editor: editor{text: "keep drop", cursor: len("keep drop")}}}
	r = key(r, tea.KeyMsg{Type: tea.KeyCtrlW})
	if r.router.text != "keep " || r.router.cursor != len("keep ") {
		t.Fatalf("request form ctrl+w left %q at %d", r.router.text, r.router.cursor)
	}
	if !r.router.open {
		t.Fatal("ctrl+w closed the request form")
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
	m.input = composer{kind: newTaskInput, editor: editor{text: text, cursor: len(text)}}
	for _, row := range m.composerBlock(m.composerHeight()) {
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
	m := model{input: composer{kind: threadInput, target: "previous", editor: editor{text: "discard me"}}}
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
	if m.router.text != "q?\nsecond line" || !m.router.open {
		t.Fatalf("router did not retain pasted text: %q", m.router.text)
	}
}
