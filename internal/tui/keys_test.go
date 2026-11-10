package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"pilharmonic/internal/orchestrator"
	"strings"
	"testing"
)

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
	if m.input.text != "q?\nsecond line" || m.help {
		t.Fatalf("composer did not retain pasted text: %q", m.input.text)
	}

	m = model{router: true}
	m = key(m, paste)
	if m.routerDraft != "q?\nsecond line" || !m.router {
		t.Fatalf("router did not retain pasted text: %q", m.routerDraft)
	}
}
