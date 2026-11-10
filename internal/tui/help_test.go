package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"strings"
	"testing"
)

func TestHelpModalCapturesInputAndScrolls(t *testing.T) {
	m := model{width: 80, height: 14, help: true}
	view := m.View()
	if !strings.Contains(view, "HELP") {
		t.Fatal(view)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = next.(model)
	if m.input.kind != "" {
		t.Fatal("help key started task composer")
	}
	next, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	m = next.(model)
	if m.helpScroll == 0 {
		t.Fatal("help wheel did not scroll")
	}
	m.helpScroll = 0
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(model)
	if m.helpScroll == 0 {
		t.Fatal("help did not scroll from keyboard")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = next.(model)
	m.scrollHelpBody(1000)
	if !strings.Contains(m.View(), "Select in executor") {
		t.Fatal("help cannot reach last shortcut")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if m.help {
		t.Fatal("escape did not close help")
	}
}
func TestHelpModalBounds(t *testing.T) {
	for _, size := range [][2]int{{30, 10}, {50, 20}, {80, 24}, {120, 40}} {
		m := model{width: size[0], height: size[1], help: true}
		for _, offset := range []int{0, 1000} {
			m.helpPage = offset
			view := m.View()
			if lipgloss.Width(view) > size[0] || lipgloss.Height(view) > size[1] {
				t.Fatalf("overflow at %v", size)
			}
		}
	}
}

func TestHelpPagination(t *testing.T) {
	m := model{width: 80, height: 24, help: true}
	if !strings.Contains(m.View(), "Page 1 /") {
		t.Fatal("missing page indicator")
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = next.(model)
	if m.helpPage != 1 || !strings.Contains(m.View(), "Page 2 /") {
		t.Fatal("right did not advance exactly one page")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = next.(model)
	if m.helpPage != 0 {
		t.Fatal("page up")
	}
	m.scrollHelp(-1)
	if m.helpPage != 0 {
		t.Fatal("wrapped before first page")
	}
	m.scrollHelp(10000)
	last := m.helpPage
	m.scrollHelp(1)
	if m.helpPage != last {
		t.Fatal("wrapped past last page")
	}
	if !strings.Contains(m.View(), "Select in executor") {
		t.Fatal("final page lost final shortcut")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyHome})
	m = next.(model)
	if m.helpPage != 0 {
		t.Fatal("home")
	}
}

func TestHelpTopicsAreLogicalAndIndependent(t *testing.T) {
	m := model{width: 80, height: 24, help: true}
	concepts := strings.Join(m.helpLines(70), "\n")
	if !strings.Contains(concepts, "Accepted work") || !strings.Contains(concepts, "need you") || strings.Contains(concepts, "Focus tasks") {
		t.Fatal("concepts missing or contains another topic")
	}
	m.scrollHelp(1)
	navigation := strings.Join(m.helpLines(70), "\n")
	if !strings.Contains(navigation, "Focus tasks") || strings.Contains(navigation, "New task") {
		t.Fatal("navigation contains another topic")
	}
	m.scrollHelp(1)
	tasks := strings.Join(m.helpLines(70), "\n")
	if !strings.Contains(tasks, "New task") || strings.Contains(tasks, "Toggle tool activity") {
		t.Fatal("tasks contains another topic")
	}
	m.scrollHelp(1)
	if !strings.Contains(strings.Join(m.helpLines(70), "\n"), "Toggle tool activity") {
		t.Fatal("activity missing")
	}
	m.scrollHelpBody(5)
	m.scrollHelp(1)
	if m.helpScroll != 0 {
		t.Fatal("topic switch retained old scroll")
	}
	if !strings.Contains(m.helpTabs(70), "Navigation") || !strings.Contains(m.helpTabs(70), "General") {
		t.Fatal("horizontal tabs missing")
	}
}
