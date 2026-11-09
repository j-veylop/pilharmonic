package tui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"orchestrator/internal/orchestrator"
)

func activityModel() model {
	return model{width: 100, height: 24, activity: true, detailFocus: true, tasks: []orchestrator.Task{{ID: "task", Title: "work", Activities: []orchestrator.ToolActivity{
		{ID: "one", Name: "bash", AttemptID: "attempt-one", Status: "finished", Args: json.RawMessage(`{"command":"go test ./..."}`), Result: json.RawMessage(`{"content":[{"type":"text","text":"all passed"}]}`)},
		{ID: "two", Name: "read", AttemptID: "attempt-one", Status: "running", Args: json.RawMessage(`{"path":"main.go"}`)},
	}}}}
}
func TestActivitySelectExpandAndRetainIdentity(t *testing.T) {
	m := activityModel()
	if !strings.Contains(m.View(), "go test") {
		t.Fatal(m.View())
	}
	if !m.activityKey("j") || m.activityID != "two" {
		t.Fatal("selection failed")
	}
	m.activityKey("enter")
	if !m.activityExpanded || !strings.Contains(m.View(), "main.go") || !strings.Contains(m.View(), "attempt-one") {
		t.Fatal(m.View())
	}
	m.tasks[0].Activities = append(m.tasks[0].Activities, orchestrator.ToolActivity{ID: "three", Name: "bash"})
	selected, _, _ := m.selectedActivity()
	if selected.ID != "two" {
		t.Fatal("new activity moved selection")
	}
	m.activityKey("esc")
	if m.activityExpanded {
		t.Fatal("did not collapse")
	}
}
func TestExpandedActivityScrollableAndFits(t *testing.T) {
	for _, size := range [][2]int{{120, 30}, {80, 24}, {50, 20}} {
		m := activityModel()
		m.width, m.height = size[0], size[1]
		m.activityID = "one"
		m.activityExpanded = true
		payload, _ := json.Marshal(map[string]string{"text": strings.Repeat("long output\t", 200) + "END-RESULT"})
		m.tasks[0].Activities[0].Result = payload
		m.historyOffset = m.maxScroll()
		if !strings.Contains(m.View(), "ARGUMENTS") {
			t.Fatalf("top missing: %s", m.View())
		}
		next, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
		m = next.(model)
		m.historyOffset = 0
		view := m.View()
		if !strings.Contains(view, "END-RESULT") {
			t.Fatal("result bottom inaccessible")
		}
		if lipgloss.Width(view) > size[0] || lipgloss.Height(view) > size[1] {
			t.Fatal("overflow")
		}
	}
}

func TestReadableToolTextResult(t *testing.T) {
	got := readableResult(json.RawMessage(`{"content":[{"type":"text","text":"first line\nsecond line"}],"details":{}}`))
	if got != "first line\nsecond line" {
		t.Fatalf("escaped output: %q", got)
	}
}

func TestActivityListDistinguishesStatuses(t *testing.T) {
	view := ansi.Strip(activityModel().View())
	for _, want := range []string{"✓", "finished", "⠋", "running", "main.go"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q in %s", want, view)
		}
	}
}
