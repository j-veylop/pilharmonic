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

func TestViewFitsTerminal(t *testing.T) {
	for _, size := range [][2]int{{120, 35}, {80, 24}, {50, 20}, {30, 10}, {20, 5}} {
		m := model{width: size[0], height: size[1], workspace: "/a/very/long/path", tasks: []orchestrator.Task{{ID: "123", Title: "A very long task title", Prompt: "the original task", Status: orchestrator.Running, Agent: "general", Output: "working\nmore output"}}}
		for _, history := range []bool{false, true} {
			m.history = history
			view := m.View()
			if lipgloss.Height(view) > size[1] || lipgloss.Width(view) > size[0] {
				t.Fatalf("%v: rendered %dx%d", size, lipgloss.Width(view), lipgloss.Height(view))
			}
			if !strings.Contains(view, "ORCHESTRATOR") {
				t.Fatalf("missing header: %s", view)
			}
		}
	}
}

func TestViewWithToolOutputFitsTerminal(t *testing.T) {
	task := orchestrator.Task{ID: "tool", Title: "checks", Prompt: "run checks", Status: orchestrator.Running, Output: "# Update\n\nWorking.\n\n[running] bash: " + strings.Repeat("long-command ", 20) + "\n[finished] bash: go test ./...\n[failed] bash: go vet ./...\n\nDone.\n"}
	for _, size := range [][2]int{{120, 35}, {80, 24}, {50, 20}, {30, 10}} {
		m := model{width: size[0], height: size[1], tasks: []orchestrator.Task{task}}
		view := m.View()
		if lipgloss.Width(view) > size[0] || lipgloss.Height(view) > size[1] {
			t.Fatalf("%v: rendered %dx%d", size, lipgloss.Width(view), lipgloss.Height(view))
		}
	}
}

func TestHistoryShowsContextDelivery(t *testing.T) {
	m := model{width: 120, height: 35, history: true, tasks: []orchestrator.Task{{ID: "1", Title: "test", Prompt: "do work", Status: orchestrator.Running, Context: []orchestrator.Trigger{{ID: "trigger-1", Delivery: "pending"}, {ID: "trigger-2", Delivery: "rejected", DeliveryError: "not accepted"}}}}}
	view := m.View()
	for _, text := range []string{"trigger-1", "pending", "trigger-2", "rejected", "not accepted"} {
		if !strings.Contains(view, text) {
			t.Fatalf("missing %q in %s", text, view)
		}
	}
}

func TestHistoryShowsPerAttemptDeliveries(t *testing.T) {
	task := orchestrator.Task{
		ID: "1", Title: "test", Prompt: "do work", Status: orchestrator.Running,
		Context: []orchestrator.Trigger{{ID: "trigger-1", Delivery: orchestrator.DeliveryAccepted}},
		Deliveries: []orchestrator.Delivery{
			{TriggerID: "trigger-1", AttemptID: "attempt-old", State: orchestrator.DeliveryRejected, Error: "queue full"},
			{TriggerID: "trigger-1", AttemptID: "attempt-new", State: orchestrator.DeliveryAccepted},
		},
	}
	m := model{width: 120, height: 35, history: true, tasks: []orchestrator.Task{task}}
	view := m.View()
	for _, text := range []string{"DELIVERIES", "attempt-old", "rejected", "queue full", "attempt-new", "accepted"} {
		if !strings.Contains(view, text) {
			t.Fatalf("missing %q in %s", text, view)
		}
	}
}

func TestScheduledAndHistoryPagination(t *testing.T) {
	task := orchestrator.Task{ID: "task", ThreadID: "thread", Title: "example", Prompt: "work", Status: orchestrator.Queued, ScheduleRequested: true}
	for i := 0; i < 30; i++ {
		task.Events = append(task.Events, orchestrator.Event{Kind: fmt.Sprintf("event-%02d", i)})
	}
	m := model{width: 120, height: 22, history: true, tasks: []orchestrator.Task{task}}
	latest := m.View()
	if !strings.Contains(latest, "WAITING") || !strings.Contains(latest, "event-29") {
		t.Fatalf("missing waiting or latest event: %s", latest)
	}
	m.historyOffset = 100
	oldest := m.View()
	if !strings.Contains(oldest, "event-00") {
		t.Fatalf("cannot inspect oldest event: %s", oldest)
	}
}

func TestDetailFitsWithTabsAndTerminalControls(t *testing.T) {
	task := orchestrator.Task{ID: "task", Title: "title\twide", Prompt: "Please\tinspect", Output: "start\t" + strings.Repeat("\tindented text", 30) + "\n\x1b[999Ccursor-jump\x1b[2J\rEND"}
	for _, size := range [][2]int{{120, 30}, {80, 24}, {50, 20}, {30, 12}} {
		m := model{width: size[0], height: size[1], tasks: []orchestrator.Task{task}}
		for offset := 0; offset <= m.maxScroll(); offset++ {
			m.historyOffset = offset
			view := m.View()
			if strings.Contains(view, "\t") || strings.Contains(view, "\r") || strings.Contains(view, "\x1b[999C") || strings.Contains(view, "\x1b[2J") {
				t.Fatalf("unsafe terminal characters at %v: %q", size, view)
			}
			if lipgloss.Width(view) > size[0] || lipgloss.Height(view) > size[1] {
				t.Fatalf("overflow at %v: %dx%d", size, lipgloss.Width(view), lipgloss.Height(view))
			}
		}
		text := strings.Join(m.detailContent(20), "\n")
		if !strings.Contains(text, "cursor-jump") || !strings.Contains(text, "END") {
			t.Fatalf("content lost: %s", text)
		}
	}
}

func TestPanelDoesNotRewrapViewportRows(t *testing.T) {
	for _, width := range []int{26, 40, 75} {
		height := 15
		rows := make([]string, height-2)
		for i := range rows {
			rows[i] = strings.Repeat("x", width-4)
		}
		rendered := panel(strings.Join(rows, "\n"), width, height)
		if lipgloss.Width(rendered) != width || lipgloss.Height(rendered) != height {
			t.Fatalf("panel %dx%d rendered %dx%d", width, height, lipgloss.Width(rendered), lipgloss.Height(rendered))
		}
		if strings.Count(rendered, strings.Repeat("x", width-4)) != height-2 {
			t.Fatal("viewport rows wrapped or disappeared")
		}
	}
}

func TestDetailTabsOmitActivityTab(t *testing.T) {
	task := orchestrator.Task{ID: "task", Title: "work", Prompt: "do work", Agent: "general"}
	m := model{width: 120, height: 30, tasks: []orchestrator.Task{task}}
	for _, activity := range []bool{false, true} {
		m.activity = activity
		tabs := ansi.Strip(m.detailTabRow(task, 100))
		if strings.Contains(tabs, "ACTIVITY") {
			t.Fatalf("activity tab still present: %q", tabs)
		}
		if !strings.Contains(tabs, "OUTPUT") || !strings.Contains(tabs, "HISTORY") {
			t.Fatalf("missing output/history tabs: %q", tabs)
		}
	}
}

func TestThinkingToggleChangesOutputAndKeepsLayoutBounded(t *testing.T) {
	service, err := orchestrator.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	task := orchestrator.Task{ID: "thinking", Title: "reason", Prompt: "work", Status: orchestrator.Running,
		Output: "visible before\n[thinking]\nprivate reasoning\n[/thinking]\nvisible after\n"}
	m := model{service: service, width: 120, height: 30, tasks: []orchestrator.Task{task}}
	hidden := ansi.Strip(m.View())
	if strings.Contains(hidden, "private reasoning") || !strings.Contains(hidden, "thinking hidden") {
		t.Fatalf("thinking should start hidden: %s", hidden)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'T'}})
	m = next.(model)
	m.tasks = []orchestrator.Task{task}
	if !m.thinking {
		t.Fatal("T did not enable thinking")
	}
	shown := ansi.Strip(m.View())
	if !strings.Contains(shown, "private reasoning") || !strings.Contains(shown, "THINKING") {
		t.Fatalf("thinking not shown: %s", shown)
	}
	if lipgloss.Width(m.View()) > m.width || lipgloss.Height(m.View()) > m.height {
		t.Fatal("thinking output overflowed the terminal")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'T'}})
	m = next.(model)
	m.tasks = []orchestrator.Task{task}
	if m.thinking {
		t.Fatal("T did not hide thinking")
	}
}

func TestTextUsesNoBackgroundColour(t *testing.T) {
	m := model{width: 120, height: 30, tasks: []orchestrator.Task{{
		ID: "task", Title: "work", Prompt: "do work", Agent: "general",
		Output: "`inline code`\n\n```\ncode block\n```\n",
	}}}
	views := []string{m.View()}
	m.help = true
	views = append(views, m.View())
	for _, view := range views {
		if strings.Contains(view, "\x1b[48;") {
			t.Fatal("text is rendered with a background colour")
		}
	}
}

func TestErgonomicLayoutPrioritizesDetails(t *testing.T) {
	m := model{width: 120, height: 30, tasks: []orchestrator.Task{{ID: "x", Title: "task", Prompt: "work"}}}
	w, h := m.detailGeometry()
	if w < 80 || h < 25 {
		t.Fatalf("detail pane too small: %dx%d", w, h)
	}
	m.fullscreen = true
	w, _ = m.detailGeometry()
	if w != 120 {
		t.Fatal("fullscreen detail width", w)
	}
	m.width = 50
	m.fullscreen = false
	w, _ = m.detailGeometry()
	if w != 50 {
		t.Fatal("narrow detail width", w)
	}
	if strings.Contains(m.View(), "TASKS  01") {
		t.Fatal("narrow layout wastes space on sidebar")
	}
}
