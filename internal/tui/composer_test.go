package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
	"pilharmonic/internal/tui/editor"
)

func TestRequestBoxHoldQueuesWithoutStarting(t *testing.T) {
	configureTestProvider(t)
	s := newTestService(t)
	s.RegisterRunner("general", routerRunner{})
	m := model{service: s, workspace: t.TempDir(), width: 100, height: 30, router: routerState{open: true}}
	m = key(m, runeKey("first line"))
	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = key(m, runeKey("second line"))
	if m.router.Text != "first line\nsecond line" {
		t.Fatalf("ctrl+j did not insert a newline: %q", m.router.Text)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlS})
	tasks := s.ListTasks()
	if len(tasks) != 1 || tasks[0].Prompt != "first line\nsecond line" || tasks[0].Status != orchestrator.Queued || tasks[0].ScheduleRequested {
		t.Fatalf("ctrl+s did not hold the prompt queued and unstarted: %+v", tasks)
	}
	if m.router.Text != "" || !m.router.open || m.selectedID() != tasks[0].ID || !strings.Contains(m.notice.text, "r to start") {
		t.Fatalf("hold kept the draft or lost the box, selection or notice: %+v %q", m.router, m.notice.text)
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
	if m.router.Text != "first\n" || len(s.ListTasks()) != 0 {
		t.Fatalf("alt+enter submitted instead of inserting a newline: draft=%q tasks=%+v", m.router.Text, s.ListTasks())
	}
	m = key(m, runeKey("second"))
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	tasks := s.ListTasks()
	if len(tasks) != 1 || tasks[0].Prompt != "first\nsecond" || m.router.Text != "" {
		t.Fatalf("enter did not submit the complete draft: draft=%q tasks=%+v", m.router.Text, tasks)
	}
}

func TestComposerPageKeysScrollLongDraft(t *testing.T) {
	var lines []string
	for i := 1; i <= 30; i++ {
		lines = append(lines, fmt.Sprintf("line-%02d", i))
	}
	text := strings.Join(lines, "\n")
	m := model{width: 80, height: 24, input: composer{kind: reportInput, Editor: editor.Editor{Text: text, Cursor: len(text)}}}
	before := ansi.Strip(strings.Join(m.composerBlock(m.composerHeight()), "\n"))
	if !strings.Contains(before, "line-30") || strings.Contains(before, "line-01") {
		t.Fatalf("long composer did not open at the cursor: %q", before)
	}
	for i := 0; i < 30; i++ {
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

// boxRows returns the stripped rows of the docked composer: its title row and
// draft, without the completion popups drawn above it.
func boxRows(t *testing.T, m model) []string {
	t.Helper()
	rows := m.composerBlock(m.composerHeight())
	for i := range rows {
		rows[i] = ansi.Strip(rows[i])
	}
	if len(rows) == 0 {
		t.Fatal("no composer in view")
	}
	return rows
}

func TestComposerBoxAlwaysKeepsThreeDraftRows(t *testing.T) {
	m := boardWithTask()
	m.input = composer{kind: reportInput}
	if got := m.composerDraftRows(); got != 3 {
		t.Fatalf("empty draft rows = %d, want 3", got)
	}
	for _, lines := range []int{1, 3, 9, 40} {
		text := strings.Repeat("line\n", lines-1) + "line"
		m.input = composer{kind: reportInput, Editor: editor.Editor{Text: text, Cursor: len(text)}}
		if got := m.composerDraftRows(); got != 3 {
			t.Fatalf("%d draft lines: rows = %d, want 3", lines, got)
		}
	}
	for _, size := range [][2]int{{120, 35}, {40, 12}, {30, 10}} {
		m.width, m.height = size[0], size[1]
		if got := m.composerDraftRows(); got != 3 {
			t.Fatalf("%v: rows = %d, want 3", size, got)
		}
	}
	m.width, m.height = 100, 28
	m.input = composer{kind: reportInput}
	if rows := m.composerBlock(m.composerHeight()); len(rows) != 1+composerDraftHeight {
		t.Fatalf("empty box renders %d rows, want a rule and 3 draft rows", len(rows))
	}
	m.input = composer{kind: reportInput, Editor: editor.Editor{Text: "one line", Cursor: len("one line")}}
	if rows := m.composerBlock(m.composerHeight()); len(rows) != 1+composerDraftHeight {
		t.Fatalf("short draft box renders %d rows, want a rule and 3 draft rows", len(rows))
	}
}

// Every composer and the request form keep a full-width box with the cursor
// row and the draft's end visible, at every size, even under a notice.
func TestComposerBoxFitsEverySurfaceAndSize(t *testing.T) {
	text := strings.Repeat("long draft text ", 12) + "final-marker"
	kinds := []inputKind{"", relatedTaskInput, subagentInput, threadInput, followUpInput, reportInput, rejectReviewInput}
	for _, size := range [][2]int{{120, 35}, {80, 24}, {50, 20}, {40, 12}, {30, 10}} {
		for _, kind := range kinds {
			m := boardWithTask()
			m.width, m.height = size[0], size[1]
			m.notice.text = "Enter a reason to reject; the worktree is kept for f follow-up."
			if kind == "" {
				m.router = routerState{open: true, Editor: editor.Editor{Text: text, Cursor: len(text)}}
			} else {
				m.input = composer{kind: kind, target: "task", reviewCommit: "abc", Editor: editor.Editor{Text: text, Cursor: len(text)}}
			}
			view := m.View()
			assertFits(t, view, size[0], size[1])
			box := boxRows(t, m)
			for _, row := range box {
				if lipgloss.Width(row) > size[0] {
					t.Fatalf("%v %q: box row is %d cells: %q", size, kind, lipgloss.Width(row), row)
				}
			}
			if joined := strings.Join(box, "\n"); !strings.Contains(strings.ReplaceAll(quitPlain(joined), " ", ""), "final-marker") || strings.Contains(joined, "…") {
				t.Fatalf("%v %q: box lost the cursor row or wrapped badly: %#v", size, kind, box)
			}
			if kind != "" && size[0] != 50 && !strings.Contains(quitPlain(view), "esc cancel") {
				t.Fatalf("%v %q: composer hints were clipped", size, kind)
			}
		}
	}
	m := boardWithTask()
	m.width, m.height = 30, 10
	m.router = routerState{open: true, Editor: editor.Editor{Text: "@gen", Cursor: 4}}
	if plain := ansi.Strip(m.View()); !strings.Contains(plain, "No registered executor") {
		t.Fatalf("mention hint row was clipped: %q", plain)
	}
}

func TestComposerBoxLabelsPlaceholderAndFocus(t *testing.T) {
	m := boardWithTask()
	m.input = composer{kind: followUpInput, target: "task", Editor: editor.Editor{Text: "more context", Cursor: len("more context")}}
	plain := ansi.Strip(m.View())
	box := boxRows(t, m)
	if !strings.Contains(box[1], "more context") {
		t.Fatalf("box lost its prompt or draft: %#v", box)
	}
	if footer := ansi.Strip(m.footer(m.width)); strings.Contains(footer, "more context") || !strings.Contains(footer, "enter submit") {
		t.Fatalf("footer must carry hints only: %q", footer)
	}
	if at, hints := strings.Index(plain, "more context"), strings.Index(plain, "enter submit"); !(at >= 0 && at < hints) {
		t.Fatalf("composer is not docked above the hints: %q", plain)
	}
	m.input = composer{kind: relatedTaskInput}
	if row := boxRows(t, m)[1]; strings.TrimSpace(row) != "" {
		t.Fatalf("focused empty composer shows placeholder: %q", row)
	}
	m.input = composer{}
	if row := boxRows(t, m)[1]; !strings.Contains(row, "Describe the work") {
		t.Fatalf("unfocused empty composer lost placeholder: %q", row)
	}
	m.router.open = true
	if row := boxRows(t, m)[1]; strings.TrimSpace(row) != "" {
		t.Fatalf("focused empty request box shows placeholder: %q", row)
	}
	m.router = routerState{open: true, Editor: editor.Editor{Text: "hello", Cursor: 5}}
	request := ansi.Strip(strings.Join(m.composerBlock(2), "\n"))
	if !strings.Contains(request, "hello") {
		t.Fatalf("request box lost its draft: %q", request)
	}
	m.width = 30
	if narrow := ansi.Strip(strings.Join(m.composerBlock(2), "\n")); !strings.Contains(narrow, "hello") {
		t.Fatalf("narrow request box label or note is wrong: %q", narrow)
	}
	m.router = routerState{}
	m.input = composer{kind: reportInput}
	if m = key(m, runeKey("a")); key(m, tea.KeyMsg{Type: tea.KeyEnter, Alt: true}).input.Text != "a\n" {
		t.Fatal("alt+enter did not insert a newline")
	}
}
