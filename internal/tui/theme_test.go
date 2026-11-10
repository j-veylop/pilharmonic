package tui

import (
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestRunningStatusUsesBlue(t *testing.T) {
	hex := string(statusRunning)
	value, err := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	if err != nil || len(hex) != 7 || !strings.HasPrefix(hex, "#") {
		t.Fatalf("running status is not a hex colour: %q (%v)", hex, err)
	}
	r, g, b := int(value>>16), int(value>>8&0xFF), int(value&0xFF)
	if b <= g || b <= r {
		t.Fatalf("running colour %q is not blue-dominant: r=%d g=%d b=%d", hex, r, g, b)
	}
	if statusRunning != blue {
		t.Fatalf("running status %q does not use the blue palette entry %q", statusRunning, blue)
	}
}

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
