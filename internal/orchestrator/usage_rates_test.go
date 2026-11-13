package orchestrator

import (
	"context"
	"io"
	"testing"
	"time"
)

// TestAttemptUsageAddUsageMixesRates pins the durable rate label for an attempt
// that recorded usage under more than one price. deepseekOffPeak reads the wall
// clock, so a two-call service run cannot force a rate change; the mixing rule
// is asserted on addUsage directly instead.
func TestAttemptUsageAddUsageMixesRates(t *testing.T) {
	first := TokenUsage{InputTokens: 100, OutputTokens: 10}
	second := TokenUsage{InputTokens: 200, OutputTokens: 20}
	cases := []struct {
		name  string
		rates []string
		want  string
	}{
		{"peak then off-peak is mixed", []string{RatePeak, RateOffPeak}, RateMixed},
		{"off-peak then peak is mixed", []string{RateOffPeak, RatePeak}, RateMixed},
		{"the same peak rate twice keeps peak", []string{RatePeak, RatePeak}, RatePeak},
		{"the same off-peak rate twice keeps off-peak", []string{RateOffPeak, RateOffPeak}, RateOffPeak},
		{"an all-day model keeps the empty rate", []string{"", ""}, ""},
		{"an all-day report does not discount-label a priced one", []string{RateOffPeak, ""}, RateOffPeak},
		{"an all-day report then a peak one becomes peak", []string{"", RatePeak}, RatePeak},
		{"mixed stays mixed", []string{RatePeak, RateOffPeak, RatePeak}, RateMixed},
	}
	for _, tc := range cases {
		usage := &AttemptUsage{}
		for i, rate := range tc.rates {
			// The cost is fixed by the caller at recording time, so folding in a
			// count must never rewrite it.
			usage.CostUSD = 0.5
			tokens := first
			if i%2 == 1 {
				tokens = second
			}
			usage.addUsage(tokens, rate)
			if usage.CostUSD != 0.5 {
				t.Fatalf("%s: addUsage changed the recorded cost to %v", tc.name, usage.CostUSD)
			}
		}
		if usage.Rate != tc.want {
			t.Errorf("%s: rate = %q, want %q", tc.name, usage.Rate, tc.want)
		}
		// Both halves accumulate regardless of the rate label.
		wantInput, wantOutput := int64(0), int64(0)
		for i := range tc.rates {
			tokens := first
			if i%2 == 1 {
				tokens = second
			}
			wantInput += tokens.InputTokens
			wantOutput += tokens.OutputTokens
		}
		if usage.InputTokens != wantInput || usage.OutputTokens != wantOutput {
			t.Errorf("%s: tokens = %d/%d, want %d/%d", tc.name,
				usage.InputTokens, usage.OutputTokens, wantInput, wantOutput)
		}
	}
}

// TestAttemptUsageAddUsageFromEmptyRecord sets the label from the first priced
// report, which is what a session sees for every attempt that is charged once.
func TestAttemptUsageAddUsageFromEmptyRecord(t *testing.T) {
	cases := []struct {
		rate string
		want string
	}{
		{RatePeak, RatePeak},
		{RateOffPeak, RateOffPeak},
		{"", ""},
	}
	for _, tc := range cases {
		usage := &AttemptUsage{}
		usage.addUsage(TokenUsage{InputTokens: 1}, tc.rate)
		if usage.Rate != tc.want {
			t.Errorf("first report at rate %q labelled the attempt %q", tc.rate, usage.Rate)
		}
	}
}

// TestRecordUsageSumsTwoCallsOnOneAttempt records usage twice through the real
// sink for a model priced the same all day, so both calls use one known rate and
// the expected total is exact. Recording twice is how one attempt makes several
// provider calls.
func TestRecordUsageSumsTwoCallsOnOneAttempt(t *testing.T) {
	s := openUsageTestService(t)
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, _ Task, out io.Writer) error {
		recorder, err := usageRecorderFor(out)
		if err != nil {
			return err
		}
		if err := recorder.RecordUsage("openai/gpt-4.1", TokenUsage{InputTokens: 1000, OutputTokens: 100}); err != nil {
			return err
		}
		return recorder.RecordUsage("openai/gpt-4.1", TokenUsage{InputTokens: 2000, OutputTokens: 300})
	}))
	task, err := s.CreateTask("two calls", "do work", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	done := await(t, s, task.ID, Completed)
	if len(done.Attempts) != 1 || done.Attempts[0].Usage == nil {
		t.Fatalf("attempt usage missing: %+v", done.Attempts)
	}
	usage := done.Attempts[0].Usage
	if usage.InputTokens != 3000 || usage.OutputTokens != 400 {
		t.Fatalf("recorded tokens = %d/%d, want 3000/400", usage.InputTokens, usage.OutputTokens)
	}
	// An all-day model keeps the empty rate: two calls at the same rate are not
	// a mixed-rate attempt.
	if usage.Rate != "" {
		t.Fatalf("all-day model recorded rate %q, want the empty label", usage.Rate)
	}
	if usage.Model != "openai/gpt-4.1" || !usage.Priced {
		t.Fatalf("attempt usage = %+v", usage)
	}
	// 3000*2/1M + 400*8/1M = 0.006 + 0.0032.
	wantCost := 3000*2.0/millionTokens + 400*8.0/millionTokens
	if !floatMatches(usage.CostUSD, wantCost) {
		t.Fatalf("attempt cost = %v, want %v", usage.CostUSD, wantCost)
	}
	total := done.Usage()
	if total.InputTokens != 3000 || total.OutputTokens != 400 || !floatMatches(total.CostUSD, wantCost) {
		t.Fatalf("task usage = %+v", total)
	}
	session := s.SessionUsage()
	if session.InputTokens != 3000 || session.OutputTokens != 400 || !floatMatches(session.CostUSD, wantCost) {
		t.Fatalf("session usage = %+v", session)
	}
}

// TestRecordUsageTwiceForTimeOfDayModelKeepsOneRate records two calls for a
// model with time-of-day pricing inside one window. Both calls are charged the
// rate in force, the costs sum, and the attempt is not labelled mixed.
func TestRecordUsageTwiceForTimeOfDayModelKeepsOneRate(t *testing.T) {
	const model = "deepseek/deepseek-reasoner"
	call := TokenUsage{InputTokens: 12345, OutputTokens: 6789}
	before := time.Now().UTC()
	s := openUsageTestService(t)
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, _ Task, out io.Writer) error {
		recorder, err := usageRecorderFor(out)
		if err != nil {
			return err
		}
		if err := recorder.RecordUsage(model, call); err != nil {
			return err
		}
		return recorder.RecordUsage(model, call)
	}))
	task, err := s.CreateTask("two calls", "do work", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	done := await(t, s, task.ID, Completed)
	after := time.Now().UTC()
	price, ok := PriceFor(model, before)
	if !ok {
		t.Fatalf("%s has no curated price", model)
	}
	rate := price.Rate(before)
	if price.Rate(after) != rate {
		// The run straddled the off-peak boundary, so the two calls legitimately
		// used different prices and an exact total cannot be asserted.
		t.Skipf("run straddled the %s boundary; rate changed from %q to %q", model, rate, price.Rate(after))
	}
	usage := done.Attempts[len(done.Attempts)-1].Usage
	if usage == nil {
		t.Fatal("attempt usage missing")
	}
	if usage.Rate != rate {
		t.Fatalf("rate = %q, want %q (two calls in one window are not mixed)", usage.Rate, rate)
	}
	if usage.InputTokens != 2*call.InputTokens || usage.OutputTokens != 2*call.OutputTokens {
		t.Fatalf("tokens = %d/%d, want %d/%d", usage.InputTokens, usage.OutputTokens, 2*call.InputTokens, 2*call.OutputTokens)
	}
	wantCost := 2 * price.Cost(call, before)
	if !floatMatches(usage.CostUSD, wantCost) {
		t.Fatalf("cost = %v, want %v", usage.CostUSD, wantCost)
	}
	session := s.SessionUsage()
	if len(session.Models) != 1 || session.Models[0].CurrentRate != rate {
		t.Fatalf("session model rate = %+v, want %q", session.Models, rate)
	}
	if !floatMatches(session.CostUSD, wantCost) {
		t.Fatalf("session cost = %v, want %v", session.CostUSD, wantCost)
	}
}

// TestRecordUsageMarksAttemptUnpricedWhenOneCallIsUnpriced checks an attempt
// that used both a priced and an unpriced model in either order: every token is
// counted, only the priced call contributes money, and an attempt with any
// unpriced call is never reported as fully priced.
func TestRecordUsageMarksAttemptUnpricedWhenOneCallIsUnpriced(t *testing.T) {
	pricedCall := usageCall{model: "openai/gpt-4.1", usage: TokenUsage{InputTokens: 1000, OutputTokens: 500}}
	unpricedCall := usageCall{model: "nosuch/model", usage: TokenUsage{InputTokens: 7, OutputTokens: 3}}
	// 1000*2/1M + 500*8/1M = 0.006, and only that half is charged.
	wantCost := 1000*2.0/millionTokens + 500*8.0/millionTokens
	cases := []struct {
		name  string
		calls []usageCall
		model string
	}{
		{"priced then unpriced", []usageCall{pricedCall, unpricedCall}, "openai/gpt-4.1"},
		{"unpriced then priced", []usageCall{unpricedCall, pricedCall}, "nosuch/model"},
	}
	for _, tc := range cases {
		calls := tc.calls
		s := openUsageTestService(t)
		s.RegisterRunner("general", runnerFunc(func(_ context.Context, _ Task, out io.Writer) error {
			recorder, err := usageRecorderFor(out)
			if err != nil {
				return err
			}
			for _, call := range calls {
				if err := recorder.RecordUsage(call.model, call.usage); err != nil {
					return err
				}
			}
			return nil
		}))
		task, err := s.CreateTask("mixed models", "do work", t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if err = s.Start(context.Background(), task.ID); err != nil {
			t.Fatal(err)
		}
		done := await(t, s, task.ID, Completed)
		usage := done.Attempts[0].Usage
		if usage == nil || usage.InputTokens != 1007 || usage.OutputTokens != 503 {
			t.Fatalf("%s: attempt tokens = %+v, want every token counted", tc.name, usage)
		}
		if usage.Priced {
			t.Fatalf("%s: attempt with an unpriced call was reported as priced: %+v", tc.name, usage)
		}
		if !floatMatches(usage.CostUSD, wantCost) {
			t.Fatalf("%s: attempt cost = %v, want only the priced call %v", tc.name, usage.CostUSD, wantCost)
		}
		// One attempt record carries one model selector, so it keeps the model of
		// its first report.
		if usage.Model != tc.model {
			t.Fatalf("%s: attempt model = %q, want %q", tc.name, usage.Model, tc.model)
		}
		session := s.SessionUsage()
		if session.Priced || !floatMatches(session.CostUSD, wantCost) || session.InputTokens != 1007 {
			t.Fatalf("%s: session usage = %+v", tc.name, session)
		}
		// The attempt record cannot name the unpriced call, so the session names
		// the attempt's recorded selector; it must still admit the work is
		// partly unpriced rather than claim a complete cost.
		if len(session.Unpriced) != 1 {
			t.Fatalf("%s: unpriced selectors = %v", tc.name, session.Unpriced)
		}
	}
}
