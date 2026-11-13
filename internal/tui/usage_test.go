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

// A session that counted an unpriced model reports tokens without a dollar
// figure: the terminal never prints a cost it cannot support.
func TestHeaderShowsTokensWithoutCostWhenUnpriced(t *testing.T) {
	m, _ := usedBoard(t, orchestrator.SessionUsage{
		InputTokens: 12_300, OutputTokens: 4_500, TotalTokens: 16_800,
		CostUSD: 0.9, Priced: false, Tasks: 1, Unpriced: []string{"openrouter/mystery"},
	})
	top := ansi.Strip(strings.Split(m.View(), "\n")[0])
	if !strings.Contains(top, "Σ 12.3k in · 4.5k out") {
		t.Fatalf("header missing the unpriced usage segment: %q", top)
	}
	if strings.Contains(top, "$") {
		t.Fatalf("header printed a cost for an unpriced session: %q", top)
	}
}

// The usage segment gives way as the terminal narrows, and adding it never
// changes the frame's shape or pushes the wordmark and badge out.
func TestHeaderUsageFitsNarrowTerminals(t *testing.T) {
	total := orchestrator.SessionUsage{
		InputTokens: 1_234_567, OutputTokens: 987_654, TotalTokens: 2_222_221,
		CostUSD: 4.21, Priced: true, Tasks: 3,
	}
	for _, width := range []int{40, 60, 76, 80, 120} {
		idle, _ := usedBoard(t, orchestrator.SessionUsage{})
		idle.width, idle.height = width, 24
		used, _ := usedBoard(t, total)
		used.width, used.height = width, 24
		view := used.View()
		assertFits(t, view, width, 24)
		if got, want := lipgloss.Height(view), lipgloss.Height(idle.View()); got != want {
			t.Fatalf("width %d: usage changed the frame height from %d to %d rows", width, want, got)
		}
		top := ansi.Strip(strings.Split(view, "\n")[0])
		for _, want := range []string{"PILHARMONIC", "TASKS"} {
			if !strings.Contains(top, want) {
				t.Fatalf("width %d: header lost %q: %q", width, want, top)
			}
		}
		if strings.Contains(top, "…") {
			t.Fatalf("width %d: header truncated instead of dropping the segment: %q", width, top)
		}
	}
}

// The usage segment shortens, and then leaves, before the counters lose their
// wording: naming what needs action matters more than the session total.
func TestHeaderUsageShortensBeforeTheCounters(t *testing.T) {
	total := orchestrator.SessionUsage{
		InputTokens: 1_234_567, OutputTokens: 987_654, TotalTokens: 2_222_221,
		CostUSD: 4.21, Priced: true, Tasks: 1,
	}
	cases := []struct {
		width      int
		wantUsage  string
		wantAction string
	}{
		// The whole segment fits, tokens and estimate.
		{120, "Σ 1.2M in · 987.7k out · $4.21", "◆ 0 need action"},
		// Only the compact form fits beside the full counters.
		{100, "Σ 1.2M/987.7k · $4.21", "◆ 0 need action"},
		// Nothing but the estimate fits, and the counters have already
		// shortened, so the segment still gets a chance.
		{60, "Σ $4.21", "◆0"},
		// The wording of the counters is worth more than the total here.
		{80, "", "◆ 0 need action"},
		{76, "", "◆ 0 need action"},
		{40, "", "◆0"},
	}
	for _, tc := range cases {
		m, _ := usedBoard(t, total)
		m.width, m.height = tc.width, 24
		top := ansi.Strip(strings.Split(m.View(), "\n")[0])
		if got := strings.Contains(top, "Σ"); got != (tc.wantUsage != "") {
			t.Fatalf("width %d: usage present = %v, want %v: %q", tc.width, got, tc.wantUsage != "", top)
		}
		if tc.wantUsage != "" && !strings.Contains(top, tc.wantUsage) {
			t.Fatalf("width %d: header missing %q: %q", tc.width, tc.wantUsage, top)
		}
		if !strings.Contains(top, tc.wantAction) {
			t.Fatalf("width %d: header lost %q: %q", tc.width, tc.wantAction, top)
		}
	}
}

// The detail panel prices the model a task last used at the rate in force now,
// and labels a time-of-day model with the provider and the current window.
func TestDetailShowsUsageAndPrice(t *testing.T) {
	const selector = "deepseek/deepseek-chat"
	task := orchestrator.Task{
		ID: "task", Title: "work", Prompt: "do the work", Agent: "general",
		Attempts: []orchestrator.Attempt{{
			ID: "attempt", RequestedModel: selector,
			Usage: &orchestrator.AttemptUsage{Model: selector, InputTokens: 12_300, OutputTokens: 4_500, CostUSD: 0.0043, Priced: true},
		}},
	}
	m := model{width: 120, height: 40, tasks: []orchestrator.Task{task}}
	detail := ansi.Strip(strings.Join(m.detailContent(100), "\n"))
	if want := "Usage: 12.3k in · 4.5k out · $0.0043"; !strings.Contains(detail, want) {
		t.Fatalf("detail missing %q: %s", want, detail)
	}
	if strings.Index(detail, "Usage:") < strings.Index(detail, "Last attempt model:") {
		t.Fatalf("usage line moved above the model line: %s", detail)
	}

	// PriceFor reads the clock, so the expected label is derived rather than
	// hardcoded: DeepSeek charges list price at peak and the discount off-peak.
	now := time.Now()
	price, known := orchestrator.PriceFor(selector, now)
	if !known {
		t.Fatalf("%s has no curated price", selector)
	}
	when := price.Rate(now)
	if after := price.Rate(time.Now()); after != when {
		t.Skipf("pricing window closed while rendering: %s then %s", when, after)
	}
	want := "Price: $0.28/M in · $0.42/M out · deepseek peak"
	if when == orchestrator.RateOffPeak {
		want = "Price: $0.14/M in · $0.21/M out · deepseek off-peak"
	}
	if !strings.Contains(detail, want) {
		t.Fatalf("detail missing %q (rate %q): %s", want, when, detail)
	}
}

// An attempt that straddled the discount window is labelled with both rates
// instead of the one in force now.
func TestDetailLabelsMixedRateAsBothWindows(t *testing.T) {
	const selector = "deepseek/deepseek-chat"
	task := orchestrator.Task{
		ID: "task", Title: "work", Prompt: "do the work", Agent: "general",
		Attempts: []orchestrator.Attempt{{
			ID: "attempt", RequestedModel: selector,
			Usage: &orchestrator.AttemptUsage{Model: selector, InputTokens: 12_300, OutputTokens: 4_500, CostUSD: 0.0043, Priced: true, Rate: orchestrator.RateMixed},
		}},
	}
	m := model{width: 120, height: 40, tasks: []orchestrator.Task{task}}
	detail := ansi.Strip(strings.Join(m.detailContent(100), "\n"))
	if !strings.Contains(detail, "· deepseek peak/off-peak") {
		t.Fatalf("mixed rate is not labelled with both windows: %s", detail)
	}
}

// An all-day model carries no window suffix, and an unpriced one prints no
// price and no cost.
func TestDetailPriceSuffixDependsOnTheModel(t *testing.T) {
	allDay := orchestrator.Task{
		ID: "all-day", Title: "work", Prompt: "do the work", Agent: "general",
		Attempts: []orchestrator.Attempt{{
			ID: "attempt", RequestedModel: "openai/gpt-4.1",
			Usage: &orchestrator.AttemptUsage{Model: "openai/gpt-4.1", InputTokens: 2_000, OutputTokens: 1_000, CostUSD: 0.012, Priced: true},
		}},
	}
	m := model{width: 120, height: 40, tasks: []orchestrator.Task{allDay}}
	detail := ansi.Strip(strings.Join(m.detailContent(100), "\n"))
	if want := "Price: $2/M in · $8/M out"; !strings.Contains(detail, want) {
		t.Fatalf("detail missing %q: %s", want, detail)
	}
	if strings.Contains(detail, "peak") || strings.Contains(detail, "openai ") {
		t.Fatalf("all-day model carries a window suffix: %s", detail)
	}

	unpriced := allDay
	unpriced.ID, unpriced.Title = "unpriced", "reseller"
	unpriced.Attempts = []orchestrator.Attempt{{
		ID: "attempt", RequestedModel: "openrouter/mystery",
		Usage: &orchestrator.AttemptUsage{Model: "openrouter/mystery", InputTokens: 2_000, OutputTokens: 1_000},
	}}
	m = model{width: 120, height: 40, tasks: []orchestrator.Task{unpriced}}
	detail = ansi.Strip(strings.Join(m.detailContent(100), "\n"))
	if want := "Usage: 2k in · 1k out · cost unknown"; !strings.Contains(detail, want) {
		t.Fatalf("detail missing %q: %s", want, detail)
	}
	if strings.Contains(detail, "Price:") || strings.Contains(detail, "$") {
		t.Fatalf("unpriced task printed a price: %s", detail)
	}
}

// A task that recorded nothing shows no usage line at all.
func TestDetailOmitsUsageWhenNothingWasRecorded(t *testing.T) {
	task := orchestrator.Task{
		ID: "task", Title: "work", Prompt: "do the work", Agent: "general",
		Attempts: []orchestrator.Attempt{{ID: "attempt", RequestedModel: "openai/gpt-4.1"}},
	}
	m := model{width: 120, height: 40, tasks: []orchestrator.Task{task}}
	detail := ansi.Strip(strings.Join(m.detailContent(100), "\n"))
	if strings.Contains(detail, "Usage:") || strings.Contains(detail, "Price:") {
		t.Fatalf("detail invented usage for an attempt without it: %s", detail)
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
