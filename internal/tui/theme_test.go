package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestGradientWordmarkPreservesTextAndWidth(t *testing.T) {
	const wordmark = "◈ ORCHESTRATOR"
	rendered := gradientText(wordmark)
	if got := ansi.Strip(rendered); got != wordmark {
		t.Fatalf("gradient changed the wordmark: %q", got)
	}
	if got, want := lipgloss.Width(rendered), lipgloss.Width(wordmark); got != want {
		t.Fatalf("gradient changed width: got %d want %d", got, want)
	}
}

func TestSelectionRowFillsExactWidth(t *testing.T) {
	for _, width := range []int{1, 2, 5, 20, 34} {
		row := selectionRow("selected task", width)
		if lipgloss.Width(row) != width {
			t.Fatalf("width %d rendered %d cells: %q", width, lipgloss.Width(row), ansi.Strip(row))
		}
		if strings.Contains(row, "\n") {
			t.Fatalf("selection row wrapped at width %d", width)
		}
	}
	if got := ansi.Strip(selectionRow("selected task", 20)); !strings.Contains(got, "selected task") {
		t.Fatalf("selection text missing: %q", got)
	}
}
