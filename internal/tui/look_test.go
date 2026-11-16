package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
	"pilharmonic/internal/tui/editor"
)

func TestStatusPillsShowModeAndModel(t *testing.T) {
	m := boardWithTask()
	m.service = &fakeLoginService{tasks: m.tasks, overrides: orchestrator.ModelSelection{Big: "openai-subscription/gpt-6.1-sol"}}
	plain := ansi.Strip(m.statusPills(120))
	if !strings.Contains(plain, "TASKS") || !strings.Contains(plain, "gpt-6.1-sol") || strings.Contains(plain, "openai-subscription/") {
		t.Fatalf("status pills = %q", plain)
	}
	m.router.open = true
	if plain := ansi.Strip(m.statusPills(120)); !strings.Contains(plain, "REQUEST") {
		t.Fatalf("request mode missing: %q", plain)
	}
	narrow := ansi.Strip(m.statusPills(12))
	if !strings.Contains(narrow, "REQUEST") || strings.Contains(narrow, "gpt-6.1-sol") {
		t.Fatalf("narrow pills kept the model: %q", narrow)
	}
}

func TestStatusPillsRespectShortTerminals(t *testing.T) {
	m := boardWithTask()
	m.width, m.height = 80, 12
	if strings.Contains(ansi.Strip(m.footer(80)), "TASKS") {
		t.Fatal("short terminal lost a body row to pills")
	}
	m.height = 24
	if !strings.Contains(ansi.Strip(m.footer(80)), "TASKS") {
		t.Fatal("tall terminal omitted the status pills")
	}
}

func TestSidebarToggle(t *testing.T) {
	m := boardWithTask()
	m.width, m.height = 100, 28
	if !m.layout().sidebar {
		t.Fatal("wide layout hid the sidebar by default")
	}
	m, _ = m.boardKey("B")
	if m.layout().sidebar {
		t.Fatal("B did not hide the sidebar")
	}
	if !strings.Contains(ansi.Strip(m.View()), "PILHARMONIC") {
		t.Fatal("hiding the sidebar lost the header")
	}
}

func TestLandingShownWithoutTasks(t *testing.T) {
	m := model{width: 100, height: 28}
	plain := ansi.Strip(m.View())
	for _, want := range []string{"PILHARMONIC", "submit a request", "/model"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("landing missing %q: %q", want, plain)
		}
	}
	m.tasks = []orchestrator.Task{{ID: "task", Title: "work", Status: orchestrator.Queued}}
	if plain := ansi.Strip(m.View()); strings.Contains(plain, "submit a request") {
		t.Fatal("landing stayed over a non-empty board")
	}
}

func TestCompletionPopupSitsAboveComposer(t *testing.T) {
	m := boardWithTask()
	m.router = routerState{open: true, Editor: editor.Editor{Text: "@ge", Cursor: 3}}
	m.width, m.height = 100, 28
	rows := m.composerBlock(m.composerHeight())
	joined := ansi.Strip(strings.Join(rows, "\n"))
	if !strings.Contains(joined, "No registered executor") || !strings.Contains(joined, "╭") {
		t.Fatalf("completion popup missing: %q", joined)
	}
	if strings.Index(joined, "No registered executor") > strings.Index(joined, "REQUEST") {
		t.Fatalf("completion popup is not above the box: %q", joined)
	}
}

func TestToolRowsShowStatusWord(t *testing.T) {
	lines := toolCallLines(transcriptToolCall{status: orchestrator.ActivityFinished, summary: "read: main.go"}, 60)
	if len(lines) != 1 || !strings.Contains(ansi.Strip(lines[0]), "DONE") {
		t.Fatalf("tool row = %q", lines)
	}
	if rows := toolCallLines(transcriptToolCall{status: orchestrator.ActivityFinished, summary: "read"}, 8); strings.Contains(ansi.Strip(strings.Join(rows, "\n")), "DONE") {
		t.Fatal("narrow tool row kept a status word")
	}
}

func TestRequestBoxStaysVisibleWhenUnfocused(t *testing.T) {
	m := boardWithTask()
	m.router = routerState{Editor: editor.Editor{Text: "kept draft", Cursor: len("kept draft")}}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "REQUEST") || !strings.Contains(view, "kept draft") || !strings.Contains(view, "i or / to type") {
		t.Fatalf("unfocused request box missing: %q", view)
	}
}

func TestEscBlursAndIKeepsDraft(t *testing.T) {
	m := boardWithTask()
	m.router = routerState{open: true, Editor: editor.Editor{Text: "draft", Cursor: 5}}
	m = key(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.router.open || m.router.Text != "draft" {
		t.Fatalf("esc changed focus or draft: open=%v text=%q", m.router.open, m.router.Text)
	}
	m = key(m, runeKey("x"))
	if m.router.Text != "draft" {
		t.Fatalf("blurred request box took typing: %q", m.router.Text)
	}
	m = key(m, runeKey("i"))
	if !m.router.open || m.router.Text != "draft" {
		t.Fatalf("i did not focus the draft: open=%v text=%q", m.router.open, m.router.Text)
	}
}

func TestBlurredBoxKeepsBoardShortcuts(t *testing.T) {
	m := boardWithTask()
	m.router = routerState{}
	m = key(m, runeKey("q"))
	if !m.quitConfirm {
		t.Fatal("board shortcuts stopped working while the box was visible")
	}
}

// The footer keeps its height, so nothing above it moves when a notice appears
// or expires and when the selection moves between tasks with different actions.
func TestFooterKeepsItsHeight(t *testing.T) {
	tasks := []orchestrator.Task{
		{ID: "run", Title: "run", Status: orchestrator.Running},
		{ID: "done", Title: "done", Status: orchestrator.Completed},
	}
	for _, size := range [][2]int{{40, 24}, {80, 24}, {120, 35}} {
		w, h := size[0], size[1]
		reference := model{width: w, height: h, tasks: tasks}
		rows, body := strings.Count(reference.footer(w), "\n"), reference.layout().bodyHeight
		notices := []noticeState{
			{},
			{text: "Task queued"},
			{service: "State save failed: disk full"},
			{text: "Task queued", service: "State save failed: disk full"},
		}
		for _, selected := range []int{0, 1} {
			for _, notice := range notices {
				m := model{width: w, height: h, tasks: tasks, selected: selected, notice: notice}
				if got := strings.Count(m.footer(w), "\n"); got != rows {
					t.Fatalf("%v selection %d notice %+v: footer is %d rows, want %d", size, selected, notice, got+1, rows+1)
				}
				if got := m.layout().bodyHeight; got != body {
					t.Fatalf("%v selection %d notice %+v: body height %d, want %d", size, selected, notice, got, body)
				}
			}
		}
	}
}

// The status bar carries no animation: the row keeps its width while tasks run.
func TestStatusPillsCarryNoAnimation(t *testing.T) {
	m := boardWithTask()
	idle := ansi.Strip(m.statusPills(120))
	m.spinnerActive = true
	if got := ansi.Strip(m.statusPills(120)); got != idle {
		t.Fatalf("spinner changed the status row: %q → %q", idle, got)
	}
}

// The header leads with one flat wordmark rather than a per-rune gradient.
func TestHeaderWordmarkIsFlat(t *testing.T) {
	if header := (model{}).headerRow(120); !strings.HasPrefix(header, wordmark()) {
		t.Fatalf("header does not lead with the flat wordmark: %q", header)
	}
}
