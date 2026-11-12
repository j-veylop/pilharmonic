package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
)

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
	m.router = true
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
		m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("word ")})
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

func TestRequestFormBoxCarriesItsPolicyAndFocus(t *testing.T) {
	m := boardWithTask()
	m.router = true
	m.routerDraft = "inspect the parser"
	box := ansi.Strip(strings.Join(m.composerBox(m.width, 2), "\n"))
	if !strings.Contains(box, "NEW REQUEST") || !strings.Contains(box, "policy "+orchestrator.LocalPolicyVersion) {
		t.Fatalf("request box lost its label or policy: %q", box)
	}
	if !m.composerFocused() {
		t.Fatal("request box reports no focus while the draft is active")
	}
	m.routerFocus = 1
	if m.composerFocused() {
		t.Fatal("request box still reports focus while the attention list owns keys")
	}
	// The label must survive a narrow terminal without the policy note.
	narrow := ansi.Strip(strings.Join(m.composerBox(30, 2), "\n"))
	if !strings.Contains(narrow, "NEW REQUEST") || strings.Contains(narrow, "policy") {
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
	m.router = true
	m.routerDraft = "hello"
	m.routerCursor = len("hello")
	if box := strings.Join(m.composerBox(m.width, 2), "\n"); !strings.Contains(box, "▏") {
		t.Fatalf("focused request box has no cursor: %q", box)
	}
	m.routerFocus = 1
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
	m.notice = "Enter a reason to reject; the worktree is kept for F follow-up."
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
	m.router = true
	m.service = nil
	m.routerDraft = "@gen"
	m.routerCursor = len("@gen")
	view := m.View()
	if width, height := lipgloss.Width(view), lipgloss.Height(view); width > 30 || height > 10 {
		t.Fatalf("rendered %dx%d", width, height)
	}
	plain := ansi.Strip(view)
	if !strings.Contains(plain, "No registered executor") {
		t.Fatalf("mention hint was clipped: %q", plain)
	}
}
