package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
	"pilharmonic/internal/tui/editor"
)

// TestComposerStandardShortcutsReachTheDraft checks the screen-level key
// routing: composers accept printable shortcuts as text, so the word-editing
// keys must arrive at the editor rather than be treated as commands.
func TestComposerStandardShortcutsReachTheDraft(t *testing.T) {
	m := model{width: 80, height: 24, input: composer{kind: reportInput, Editor: editor.Editor{Text: "keep drop", Cursor: len("keep drop")}}}
	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlW})
	if m.input.Text != "keep " || m.input.Cursor != len("keep ") {
		t.Fatalf("task composer ctrl+w left %q at %d", m.input.Text, m.input.Cursor)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace, Alt: true})
	m = next.(model)
	if m.input.Text != "" {
		t.Fatalf("alt+backspace left %q", m.input.Text)
	}

	r := model{width: 80, height: 24, router: routerState{open: true, Editor: editor.Editor{Text: "keep drop", Cursor: len("keep drop")}}}
	r = key(r, tea.KeyMsg{Type: tea.KeyCtrlW})
	if r.router.Text != "keep " || r.router.Cursor != len("keep ") {
		t.Fatalf("request form ctrl+w left %q at %d", r.router.Text, r.router.Cursor)
	}
	if !r.router.open {
		t.Fatal("ctrl+w closed the request form")
	}
}

func TestComposerBoxKeepsCellWidthForWideText(t *testing.T) {
	m := model{width: 40, height: 20, tasks: []orchestrator.Task{{ID: "task", Title: "work"}}}
	text := strings.Repeat("日本語のテキスト ", 6)
	m.input = composer{kind: reportInput, Editor: editor.Editor{Text: text, Cursor: len(text)}}
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
	m := model{input: composer{kind: reportInput}}
	for _, r := range "ggGhjkl123" {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(model)
	}
	if m.input.Text != "ggGhjkl123" || m.vimG || m.vimCount != 0 {
		t.Fatalf("composer consumed motions: %+v", m)
	}
}

func TestCancelComposerClearsTargetAndDraft(t *testing.T) {
	m := model{input: composer{kind: threadInput, target: "previous", Editor: editor.Editor{Text: "discard me"}}}
	m = key(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.input != (composer{}) {
		t.Fatalf("canceled input retained state: %+v", m.input)
	}
}

func TestBracketedPasteEntersComposers(t *testing.T) {
	paste := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q?\nsecond line"), Paste: true}

	m := model{input: composer{kind: reportInput}}
	m = key(m, paste)
	if m.input.Text != "q?\nsecond line" || m.help.open {
		t.Fatalf("composer did not retain pasted text: %q", m.input.Text)
	}

	m = model{router: routerState{open: true}}
	m = key(m, paste)
	if m.router.Text != "q?\nsecond line" || !m.router.open {
		t.Fatalf("router did not retain pasted text: %q", m.router.Text)
	}
}

type runRecorder struct {
	fakeLoginService
	calls []string
}

func (r *runRecorder) Retry(id string) error { r.calls = append(r.calls, "retry "+id); return nil }
func (r *runRecorder) Start(_ context.Context, id string) error {
	r.calls = append(r.calls, "start "+id)
	return nil
}

func TestRunStartsHeldTasksAndRetriesFailedOnes(t *testing.T) {
	for _, tc := range []struct {
		status orchestrator.Status
		want   string
	}{{orchestrator.Queued, "start task"}, {orchestrator.Failed, "retry task,start task"}} {
		service := &runRecorder{fakeLoginService: fakeLoginService{tasks: []orchestrator.Task{{ID: "task", Status: tc.status}}}}
		key(model{service: service, tasks: service.tasks}, runeKey("r"))
		if got := strings.Join(service.calls, ","); got != tc.want {
			t.Fatalf("%s: r called %q, want %q", tc.status, got, tc.want)
		}
	}
}
