package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
)

func TestEditorScrollingKeepsLongDraftVisible(t *testing.T) {
	var lines []string
	for i := 1; i <= 30; i++ {
		lines = append(lines, fmt.Sprintf("line-%02d", i))
	}
	e := editor{text: strings.Join(lines, "\n"), cursor: len(strings.Join(lines, "\n"))}
	e.ensureVisible(20, 4)

	visible := ansi.Strip(strings.Join(e.viewLinesCursor(20, 4, true), "\n"))
	if !strings.Contains(visible, "line-30") || strings.Contains(visible, "line-01") {
		t.Fatalf("long draft did not follow the cursor: %q", visible)
	}

	e.cursor = 0
	e.ensureVisible(20, 4)
	visible = ansi.Strip(strings.Join(e.viewLinesCursor(20, 4, true), "\n"))
	if !strings.Contains(visible, "line-01") || strings.Contains(visible, "line-30") {
		t.Fatalf("cursor did not scroll back through the draft: %q", visible)
	}
}

func TestEditorWrapsInsteadOfTruncating(t *testing.T) {
	e := editor{}
	e.insert(strings.Repeat("wrap-this ", 12) + "final-marker")
	e.ensureVisible(18, 6)
	lines := e.viewLinesCursor(18, 6, true)
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
		visible := ansi.Strip(strings.Join(e.viewLinesCursor(width, 2, true), "\n"))
		if !strings.Contains(visible, "▏") {
			t.Fatalf("cursor row not visible after up: %q", visible)
		}
	}
}

func TestComposerNewlineAndSubmitKeepFullPrompt(t *testing.T) {
	s := newTestService(t)
	m := model{service: s, workspace: t.TempDir(), width: 100, height: 30, input: composer{kind: newTaskInput}}
	m = key(m, runeKey("first line"))
	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = key(m, runeKey("second line"))
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

func TestEmptyComposerDoesNotAssumeATaskDraft(t *testing.T) {
	m := model{input: composer{kind: reportInput}}
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.notice.text != "Enter text first." || m.input.kind != reportInput {
		t.Fatalf("empty composer notice or draft changed: notice=%q input=%+v", m.notice.text, m.input)
	}
}

func TestRouterAltEnterDoesNotSubmit(t *testing.T) {
	configureTestProvider(t)
	s := newTestService(t)
	s.RegisterRunner("general", routerRunner{})
	m := model{service: s, workspace: t.TempDir(), width: 80, height: 24, router: routerState{open: true}}
	m = key(m, runeKey("first"))
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	if m.router.text != "first\n" || len(s.ListTasks()) != 0 {
		t.Fatalf("alt+enter submitted instead of inserting a newline: draft=%q tasks=%+v", m.router.text, s.ListTasks())
	}
	m = key(m, runeKey("second"))
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	tasks := s.ListTasks()
	if len(tasks) != 1 || tasks[0].Prompt != "first\nsecond" || m.router.text != "" {
		t.Fatalf("enter did not submit the complete draft: draft=%q tasks=%+v", m.router.text, tasks)
	}
}

func TestComposerPageKeysScrollLongDraft(t *testing.T) {
	var lines []string
	for i := 1; i <= 30; i++ {
		lines = append(lines, fmt.Sprintf("line-%02d", i))
	}
	text := strings.Join(lines, "\n")
	m := model{width: 80, height: 24, input: composer{kind: newTaskInput, editor: editor{text: text, cursor: len(text)}}}
	before := ansi.Strip(strings.Join(m.composerBlock(m.composerHeight()), "\n"))
	if !strings.Contains(before, "line-30") || strings.Contains(before, "line-01") {
		t.Fatalf("long composer did not open at the cursor: %q", before)
	}
	for i := 0; i < 6; i++ {
		m = key(m, tea.KeyMsg{Type: tea.KeyPgUp})
	}
	after := ansi.Strip(strings.Join(m.composerBlock(m.composerHeight()), "\n"))
	if !strings.Contains(after, "line-01") || strings.Contains(after, "line-30") {
		t.Fatalf("pgup did not scroll the composer: %q", after)
	}
}

func boardWithTask() model {
	tasks := []orchestrator.Task{{ID: "task", Title: "work", Prompt: "do the work", Agent: "general", Status: orchestrator.Running}}
	return model{width: 100, height: 28, tasks: tasks, service: &fakeLoginService{tasks: tasks}}
}

// boxRows returns the stripped rows of the docked input box: the last bordered
// block in the view, from its top edge to its bottom edge.
func boxRows(t *testing.T, view string) []string {
	t.Helper()
	rows := strings.Split(ansi.Strip(view), "\n")
	start := -1
	for i, row := range rows {
		if strings.HasPrefix(strings.TrimSpace(row), "╭") {
			start = i
		}
	}
	if start < 0 {
		t.Fatalf("no input box in view: %q", view)
	}
	for i := start; i < len(rows); i++ {
		if strings.HasPrefix(strings.TrimSpace(rows[i]), "╰") {
			return rows[start : i+1]
		}
	}
	t.Fatalf("input box has no bottom border: %q", view)
	return nil
}

func TestComposerBoxGrowsWithTheDraftWithinItsLimit(t *testing.T) {
	m := boardWithTask()
	m.input = composer{kind: newTaskInput}
	if got := m.composerDraftRows(1); got != 2 {
		t.Fatalf("empty draft rows = %d, want the two-row floor", got)
	}
	for lines, want := range map[int]int{1: 2, 3: 3, composerMaxLines + 20: composerMaxLines} {
		text := strings.Repeat("line\n", lines-1) + "line"
		m.input = composer{kind: newTaskInput, editor: editor{text: text, cursor: len(text)}}
		if got := m.composerDraftRows(1); got != want {
			t.Fatalf("%d draft lines: rows = %d, want %d", lines, got, want)
		}
	}
}

// Every composer and the request form keep a full-width box with the cursor
// row and the draft's end visible, at every size, even under a notice.
func TestComposerBoxFitsEverySurfaceAndSize(t *testing.T) {
	text := strings.Repeat("long draft text ", 12) + "final-marker"
	kinds := []inputKind{"", newTaskInput, relatedTaskInput, subagentInput, threadInput, followUpInput, reportInput, rejectReviewInput}
	for _, size := range [][2]int{{120, 35}, {80, 24}, {50, 20}, {40, 12}, {30, 10}} {
		for _, kind := range kinds {
			m := boardWithTask()
			m.width, m.height = size[0], size[1]
			m.notice.text = "Enter a reason to reject; the worktree is kept for F follow-up."
			if kind == "" {
				m.router = routerState{open: true, editor: editor{text: text, cursor: len(text)}}
			} else {
				m.input = composer{kind: kind, target: "task", reviewCommit: "abc", editor: editor{text: text, cursor: len(text)}}
			}
			view := m.View()
			assertFits(t, view, size[0], size[1])
			box := boxRows(t, view)
			for _, row := range box {
				if lipgloss.Width(row) != size[0] {
					t.Fatalf("%v %q: box row is %d cells: %q", size, kind, lipgloss.Width(row), row)
				}
			}
			if joined := strings.Join(box, "\n"); !strings.Contains(joined, "▏") || !strings.Contains(strings.ReplaceAll(quitPlain(joined), " ", ""), "final-marker") || strings.Contains(joined, "…") {
				t.Fatalf("%v %q: box lost the cursor row or wrapped badly: %#v", size, kind, box)
			}
			if kind != "" && size[0] != 50 && !strings.Contains(quitPlain(view), "esc cancel") {
				t.Fatalf("%v %q: composer hints were clipped", size, kind)
			}
		}
	}
	m := boardWithTask()
	m.width, m.height = 30, 10
	m.router = routerState{open: true, editor: editor{text: "@gen", cursor: 4}}
	if plain := ansi.Strip(m.View()); !strings.Contains(plain, "No registered executor") {
		t.Fatalf("mention hint row was clipped: %q", plain)
	}
}

func TestComposerBoxLabelsPlaceholderAndFocus(t *testing.T) {
	m := boardWithTask()
	m.input = composer{kind: followUpInput, target: "task", editor: editor{text: "more context", cursor: len("more context")}}
	plain := ansi.Strip(m.View())
	box := boxRows(t, m.View())
	if !strings.Contains(box[0], "FOLLOW UP") || !strings.Contains(box[1], "› more context") {
		t.Fatalf("box lost its label or draft: %#v", box)
	}
	if footer := ansi.Strip(m.footer(m.width)); strings.Contains(footer, "more context") || !strings.Contains(footer, "enter submit") {
		t.Fatalf("footer must carry hints only: %q", footer)
	}
	if panel, at, hints := strings.Index(plain, "╰"), strings.Index(plain, box[0]), strings.Index(plain, "enter submit"); !(panel < at && at < hints) {
		t.Fatalf("box is not docked between the body and the hints: %q", plain)
	}
	m.input = composer{kind: newTaskInput}
	if row := boxRows(t, m.View())[1]; !strings.Contains(row, "› ") || !strings.Contains(row, "Describe the task") || !strings.Contains(row, "▏") {
		t.Fatalf("empty composer lost its prompt, placeholder or cursor: %q", row)
	}
	m.router = routerState{open: true, editor: editor{text: "hello", cursor: 5}}
	request := ansi.Strip(strings.Join(m.composerBlock(2), "\n"))
	if !strings.Contains(request, "NEW REQUEST") || !strings.Contains(request, "authorization "+orchestrator.LocalPolicyVersion) || !strings.Contains(request, "▏") {
		t.Fatalf("request box lost its label, note or cursor: %q", request)
	}
	m.router.focus = routerFocusRecent
	if box := ansi.Strip(strings.Join(m.composerBlock(2), "\n")); strings.Contains(box, "▏") || !strings.Contains(box, "hello") || m.composerFocused() {
		t.Fatalf("unfocused request box still draws a cursor or lost the draft: %q", box)
	}
	m.width, m.router.focus = 30, routerFocusComposer
	if narrow := ansi.Strip(strings.Join(m.composerBlock(2), "\n")); !strings.Contains(narrow, "NEW REQUEST") || strings.Contains(narrow, "authorization") {
		t.Fatalf("narrow request box label or note is wrong: %q", narrow)
	}
	m.router = routerState{}
	m.input = composer{kind: newTaskInput}
	if m = key(m, runeKey("a")); key(m, tea.KeyMsg{Type: tea.KeyEnter, Alt: true}).input.text != "a\n" {
		t.Fatal("alt+enter did not insert a newline")
	}
}
