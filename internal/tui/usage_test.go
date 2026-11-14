package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
)

func TestFormatTokensCostAndPrice(t *testing.T) {
	tokens := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{950, "950"},
		{999, "999"},
		{1_000, "1k"},
		{12_000, "12k"},
		{12_300, "12.3k"},
		{999_949, "999.9k"},
		{999_950, "1M"},
		{1_200_000, "1.2M"},
		{1_500_000, "1.5M"},
	}
	for _, tc := range tokens {
		if got := formatTokens(tc.in); got != tc.want {
			t.Fatalf("formatTokens(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}

	costs := []struct {
		in   float64
		want string
	}{
		{0.0043, "$0.0043"},
		{0.009999, "$0.0100"},
		{0.01, "$0.01"},
		{4.21, "$4.21"},
		{12.345, "$12.35"},
		{0.00001, "<$0.0001"},
	}
	for _, tc := range costs {
		if got := formatCost(tc.in); got != tc.want {
			t.Fatalf("formatCost(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}

	prices := []struct {
		in   float64
		want string
	}{
		{0.14, "$0.14"},
		{0.21, "$0.21"},
		{2, "$2"},
		{1.25, "$1.25"},
		{10, "$10"},
	}
	for _, tc := range prices {
		if got := formatPrice(tc.in); got != tc.want {
			t.Fatalf("formatPrice(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The history tab appends each attempt's own usage, dropping detail rather than
// wrapping the row.
func TestHistoryAttemptsShowRecordedUsage(t *testing.T) {
	const selector = "openai/gpt-4.1"
	started := time.Date(2025, 9, 1, 12, 0, 0, 0, time.Local)
	task := orchestrator.Task{
		ID: "task", Title: "work", Prompt: "do the work", Agent: "general",
		Attempts: []orchestrator.Attempt{
			{ID: "1a2b", Status: orchestrator.Running, StartedAt: started,
				Usage: &orchestrator.AttemptUsage{Model: selector, InputTokens: 12_300, OutputTokens: 4_500, CostUSD: 0.0043, Priced: true}},
			{ID: "3c4d", Status: orchestrator.Completed, StartedAt: started.Add(-time.Hour),
				Usage: &orchestrator.AttemptUsage{Model: "openrouter/mystery", InputTokens: 12_300, OutputTokens: 4_500}},
			{ID: "5e6f", Status: orchestrator.Completed, StartedAt: started.Add(-2 * time.Hour)},
		},
	}
	m := model{width: 120, height: 40, tasks: []orchestrator.Task{task}, tab: infoTab}
	detail := ansi.Strip(strings.Join(m.detailContent(100), "\n"))
	if want := "1a2b · 12.3k in · 4.5k out · $0.0043"; !strings.Contains(detail, want) {
		t.Fatalf("attempt row missing %q: %s", want, detail)
	}
	if want := "3c4d · 12.3k in · 4.5k out"; !strings.Contains(detail, want) {
		t.Fatalf("unpriced attempt row missing %q: %s", want, detail)
	}
	if strings.Contains(detail, "3c4d · 12.3k in · 4.5k out · $") {
		t.Fatalf("unpriced attempt row printed a cost: %s", detail)
	}
	if strings.Contains(detail, "5e6f ·") {
		t.Fatalf("attempt without usage gained a suffix: %s", detail)
	}

	// A narrow panel keeps the row on one line by dropping the cost, and then
	// the token counts, rather than wrapping it.
	narrow := model{width: 40, height: 40, tasks: []orchestrator.Task{task}, tab: infoTab}
	lines := narrow.detailContent(40)
	var row string
	for _, line := range lines {
		if strings.Contains(line, "1a2b") {
			row = ansi.Strip(line)
		}
	}
	if row == "" {
		t.Fatalf("narrow history lost the attempt row: %v", lines)
	}
	if lipgloss.Width(row) > 40 {
		t.Fatalf("attempt row is %d cells in a 40 cell panel: %q", lipgloss.Width(row), row)
	}
	// Only the cost fits beside the time, status and ID, and that is what the
	// row keeps.
	if !strings.HasSuffix(row, " · $0.0043") {
		t.Fatalf("narrow attempt row did not shorten to the cost: %q", row)
	}
}
