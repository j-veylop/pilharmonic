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
	if want := "1a2b · ↑12.3k ↓4.5k $0.0043"; !strings.Contains(detail, want) {
		t.Fatalf("attempt row missing %q: %s", want, detail)
	}
	if want := "3c4d · ↑12.3k ↓4.5k"; !strings.Contains(detail, want) {
		t.Fatalf("unpriced attempt row missing %q: %s", want, detail)
	}
	if strings.Contains(detail, "3c4d · ↑12.3k ↓4.5k $") {
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

func TestUsageVariantsShowCacheBuckets(t *testing.T) {
	tokens := orchestrator.TokenUsage{
		InputTokens: 1000, OutputTokens: 500,
		CacheReadTokens: 3000, CacheWriteTokens: 200,
	}
	variants := usageVariants(tokens, 0.01, true)
	if len(variants) < 2 {
		t.Fatalf("missing variants: %v", variants)
	}
	for _, want := range []string{"↑4.2k", "↺3k(71%)", "✎200", "↓500", "$0.01"} {
		if !strings.Contains(variants[0], want) {
			t.Fatalf("detailed variant missing %q: %q", want, variants[0])
		}
	}
	if !strings.Contains(variants[1], "↑4.2k/↓500 ↺3k") {
		t.Fatalf("compact variant missing cache: %q", variants[1])
	}
}

// A task row keeps the cost when the token detail no longer fits, and a task
// with an unpriced model shows only tokens.
func TestTaskListUsageDropsDetailBeforeCost(t *testing.T) {
	usage := orchestrator.TaskUsage{
		TokenUsage: orchestrator.TokenUsage{InputTokens: 12_300, OutputTokens: 4_500},
		CostUSD:    0.0043,
		Priced:     true,
	}
	if got := taskListUsage(usage, 80); got != "↑12.3k ↓4.5k $0.0043" {
		t.Fatalf("full usage = %q", got)
	}
	if got := taskListUsage(usage, 7); got != "$0.0043" {
		t.Fatalf("narrow usage = %q, want the cost", got)
	}
	usage.Priced = false
	if got := taskListUsage(usage, 80); strings.Contains(got, "$") {
		t.Fatalf("unpriced usage showed a cost: %q", got)
	}
}

// A main-view row stands for its whole tree, so its tokens and cost include
// every descendant, including while the work is still running.
func TestMainViewTaskUsageIncludesSubtasks(t *testing.T) {
	now := time.Now()
	attempt := func(id string, input, output int64, cost float64) []orchestrator.Attempt {
		return []orchestrator.Attempt{{
			ID: id, Status: orchestrator.Running, StartedAt: now,
			Usage: &orchestrator.AttemptUsage{
				Model: "openai/gpt-4.1", InputTokens: input, OutputTokens: output,
				CostUSD: cost, Priced: true,
			},
		}}
	}
	tasks := []orchestrator.Task{
		{ID: "parent", Title: "parent", Status: orchestrator.Running, UpdatedAt: now, Attempts: attempt("p", 1000, 500, 0.004)},
		{ID: "child", ParentID: "parent", Depth: 1, Title: "child", Status: orchestrator.Running, UpdatedAt: now, Attempts: attempt("c", 2000, 500, 0.002)},
		{ID: "grandchild", ParentID: "child", Depth: 2, Title: "grandchild", Status: orchestrator.Running, UpdatedAt: now, Attempts: attempt("g", 1000, 500, 0.001)},
	}
	usage := taskUsageWithSubtasks(tasks[0], tasks)
	if usage.InputTokens != 4000 || usage.OutputTokens != 1500 {
		t.Fatalf("tree tokens = %+v", usage.TokenUsage)
	}
	if got := formatCost(usage.CostUSD); got != "$0.0070" {
		t.Fatalf("tree cost = %s, want $0.0070", got)
	}

	m := model{width: 120, height: 40, tasks: tasks}
	board := ansi.Strip(m.View())
	if !strings.Contains(board, "$0.0070") {
		t.Fatalf("main view row missing aggregated cost: %q", board)
	}
}

// One unpriced descendant keeps the tree total from claiming a cost.
func TestMainViewTaskUsageStaysUnpricedWithAnUnpricedSubtask(t *testing.T) {
	now := time.Now()
	tasks := []orchestrator.Task{
		{ID: "parent", Title: "parent", Status: orchestrator.Running, UpdatedAt: now, Attempts: []orchestrator.Attempt{{
			ID: "p", StartedAt: now,
			Usage: &orchestrator.AttemptUsage{Model: "openai/gpt-4.1", InputTokens: 1000, OutputTokens: 500, CostUSD: 0.004, Priced: true},
		}}},
		{ID: "child", ParentID: "parent", Depth: 1, Title: "child", Status: orchestrator.Running, UpdatedAt: now, Attempts: []orchestrator.Attempt{{
			ID: "c", StartedAt: now,
			Usage: &orchestrator.AttemptUsage{Model: "nosuch/model", InputTokens: 2000, OutputTokens: 500},
		}}},
	}
	usage := taskUsageWithSubtasks(tasks[0], tasks)
	if usage.Priced {
		t.Fatalf("tree total claimed a price: %+v", usage)
	}
	m := model{width: 120, height: 40, tasks: tasks}
	board := ansi.Strip(m.View())
	if strings.Contains(board, "$") {
		t.Fatalf("main view row claimed a cost for unpriced work: %q", board)
	}
}
