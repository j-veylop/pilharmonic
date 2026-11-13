package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
)

func activityModel() model {
	return model{width: 100, height: 24, activity: activityState{open: true}, detailFocus: true, tasks: []orchestrator.Task{{ID: "task", Title: "work", Activities: []orchestrator.ToolActivity{
		{ID: "one", Name: "bash", AttemptID: "attempt-one", Status: "finished", Args: json.RawMessage(`{"command":"go test ./..."}`), Result: json.RawMessage(`{"content":[{"type":"text","text":"all passed"}]}`)},
		{ID: "two", Name: "read", AttemptID: "attempt-one", Status: "running", Args: json.RawMessage(`{"path":"main.go"}`)},
	}}}}
}
func TestActivitySelectExpandAndRetainIdentity(t *testing.T) {
	m := activityModel()
	if !strings.Contains(m.View(), "go test") {
		t.Fatal(m.View())
	}
	if !m.activityKey("j") || m.activity.id != "two" {
		t.Fatal("selection failed")
	}
	m.activityKey("enter")
	if !m.activity.expanded || !strings.Contains(m.View(), "main.go") || !strings.Contains(m.View(), "attempt-one") {
		t.Fatal(m.View())
	}
	m.tasks[0].Activities = append(m.tasks[0].Activities, orchestrator.ToolActivity{ID: "three", Name: "bash"})
	selected, _, _ := m.selectedActivity()
	if selected.ID != "two" {
		t.Fatal("new activity moved selection")
	}
	m.activityKey("esc")
	if m.activity.expanded {
		t.Fatal("did not collapse")
	}
}
func TestExpandedActivityScrollableAndFits(t *testing.T) {
	for _, size := range [][2]int{{120, 30}, {80, 24}, {50, 20}} {
		m := activityModel()
		m.width, m.height = size[0], size[1]
		m.activity.id = "one"
		m.activity.expanded = true
		payload, _ := json.Marshal(map[string]string{"text": strings.Repeat("long output\t", 200) + "END-RESULT"})
		m.tasks[0].Activities[0].Result = payload
		m.historyOffset = m.maxScroll()
		if !strings.Contains(m.View(), "ARGUMENTS") {
			t.Fatalf("top missing: %s", m.View())
		}
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

func TestActivityEmptyStateMentionsFilterOnlyWhenFiltered(t *testing.T) {
	m := model{activity: activityState{open: true}, tasks: []orchestrator.Task{{ID: "task"}}}
	empty := ansi.Strip(strings.Join(m.activityContent(80), "\n"))
	if !strings.Contains(empty, "No tool calls yet.") || strings.Contains(empty, "No calls match") {
		t.Fatalf("unfiltered empty state is misleading: %q", empty)
	}
	m.activity.filter = 2
	filtered := ansi.Strip(strings.Join(m.activityContent(80), "\n"))
	if !strings.Contains(filtered, "No calls match this filter.") || strings.Contains(filtered, "No tool calls yet.") {
		t.Fatalf("filtered empty state is misleading: %q", filtered)
	}
}

func TestActivityListDistinguishesStatuses(t *testing.T) {
	// The status is the icon alone: the panel keeps its rows compact and shows
	// the arguments and result next to it instead of the status word.
	m := activityModel()
	view := ansi.Strip(m.View())
	for _, want := range []string{"✓", "⠋", "main.go", "go test ./..."} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q in %s", want, view)
		}
	}
	rows := ansi.Strip(strings.Join(m.activityContent(80), "\n"))
	for _, unwanted := range []string{"finished", "running"} {
		if strings.Contains(rows, unwanted) {
			t.Fatalf("status word %q still shown in %s", unwanted, rows)
		}
	}
	m.activity.id = "one"
	m.activity.expanded = true
	expanded := ansi.Strip(strings.Join(m.activityContent(80), "\n"))
	if !strings.HasPrefix(expanded, "bash  ✓") || strings.Contains(expanded, "finished") {
		t.Fatalf("expanded call did not show the status as an icon: %q", expanded)
	}
}

func TestActivityFiltersAndCurrentAction(t *testing.T) {
	task := orchestrator.Task{Status: orchestrator.Running, Attempts: []orchestrator.Attempt{{ID: "old"}, {ID: "new"}}, Activities: []orchestrator.ToolActivity{
		{ID: "same", AttemptID: "old", Status: "failed"},
		{ID: "same", AttemptID: "new", Name: "bash", Status: "running", Args: json.RawMessage(`{"command":"go test ./..."}`)},
	}}
	m := model{tasks: []orchestrator.Task{task}, activity: activityState{open: true}}
	if m.currentAction(task) != "⠋ bash: go test ./..." {
		t.Fatal(m.currentAction(task))
	}
	m.activityKey("f")
	if len(m.visibleActivities()) != 1 || m.visibleActivities()[0].AttemptID != "new" {
		t.Fatal("latest filter")
	}
	m.activityKey("enter")
	if !m.activity.expanded || m.activity.attempt != "new" {
		t.Fatal("wrong expanded attempt")
	}
	m.activityKey("f")
	if m.activity.expanded || len(m.visibleActivities()) != 1 || m.visibleActivities()[0].Status != "failed" {
		t.Fatal("error filter")
	}
	m.activityKey("f")
	if len(m.visibleActivities()) != 2 {
		t.Fatal("all filter")
	}
	task.Status = orchestrator.Completed
	if m.currentAction(task) != "" {
		t.Fatal("finished task showed current action")
	}
}
