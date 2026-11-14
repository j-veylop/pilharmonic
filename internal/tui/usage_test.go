package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
)

// fixedUsageService reports a canned session total. A real service only records
// usage while an adapter runs, so the header and the refresh tick need a
// snapshot the test can set.
type fixedUsageService struct {
	*orchestrator.Service
	total orchestrator.SessionUsage
}

func (f fixedUsageService) SessionUsage() orchestrator.SessionUsage { return f.total }

// usedBoard is a board whose service reports tokens for one session.
func usedBoard(t *testing.T, total orchestrator.SessionUsage) (model, *orchestrator.Service) {
	t.Helper()
	s := newTestService(t)
	if _, err := s.CreateTask("work", "do the work", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	m := model{service: fixedUsageService{Service: s, total: total}, tasks: s.ListTasks(), usage: total, width: 120, height: 35}
	return m, s
}

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

// An untouched board is unchanged: the usage segment only appears once the
// session has recorded a token.
func TestHeaderOmitsUsageWithoutTokens(t *testing.T) {
	m, s := usedBoard(t, orchestrator.SessionUsage{})
	plain := ansi.Strip(m.View())
	if strings.Contains(plain, "Σ") {
		t.Fatalf("header rendered a usage segment for an untouched session: %q", plain)
	}
	if !strings.Contains(plain, "1 tasks") || !strings.Contains(plain, "◆ 0 need action") {
		t.Fatalf("header lost its counters: %q", plain)
	}

	// A tick is what refreshes the total: the segment must appear from the
	// service snapshot, not from anything the board counts itself.
	m.service = fixedUsageService{Service: s, total: orchestrator.SessionUsage{
		InputTokens: 12_300, OutputTokens: 4_500, TotalTokens: 16_800,
		CostUSD: 0.0043, Priced: true, Tasks: 1,
	}}
	next, _ := m.Update(tickMsg(time.Now()))
	m = next.(model)
	plain = ansi.Strip(m.View())
	if want := "Σ 12.3k in · 4.5k out · $0.0043"; !strings.Contains(plain, want) {
		t.Fatalf("header missing %q: %q", want, plain)
	}
	// The segment sits between the counters and the mode badge.
	actions, usage, badge := strings.Index(plain, "◆ 0 need action"), strings.Index(plain, "Σ"), strings.Index(plain, " TASKS ")
	if actions < 0 || usage < 0 || badge < 0 || !(actions < usage && usage < badge) {
		t.Fatalf("usage segment is not between the counters and the badge: %q", plain)
	}
	if strings.Contains(strings.Split(plain, "\n")[0], "…") {
		t.Fatalf("the header truncated with room to spare: %q", plain)
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
	m := model{width: 120, height: 40, tasks: []orchestrator.Task{task}, history: true}
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
	narrow := model{width: 40, height: 40, tasks: []orchestrator.Task{task}, history: true}
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

// The session total shortens, then leaves, before the counters lose their
// wording, and never truncates the header or changes the frame's height.
func TestHeaderUsageGivesWayAsTheTerminalNarrows(t *testing.T) {
	big := orchestrator.SessionUsage{InputTokens: 1_234_567, OutputTokens: 987_654, TotalTokens: 2_222_221, CostUSD: 4.21, Priced: true, Tasks: 1}
	unpriced := orchestrator.SessionUsage{InputTokens: 12_300, OutputTokens: 4_500, TotalTokens: 16_800, CostUSD: 0.9, Tasks: 1}
	cases := []struct {
		total        orchestrator.SessionUsage
		width        int
		usage, count string
	}{
		{big, 120, "Σ 1.2M in · 987.7k out · $4.21", "◆ 0 need action"},
		{big, 100, "Σ 1.2M/987.7k · $4.21", "◆ 0 need action"},
		{big, 80, "", "◆ 0 need action"},
		{big, 76, "", "◆ 0 need action"},
		{big, 60, "Σ $4.21", "◆0"},
		{big, 40, "", "◆0"},
		{unpriced, 120, "Σ 12.3k in · 4.5k out", "◆ 0 need action"},
	}
	for _, tc := range cases {
		m, _ := usedBoard(t, tc.total)
		m.width, m.height = tc.width, 24
		idle, _ := usedBoard(t, orchestrator.SessionUsage{})
		idle.width, idle.height = tc.width, 24
		view := m.View()
		assertFits(t, view, tc.width, 24)
		top := ansi.Strip(strings.Split(view, "\n")[0])
		if strings.Contains(top, "Σ") != (tc.usage != "") || !strings.Contains(top, tc.usage) || !strings.Contains(top, tc.count) ||
			!strings.Contains(top, "PILHARMONIC") || !strings.Contains(top, "TASKS") || strings.Contains(top, "…") || !tc.total.Priced && strings.Contains(top, "$") {
			t.Fatalf("width %d: header %q, want %q and %q", tc.width, top, tc.usage, tc.count)
		}
		if lipgloss.Height(view) != lipgloss.Height(idle.View()) {
			t.Fatalf("width %d: usage changed the frame height", tc.width)
		}
	}
}

// The detail panel shows a task's recorded usage and the price in force now
// for the model it last used.
func TestDetailShowsUsageAndCurrentPrice(t *testing.T) {
	usage := func(selector string, priced bool, rate string) []orchestrator.Attempt {
		return []orchestrator.Attempt{{ID: "attempt", RequestedModel: selector,
			Usage: &orchestrator.AttemptUsage{Model: selector, InputTokens: 12_300, OutputTokens: 4_500, CostUSD: 0.0043, Priced: priced, Rate: rate}}}
	}
	peak := "Price: $0.28/M in · $0.42/M out · deepseek peak"
	if price, _ := orchestrator.PriceFor("deepseek/deepseek-chat", time.Now()); price.Rate(time.Now()) == orchestrator.RateOffPeak {
		peak = "Price: $0.14/M in · $0.21/M out · deepseek off-peak"
	}
	cases := []struct {
		name     string
		attempts []orchestrator.Attempt
		want     []string
		unwanted []string
	}{
		{"time of day", usage("deepseek/deepseek-chat", true, ""), []string{"Last attempt model:", "Usage: 12.3k in · 4.5k out · $0.0043", peak}, nil},
		{"mixed rate", usage("deepseek/deepseek-chat", true, orchestrator.RateMixed), []string{"· deepseek peak/off-peak"}, nil},
		{"all day", usage("openai/gpt-4.1", true, ""), []string{"Price: $2/M in · $8/M out"}, []string{"peak", "openai "}},
		{"unpriced", usage("openrouter/mystery", false, ""), []string{"Usage: 12.3k in · 4.5k out · cost unknown"}, []string{"Price:", "$"}},
		{"nothing recorded", []orchestrator.Attempt{{ID: "attempt", RequestedModel: "openai/gpt-4.1"}}, nil, []string{"Usage:", "Price:"}},
	}
	for _, tc := range cases {
		m := model{width: 120, height: 40, tasks: []orchestrator.Task{{ID: "task", Title: "work", Prompt: "do the work", Agent: "general", Attempts: tc.attempts}}}
		detail := ansi.Strip(strings.Join(m.detailContent(100), "\n"))
		for _, want := range tc.want {
			if !strings.Contains(detail, want) {
				t.Fatalf("%s: detail missing %q: %s", tc.name, want, detail)
			}
		}
		for _, unwanted := range tc.unwanted {
			if strings.Contains(detail, unwanted) {
				t.Fatalf("%s: detail shows %q: %s", tc.name, unwanted, detail)
			}
		}
	}
}
