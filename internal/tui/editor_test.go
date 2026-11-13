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

func TestComposerBoxWrapsLongDraft(t *testing.T) {
	text := strings.Repeat("keep this visible ", 8) + "final-marker"
	m := model{width: 48, height: 24, input: composer{kind: newTaskInput, text: text, cursor: len(text)}}
	box := ansi.Strip(strings.Join(m.composerBox(48, m.composerDraftRows(1)), "\n"))
	if strings.Count(box, "\n") < 3 {
		t.Fatalf("composer box stayed single-line: %q", box)
	}
	if !strings.Contains(box, "final-marker") {
		t.Fatalf("end of long input is not visible: %q", box)
	}
	if strings.Contains(box, "…") {
		t.Fatalf("composer box truncated the draft: %q", box)
	}
	for _, line := range strings.Split(box, "\n") {
		if lipgloss.Width(line) > 48 {
			t.Fatalf("box line overflows terminal width: %q", line)
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

func TestComposerAltEnterInsertsNewline(t *testing.T) {
	m := model{width: 100, height: 30, input: composer{kind: newTaskInput}}
	m = key(m, runeKey("first"))
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	m = key(m, runeKey("second"))
	if m.input.text != "first\nsecond" {
		t.Fatalf("alt+enter did not insert a newline: %q", m.input.text)
	}
}

func TestEmptyComposerDoesNotAssumeATaskDraft(t *testing.T) {
	m := model{input: composer{kind: reportInput}}
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.notice.text != "Enter text first." || m.input.kind != reportInput {
		t.Fatalf("empty composer notice or draft changed: notice=%q input=%+v", m.notice.text, m.input)
	}
}

func TestRouterComposerWrapsAndSubmitsMultilineRequest(t *testing.T) {
	configureTestProvider(t)
	s := newTestService(t)
	m := model{service: s, workspace: t.TempDir(), width: 60, height: 35, router: routerState{open: true}}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("long request ", 8))})
	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = key(m, runeKey("final detail"))
	if strings.Count(m.router.draft, "\n") != 1 {
		t.Fatalf("router draft did not keep its newline: %q", m.router.draft)
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
	configureTestProvider(t)
	s := newTestService(t)
	s.RegisterRunner("general", routerRunner{})
	m := model{service: s, workspace: t.TempDir(), width: 80, height: 24, router: routerState{open: true}}
	m = key(m, runeKey("first"))
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	if m.router.draft != "first\n" || len(s.ListTasks()) != 0 {
		t.Fatalf("alt+enter submitted instead of inserting a newline: draft=%q tasks=%+v", m.router.draft, s.ListTasks())
	}
	m = key(m, runeKey("second"))
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	tasks := s.ListTasks()
	if len(tasks) != 1 || tasks[0].Prompt != "first\nsecond" || m.router.draft != "" {
		t.Fatalf("enter did not submit the complete draft: draft=%q tasks=%+v", m.router.draft, tasks)
	}
}

func TestComposerPageKeysScrollLongDraft(t *testing.T) {
	var lines []string
	for i := 1; i <= 30; i++ {
		lines = append(lines, fmt.Sprintf("line-%02d", i))
	}
	text := strings.Join(lines, "\n")
	m := model{width: 80, height: 24, input: composer{kind: newTaskInput, text: text, cursor: len(text)}}
	before := ansi.Strip(strings.Join(m.composerBox(80, m.composerHeight()), "\n"))
	if !strings.Contains(before, "line-30") || strings.Contains(before, "line-01") {
		t.Fatalf("long composer did not open at the cursor: %q", before)
	}
	for i := 0; i < 6; i++ {
		m = key(m, tea.KeyMsg{Type: tea.KeyPgUp})
	}
	after := ansi.Strip(strings.Join(m.composerBox(80, m.composerHeight()), "\n"))
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
		m := model{width: size[0], height: size[1], router: routerState{open: true, draft: text, cursor: len(text)}}
		view := m.View()
		if width, height := lipgloss.Width(view), lipgloss.Height(view); width > size[0] || height > size[1] {
			t.Fatalf("%v: rendered %dx%d", size, width, height)
		}
		if !strings.Contains(ansi.Strip(view), "final-detail") {
			t.Fatalf("%v: router draft missing: %s", size, ansi.Strip(view))
		}
	}
}

func boardWithTask() model {
	return model{width: 100, height: 28, tasks: []orchestrator.Task{{
		ID: "task", Title: "work", Prompt: "do the work", Agent: "general", Status: orchestrator.Running,
	}}}
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

func TestComposerBoxDocksBelowThePanelsAndAboveTheHints(t *testing.T) {
	m := boardWithTask()
	m.input = composer{kind: followUpInput, target: "task", text: "more context", cursor: len("more context")}
	plain := ansi.Strip(m.View())

	box := boxRows(t, m.View())
	if len(box) < 3 {
		t.Fatalf("input box has no body: %#v", box)
	}
	if title := strings.TrimSpace(box[0]); !strings.Contains(title, "FOLLOW UP") {
		t.Fatalf("box title missing: %q", title)
	}
	if !strings.Contains(box[1], "› more context") {
		t.Fatalf("draft is not inside the box: %q", box[1])
	}
	boxStart := strings.Index(plain, box[0])
	hints := strings.Index(plain, "enter submit")
	panel := strings.Index(plain, "╰")
	if boxStart < 0 || hints < 0 || panel < 0 {
		t.Fatalf("view misses the box, hints or panel: %q", plain)
	}
	if !(panel < boxStart && boxStart < hints) {
		t.Fatalf("box is not docked between the body and the hints: %q", plain)
	}
}

func TestComposerBoxShowsPromptAndPlaceholder(t *testing.T) {
	m := boardWithTask()
	m.input = composer{kind: newTaskInput}
	box := boxRows(t, m.View())
	if len(box) < 3 {
		t.Fatalf("empty composer has no box: %#v", box)
	}
	row := ansi.Strip(box[1])
	if !strings.Contains(row, "› ") || !strings.Contains(row, "Describe the task") {
		t.Fatalf("empty composer lost its prompt or placeholder: %q", row)
	}
	if !strings.Contains(row, "▏") {
		t.Fatalf("empty composer does not show the cursor: %q", row)
	}
	m.router.open = true
	routerBox := boxRows(t, m.View())
	if row := ansi.Strip(routerBox[1]); !strings.Contains(row, "Describe the work") {
		t.Fatalf("request form lost its placeholder: %q", row)
	}
}

func TestComposerBoxGrowsWithTheDraftWithinItsLimit(t *testing.T) {
	m := boardWithTask()
	m.input = composer{kind: newTaskInput}
	if got := m.composerDraftRows(1); got != 2 {
		t.Fatalf("empty draft rows = %d, want the two-row floor", got)
	}
	for lines, want := range map[int]int{1: 2, 3: 3, composerMaxLines + 20: composerMaxLines} {
		text := strings.Repeat("line\n", lines-1) + "line"
		m.input = composer{kind: newTaskInput, text: text, cursor: len(text)}
		if got := m.composerDraftRows(1); got != want {
			t.Fatalf("%d draft lines: rows = %d, want %d", lines, got, want)
		}
	}
}

func TestComposerBoxFitsTerminalForEveryComposer(t *testing.T) {
	kinds := []inputKind{newTaskInput, relatedTaskInput, subagentInput, threadInput, followUpInput, reportInput, rejectReviewInput}
	for _, size := range [][2]int{{120, 35}, {80, 24}, {50, 20}, {40, 12}, {30, 10}} {
		for _, kind := range kinds {
			text := strings.Repeat("long draft text ", 12)
			m := boardWithTask()
			m.width, m.height = size[0], size[1]
			m.input = composer{kind: kind, target: "task", text: text, cursor: len(text), reviewCommit: "abc"}
			view := m.View()
			if width, height := lipgloss.Width(view), lipgloss.Height(view); width > size[0] || height > size[1] {
				t.Fatalf("%v %s: rendered %dx%d", size, kind, width, height)
			}
			box := boxRows(t, view)
			if len(box) == 0 {
				t.Fatalf("%v %s: no input box: %s", size, kind, ansi.Strip(view))
			}
			for _, row := range box {
				if lipgloss.Width(row) != size[0] {
					t.Fatalf("%v %s: box row is %d cells: %q", size, kind, lipgloss.Width(row), row)
				}
			}
		}
	}
}

func TestComposerBoxKeepsTheCursorRowVisible(t *testing.T) {
	m := boardWithTask()
	m.input = composer{kind: newTaskInput}
	for i := 0; i < 4; i++ {
		m = key(m, runeKey("word "))
		m = key(m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	}
	for _, size := range [][2]int{{120, 35}, {60, 20}, {40, 12}, {30, 10}} {
		m.width, m.height = size[0], size[1]
		box := boxRows(t, m.View())
		if len(box) == 0 {
			t.Fatalf("%v: no box", size)
		}
		if !strings.Contains(strings.Join(box, "\n"), "▏") {
			t.Fatalf("%v: cursor row missing from the box: %#v", size, box)
		}
	}
}

func TestRequestFormBoxCarriesItsAuthorizationAndFocus(t *testing.T) {
	m := boardWithTask()
	m.router.open = true
	m.router.draft = "inspect the parser"
	box := ansi.Strip(strings.Join(m.composerBox(m.width, 2), "\n"))
	if !strings.Contains(box, "NEW REQUEST") || !strings.Contains(box, "authorization "+orchestrator.LocalPolicyVersion) {
		t.Fatalf("request box lost its label or authorization note: %q", box)
	}
	if !m.composerFocused() {
		t.Fatal("request box reports no focus while the draft is active")
	}
	m.router.focus = 1
	if m.composerFocused() {
		t.Fatal("request box still reports focus while the attention list owns keys")
	}
	// The label must survive a narrow terminal without the authorization note.
	narrow := ansi.Strip(strings.Join(m.composerBox(30, 2), "\n"))
	if !strings.Contains(narrow, "NEW REQUEST") || strings.Contains(narrow, "authorization") {
		t.Fatalf("narrow request box label or note is wrong: %q", narrow)
	}
	for _, row := range strings.Split(narrow, "\n") {
		if lipgloss.Width(row) != 30 {
			t.Fatalf("narrow box row is %d cells: %q", lipgloss.Width(row), row)
		}
	}
}

func TestComposerGeometryMatchesTheRenderedBox(t *testing.T) {
	m := boardWithTask()
	m.input = composer{kind: newTaskInput, text: strings.Repeat("x", 200), cursor: 200}
	for _, width := range []int{30, 50, 80, 120} {
		m.width = width
		if got, want := m.composerWidth(), width-4-lipgloss.Width(composerPrompt)-1; got != want {
			t.Fatalf("width %d: draft width = %d, want %d", width, got, want)
		}
		box := m.composerBox(width, 2)
		if len(box) != 4 {
			t.Fatalf("width %d: box rows = %d", width, len(box))
		}
		for _, row := range box {
			if lipgloss.Width(row) != width {
				t.Fatalf("width %d: row is %d cells: %q", width, lipgloss.Width(row), ansi.Strip(row))
			}
		}
	}
}

func TestComposerFooterKeepsHintsOnly(t *testing.T) {
	m := boardWithTask()
	m.input = composer{kind: threadInput, target: "task", text: "draft text stays in the box", cursor: len("draft text stays in the box")}
	footer := ansi.Strip(m.footer(m.width))
	if strings.Contains(footer, "draft text stays") {
		t.Fatalf("footer still carries the draft: %q", footer)
	}
	if !strings.Contains(footer, "enter submit") || !strings.Contains(footer, "esc cancel") {
		t.Fatalf("footer lost the composer hints: %q", footer)
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "draft text stays in the box") {
		t.Fatalf("draft is missing from the view: %q", view)
	}
}

func TestComposerBoxHidesTheCursorWhenTheListHasFocus(t *testing.T) {
	m := boardWithTask()
	m.router.open = true
	m.router.draft = "hello"
	m.router.cursor = len("hello")
	if box := strings.Join(m.composerBox(m.width, 2), "\n"); !strings.Contains(box, "▏") {
		t.Fatalf("focused request box has no cursor: %q", box)
	}
	m.router.focus = 1
	box := strings.Join(m.composerBox(m.width, 2), "\n")
	if strings.Contains(box, "▏") {
		t.Fatalf("unfocused request box still draws a cursor: %q", box)
	}
	if !strings.Contains(ansi.Strip(box), "hello") {
		t.Fatalf("unfocused request box lost the draft: %q", box)
	}
}

func TestComposerBoxSurvivesANoticeOnAShortTerminal(t *testing.T) {
	m := boardWithTask()
	m.width, m.height = 30, 10
	m.input = composer{kind: rejectReviewInput, target: "task", reviewCommit: "abc"}
	m.notice.text = "Enter a reason to reject; the worktree is kept for F follow-up."
	view := m.View()
	if width, height := lipgloss.Width(view), lipgloss.Height(view); width > 30 || height > 10 {
		t.Fatalf("rendered %dx%d", width, height)
	}
	box := boxRows(t, view)
	if len(box) != 3 {
		t.Fatalf("short terminal box rows = %d: %#v", len(box), box)
	}
	if !strings.HasPrefix(strings.TrimSpace(box[len(box)-1]), "╰") {
		t.Fatalf("box lost its bottom border: %#v", box)
	}
	plain := ansi.Strip(view)
	if !strings.Contains(plain, "ctrl+j") || !strings.Contains(plain, "esc cancel") {
		t.Fatalf("composer hints were clipped: %q", plain)
	}
	if !strings.Contains(strings.Join(box, "\n"), "▏") {
		t.Fatalf("cursor was clipped: %#v", box)
	}
}

func TestComposerMentionHintRowIsReserved(t *testing.T) {
	m := boardWithTask()
	m.width, m.height = 30, 10
	m.router.open = true
	m.service = nil
	m.router.draft = "@gen"
	m.router.cursor = len("@gen")
	view := m.View()
	if width, height := lipgloss.Width(view), lipgloss.Height(view); width > 30 || height > 10 {
		t.Fatalf("rendered %dx%d", width, height)
	}
	plain := ansi.Strip(view)
	if !strings.Contains(plain, "No registered executor") {
		t.Fatalf("mention hint was clipped: %q", plain)
	}
}
