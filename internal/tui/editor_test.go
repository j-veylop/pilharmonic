package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"orchestrator/internal/orchestrator"
)

func TestEditorScrollingKeepsLongDraftVisible(t *testing.T) {
	var lines []string
	for i := 1; i <= 30; i++ {
		lines = append(lines, fmt.Sprintf("line-%02d", i))
	}
	e := editor{text: strings.Join(lines, "\n"), cursor: len(strings.Join(lines, "\n"))}
	e.ensureVisible(20, 4)

	visible := ansi.Strip(strings.Join(e.viewLines(20, 4), "\n"))
	if !strings.Contains(visible, "line-30") || strings.Contains(visible, "line-01") {
		t.Fatalf("long draft did not follow the cursor: %q", visible)
	}

	e.moveStart()
	e.ensureVisible(20, 4)
	visible = ansi.Strip(strings.Join(e.viewLines(20, 4), "\n"))
	if !strings.Contains(visible, "line-01") || strings.Contains(visible, "line-30") {
		t.Fatalf("cursor did not scroll back through the draft: %q", visible)
	}
}

func TestEditorWrapsInsteadOfTruncating(t *testing.T) {
	e := editor{}
	e.insert(strings.Repeat("wrap-this ", 12) + "final-marker")
	e.ensureVisible(18, 6)
	lines := e.viewLines(18, 6)
	if len(lines) < 4 {
		t.Fatalf("long paragraph did not wrap into multiple lines: %#v", lines)
	}
	for _, line := range lines {
		if width := lipgloss.Width(ansi.Strip(line)); width > 19 {
			t.Fatalf("wrapped line is wider than the editor: width=%d line=%q", width, line)
		}
	}
	if !strings.Contains(ansi.Strip(strings.Join(lines, "")), "final-marker") {
		t.Fatalf("end of long draft is not visible: %#v", lines)
	}
}

func TestEditorUpFromFullWrappedLineAdvancesOneLine(t *testing.T) {
	const width = 5
	text := strings.Repeat("abcde", 4)
	e := editor{text: text, cursor: len(text)}
	for want := 2; want >= 0; want-- {
		e.handleKey(tea.KeyMsg{Type: tea.KeyUp}, width, 2)
		lines := wrapEditor(e.text, width)
		if got := editorCursorLine(lines, e.cursor); got != want {
			t.Fatalf("up from end of a full wrapped line: line=%d want %d cursor=%d", got, want, e.cursor)
		}
		visible := ansi.Strip(strings.Join(e.viewLines(width, 2), "\n"))
		if !strings.Contains(visible, "▏") {
			t.Fatalf("cursor row not visible after up: %q", visible)
		}
	}
}

func TestComposerFooterWrapsLongDraft(t *testing.T) {
	text := strings.Repeat("keep this visible ", 8) + "final-marker"
	m := model{width: 48, height: 24, input: composer{kind: newTaskInput, text: text, cursor: len(text)}}
	footer := m.footer(48)
	if strings.Count(footer, "\n") < 3 {
		t.Fatalf("composer footer stayed single-line: %q", footer)
	}
	plain := ansi.Strip(footer)
	if !strings.Contains(plain, "final-marker") {
		t.Fatalf("end of long input is not visible: %q", plain)
	}
	if strings.Contains(plain, "…") {
		t.Fatalf("composer footer truncated the draft: %q", plain)
	}
	for _, line := range strings.Split(plain, "\n") {
		if lipgloss.Width(line) > 48 {
			t.Fatalf("footer line overflows terminal width: %q", line)
		}
	}
}

func TestComposerNewlineAndSubmitKeepFullPrompt(t *testing.T) {
	s, err := orchestrator.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	m := model{service: s, workspace: t.TempDir(), width: 100, height: 30, input: composer{kind: newTaskInput}}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("first line")})
	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("second line")})
	if m.input.text != "first line\nsecond line" {
		t.Fatalf("ctrl+j did not insert a newline: %q", m.input.text)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	tasks := s.ListTasks()
	if len(tasks) != 1 || tasks[0].Prompt != "first line\nsecond line" {
		t.Fatalf("multiline prompt was not submitted intact: %+v", tasks)
	}
	if m.input != (composer{}) {
		t.Fatalf("composer retained submitted draft: %+v", m.input)
	}
}

func TestComposerAltEnterInsertsNewline(t *testing.T) {
	m := model{width: 100, height: 30, input: composer{kind: newTaskInput}}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("first")})
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("second")})
	if m.input.text != "first\nsecond" {
		t.Fatalf("alt+enter did not insert a newline: %q", m.input.text)
	}
}

func TestRouterComposerWrapsAndSubmitsMultilineRequest(t *testing.T) {
	s, err := orchestrator.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	m := model{service: s, workspace: t.TempDir(), width: 60, height: 35, router: true}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("long request ", 8))})
	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("final detail")})
	if strings.Count(m.routerDraft, "\n") != 1 {
		t.Fatalf("router draft did not keep its newline: %q", m.routerDraft)
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "final detail") || strings.Contains(view, "…") {
		t.Fatalf("router draft was truncated instead of wrapped: %s", view)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	tasks := s.ListTasks()
	if len(tasks) != 1 || !strings.HasSuffix(tasks[0].Prompt, "\nfinal detail") {
		t.Fatalf("router did not submit the multiline request intact: %+v", tasks)
	}
}

func TestRouterAltEnterDoesNotSubmit(t *testing.T) {
	s, err := orchestrator.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	s.RegisterRunner("general", routerRunner{})
	m := model{service: s, workspace: t.TempDir(), width: 80, height: 24, router: true}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("first")})
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	if m.routerDraft != "first\n" || len(s.ListTasks()) != 0 {
		t.Fatalf("alt+enter submitted instead of inserting a newline: draft=%q tasks=%+v", m.routerDraft, s.ListTasks())
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("second")})
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	tasks := s.ListTasks()
	if len(tasks) != 1 || tasks[0].Prompt != "first\nsecond" || m.routerDraft != "" {
		t.Fatalf("enter did not submit the complete draft: draft=%q tasks=%+v", m.routerDraft, tasks)
	}
}

func TestComposerPageKeysScrollLongDraft(t *testing.T) {
	var lines []string
	for i := 1; i <= 30; i++ {
		lines = append(lines, fmt.Sprintf("line-%02d", i))
	}
	text := strings.Join(lines, "\n")
	m := model{width: 80, height: 24, input: composer{kind: newTaskInput, text: text, cursor: len(text)}}
	before := ansi.Strip(m.footer(80))
	if !strings.Contains(before, "line-30") || strings.Contains(before, "line-01") {
		t.Fatalf("long composer did not open at the cursor: %q", before)
	}
	for i := 0; i < 6; i++ {
		m = key(m, tea.KeyMsg{Type: tea.KeyPgUp})
	}
	after := ansi.Strip(m.footer(80))
	if !strings.Contains(after, "line-01") || strings.Contains(after, "line-30") {
		t.Fatalf("pgup did not scroll the composer: %q", after)
	}
}

func TestComposerViewFitsTerminal(t *testing.T) {
	text := strings.Repeat("long composer text ", 12)
	for _, size := range [][2]int{{120, 35}, {80, 24}, {50, 20}, {30, 10}} {
		m := model{width: size[0], height: size[1], input: composer{kind: newTaskInput, text: text, cursor: len(text)}}
		view := m.View()
		if width, height := lipgloss.Width(view), lipgloss.Height(view); width > size[0] || height > size[1] {
			t.Fatalf("%v: rendered %dx%d", size, width, height)
		}
	}
}

func TestRouterLongDraftFitsTerminal(t *testing.T) {
	text := strings.Repeat("long router request ", 12) + "final-detail"
	for _, size := range [][2]int{{120, 35}, {80, 24}, {50, 20}, {30, 10}} {
		m := model{width: size[0], height: size[1], router: true, routerDraft: text, routerCursor: len(text)}
		view := m.View()
		if width, height := lipgloss.Width(view), lipgloss.Height(view); width > size[0] || height > size[1] {
			t.Fatalf("%v: rendered %dx%d", size, width, height)
		}
		if !strings.Contains(ansi.Strip(view), "final-detail") {
			t.Fatalf("%v: router draft missing: %s", size, ansi.Strip(view))
		}
	}
}
