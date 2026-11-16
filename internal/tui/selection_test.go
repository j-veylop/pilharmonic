package tui

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
)

func copyTestModel() model {
	task := orchestrator.Task{
		ID:     "task",
		Title:  "sidebar-title",
		Prompt: "mandate",
		Output: "alpha\nbeta\ngamma\n",
	}
	child := orchestrator.Task{ID: "child", ParentID: "task", Depth: 1, Title: "child", Status: orchestrator.Running}
	return model{width: 120, height: 24, detailFocus: true, tasks: []orchestrator.Task{task, child}}
}

func findViewportText(t *testing.T, viewport detailViewport, needle string) textPosition {
	t.Helper()
	for line := viewport.start; line < viewport.end; line++ {
		plain := ansi.Strip(viewport.content[line])
		if index := strings.Index(plain, needle); index >= 0 {
			return textPosition{line: line, col: ansi.StringWidth(plain[:index])}
		}
	}
	t.Fatalf("%q not visible in %q", needle, viewport.content)
	return textPosition{}
}

func screenPosition(viewport detailViewport, position textPosition) (int, int) {
	return viewport.x + position.col, viewport.y + position.line - viewport.start
}

func decodeOSC52(t *testing.T, sequence string) string {
	t.Helper()
	const prefix = "\x1b]52;c;"
	if !strings.HasPrefix(sequence, prefix) || !strings.HasSuffix(sequence, "\x07") {
		t.Fatalf("not an OSC 52 clipboard sequence: %q", sequence)
	}
	encoded := strings.TrimSuffix(strings.TrimPrefix(sequence, prefix), "\x07")
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("invalid clipboard payload: %v", err)
	}
	return string(decoded)
}

func TestSelectedTextAcrossLines(t *testing.T) {
	lines := []string{"alpha beta", "second line", "third"}
	selection := textSelection{
		anchor: textPosition{line: 0, col: 6},
		focus:  textPosition{line: 2, col: 3},
	}
	want := "beta\nsecond line\nthir"
	if got := selectedText(lines, selection); got != want {
		t.Fatalf("selectedText = %q, want %q", got, want)
	}

	selection.anchor, selection.focus = selection.focus, selection.anchor
	if got := selectedText(lines, selection); got != want {
		t.Fatalf("reversed selectedText = %q, want %q", got, want)
	}
}

func TestMouseSelectionCopiesContentPaneOnly(t *testing.T) {
	m := copyTestModel()
	viewport, ok := m.detailViewport()
	if !ok {
		t.Fatal("detail viewport unavailable")
	}
	start := findViewportText(t, viewport, "alpha")
	end := findViewportText(t, viewport, "gamma")
	end.col += ansi.StringWidth("gamma") - 1

	startX, startY := screenPosition(viewport, start)
	endX, endY := screenPosition(viewport, end)

	next, cmd := m.Update(tea.MouseMsg{X: startX, Y: startY, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = next.(model)
	if !m.selection.active || !m.selection.dragging {
		t.Fatal("content press did not start a selection")
	}
	if cmd != nil {
		t.Fatal("press unexpectedly copied")
	}
	next, cmd = m.Update(tea.MouseMsg{X: endX, Y: endY, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft})
	m = next.(model)
	if cmd != nil {
		t.Fatal("motion unexpectedly copied")
	}

	var out bytes.Buffer
	m.clipboard = &out
	next, cmd = m.Update(tea.MouseMsg{X: endX, Y: endY, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
	m = next.(model)
	if cmd == nil {
		t.Fatal("release did not schedule a clipboard copy")
	}
	next, _ = m.Update(cmd())
	m = next.(model)
	if got := decodeOSC52(t, out.String()); got != "alpha\n  beta\n  gamma" {
		t.Fatalf("copied %q", got)
	}
	if !strings.Contains(m.notice.text, "Copied 3 line(s)") {
		t.Fatalf("missing copy notice: %q", m.notice.text)
	}
}

func TestMouseSelectionRejectsSidebarAndClampsDrag(t *testing.T) {
	m := copyTestModel()
	viewport, ok := m.detailViewport()
	if !ok {
		t.Fatal("detail viewport unavailable")
	}

	for name, point := range map[string][2]int{
		"sidebar": {2, viewport.y},
		"header":  {viewport.x, viewport.y - 1},
		"footer":  {viewport.x, m.height - 1},
	} {
		next, cmd := m.Update(tea.MouseMsg{X: point[0], Y: point[1], Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
		m = next.(model)
		if m.selection.active || cmd != nil {
			t.Fatalf("%s press started a selection", name)
		}
	}

	start := findViewportText(t, viewport, "alpha")
	startX, startY := screenPosition(viewport, start)
	next, _ := m.Update(tea.MouseMsg{X: startX, Y: startY, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = next.(model)
	// Drag left into the sidebar and below the content rows.
	next, _ = m.Update(tea.MouseMsg{X: 1, Y: m.height - 2, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft})
	m = next.(model)

	var out bytes.Buffer
	m.clipboard = &out
	next, cmd := m.Update(tea.MouseMsg{X: 1, Y: m.height - 2, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
	m = next.(model)
	if cmd == nil {
		t.Fatal("clamped selection did not copy")
	}
	cmd()
	copied := decodeOSC52(t, out.String())
	if strings.Contains(copied, "TASKS") || strings.Contains(copied, "sidebar-title") || strings.Contains(copied, "PILHARMONIC") {
		t.Fatalf("selection escaped the content pane: %q", copied)
	}
	if !strings.HasPrefix(copied, "alpha\n  beta\n") {
		t.Fatalf("selection did not start in content: %q", copied)
	}
}

func TestMouseClickWithoutDragDoesNotCopy(t *testing.T) {
	m := copyTestModel()
	viewport, ok := m.detailViewport()
	if !ok {
		t.Fatal("detail viewport unavailable")
	}
	start := findViewportText(t, viewport, "alpha")
	x, y := screenPosition(viewport, start)
	next, _ := m.Update(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = next.(model)
	next, cmd := m.Update(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
	m = next.(model)
	if cmd != nil || m.selection.active {
		t.Fatal("click without a drag copied or retained a selection")
	}
}

func TestSelectionHighlightFitsTerminal(t *testing.T) {
	for _, size := range [][2]int{{120, 24}, {80, 24}, {50, 20}} {
		m := copyTestModel()
		m.width, m.height = size[0], size[1]
		viewport, ok := m.detailViewport()
		if !ok {
			t.Fatalf("%v: detail viewport unavailable", size)
		}
		start := findViewportText(t, viewport, "alpha")
		end := findViewportText(t, viewport, "gamma")
		end.col += ansi.StringWidth("gamma") - 1
		m.selection = textSelection{active: true, anchor: start, focus: end, lines: viewport.content}
		view := m.View()
		if lipgloss.Width(view) > size[0] || lipgloss.Height(view) > size[1] {
			t.Fatalf("%v: selected view rendered %dx%d", size, lipgloss.Width(view), lipgloss.Height(view))
		}
	}
}

func longOutputModel(lines int) model {
	output := make([]string, lines)
	for i := range output {
		output[i] = fmt.Sprintf("line-%02d", i)
	}
	task := orchestrator.Task{ID: "task", Prompt: "mandate", Output: strings.Join(output, "\n")}
	child := orchestrator.Task{ID: "child", ParentID: "task", Depth: 1, Title: "child", Status: orchestrator.Running}
	return model{width: 120, height: 24, detailFocus: true, tasks: []orchestrator.Task{task, child}}
}

func pressContent(t *testing.T, m model, needle string) (model, textPosition, int, int) {
	t.Helper()
	viewport, ok := m.detailViewport()
	if !ok {
		t.Fatal("detail viewport unavailable")
	}
	if viewport.start == 0 {
		t.Fatal("test needs a scrolling viewport so output growth moves the mapping")
	}
	position := findViewportText(t, viewport, needle)
	x, y := screenPosition(viewport, position)
	next, cmd := m.Update(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = next.(model)
	if cmd != nil || !m.selection.dragging {
		t.Fatal("content press did not start a drag")
	}
	return m, position, x, y
}

func appendOutput(m model, line string) model {
	m.tasks[0].Output += "\n" + line
	return m
}

// A selection is dropped, and never copied, once output growth or a resize
// changes which content lines the frozen cells map to.
func TestStaleSelectionIsDroppedNotCopied(t *testing.T) {
	m, _, _, _ := pressContent(t, longOutputModel(80), "line-70")
	if m.cancelStaleSelection(); !m.selection.active {
		t.Fatal("an unchanged mapping cancelled the selection")
	}
	changes := map[string]func(model) model{
		"output growth": func(m model) model { return appendOutput(m, "line-80") },
		"resize": func(m model) model {
			next, _ := m.Update(tea.WindowSizeMsg{Width: 90, Height: 16})
			return next.(model)
		},
		"tick after growth": func(m model) model {
			m = appendOutput(m, "line-80")
			m.cancelStaleSelection()
			return m
		},
	}
	for name, change := range changes {
		for _, motion := range []bool{false, true} {
			m, _, x, y := pressContent(t, longOutputModel(80), "line-70")
			m = change(m)
			var out bytes.Buffer
			m.clipboard = &out
			if motion {
				next, _ := m.Update(tea.MouseMsg{X: x, Y: y - 2, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft})
				m = next.(model)
			}
			next, cmd := m.Update(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
			if cmd != nil || out.Len() != 0 || next.(model).selection.active {
				t.Fatalf("%s (motion=%v): stale selection was kept or copied", name, motion)
			}
		}
	}
}
