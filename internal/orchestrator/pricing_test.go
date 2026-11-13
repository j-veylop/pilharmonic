package orchestrator

import (
	"math"
	"testing"
	"time"
)

// floatMatches compares two dollar amounts with the tolerance float64 arithmetic
// on prices quoted per 1M tokens needs. It is far tighter than any amount an
// operator could see, so a rounding difference cannot hide a real error.
func floatMatches(got, want float64) bool { return math.Abs(got-want) <= 1e-12 }

// TestDeepSeekOffPeakBoundaries pins both edges of the daily discount window to
// the second and in both directions: a report one second before 16:30 UTC is
// peak, one second later is discounted, one second before 00:30 UTC is still
// discounted and one second later is peak again.
func TestDeepSeekOffPeakBoundaries(t *testing.T) {
	day := time.Date(2025, 9, 1, 0, 0, 0, 0, time.UTC)
	at := func(hour, minute, second int) time.Time {
		return day.Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute + time.Duration(second)*time.Second)
	}
	cases := []struct {
		name string
		when time.Time
		want bool
	}{
		{"one second before the window opens", at(16, 29, 59), false},
		{"the window opens", at(16, 30, 0), true},
		{"inside the window", at(20, 0, 0), true},
		{"one second before the window closes", at(0, 29, 59), true},
		{"the window closes", at(0, 30, 0), false},
		{"one second after the window closes", at(0, 30, 1), false},
		{"deep in peak hours", at(12, 0, 0), false},
	}
	for _, tc := range cases {
		if got := deepseekOffPeak(tc.when); got != tc.want {
			t.Errorf("%s (%s): off-peak = %v, want %v", tc.name, tc.when.Format(time.RFC3339), got, tc.want)
		}
	}
	// The rate must flip exactly at each boundary, so neither edge can drift by
	// a minute without this failing.
	if deepseekOffPeak(at(16, 29, 59)) == deepseekOffPeak(at(16, 30, 0)) {
		t.Error("16:30 UTC does not toggle the discount")
	}
	if deepseekOffPeak(at(0, 29, 59)) == deepseekOffPeak(at(0, 30, 0)) {
		t.Error("00:30 UTC does not toggle the discount")
	}

	// The window is published in Beijing time (UTC+8): 00:30-08:30 there is
	// 16:30-00:30 UTC, and the same boundaries hold in that zone.
	beijing := time.FixedZone("CST", 8*60*60)
	beijingCases := []struct {
		name string
		when time.Time
		want bool
	}{
		{"00:29 Beijing", time.Date(2025, 9, 1, 0, 29, 59, 0, beijing), false},
		{"00:30 Beijing", time.Date(2025, 9, 1, 0, 30, 0, 0, beijing), true},
		{"08:29 Beijing", time.Date(2025, 9, 1, 8, 29, 59, 0, beijing), true},
		{"08:30 Beijing", time.Date(2025, 9, 1, 8, 30, 0, 0, beijing), false},
	}
	for _, tc := range beijingCases {
		if got := deepseekOffPeak(tc.when); got != tc.want {
			t.Errorf("%s (%s): off-peak = %v, want %v", tc.name, tc.when.UTC().Format(time.RFC3339), got, tc.want)
		}
	}
}

// TestPriceForDeepSeekDiscountedRates checks both DeepSeek models apply their
// own off-peak pair rather than one shared discount: deepseek-chat is half
// price, deepseek-reasoner is 75% off input and half price on output.
func TestPriceForDeepSeekDiscountedRates(t *testing.T) {
	peak := time.Date(2025, 9, 1, 12, 0, 0, 0, time.UTC)
	offPeak := time.Date(2025, 9, 1, 20, 0, 0, 0, time.UTC)
	cases := []struct {
		model                       string
		peakInput, peakOutput       float64
		offPeakInput, offPeakOutput float64
	}{
		{"deepseek/deepseek-chat", 0.28, 0.42, 0.14, 0.21},
		{"deepseek/deepseek-reasoner", 0.28, 0.42, 0.07, 0.21},
	}
	for _, tc := range cases {
		price, ok := PriceFor(tc.model, peak)
		if !ok {
			t.Fatalf("%s has no curated price", tc.model)
		}
		if !price.TimeOfDay {
			t.Errorf("%s is not marked time-of-day", tc.model)
		}
		if got := price.Rate(peak); got != RatePeak {
			t.Errorf("%s peak rate label = %q, want %q", tc.model, got, RatePeak)
		}
		if got := price.Rate(offPeak); got != RateOffPeak {
			t.Errorf("%s off-peak rate label = %q, want %q", tc.model, got, RateOffPeak)
		}
		if got := price.InputRate(peak); got != tc.peakInput {
			t.Errorf("%s peak input rate = %v, want %v", tc.model, got, tc.peakInput)
		}
		if got := price.OutputRate(peak); got != tc.peakOutput {
			t.Errorf("%s peak output rate = %v, want %v", tc.model, got, tc.peakOutput)
		}
		if got := price.InputRate(offPeak); got != tc.offPeakInput {
			t.Errorf("%s off-peak input rate = %v, want %v", tc.model, got, tc.offPeakInput)
		}
		if got := price.OutputRate(offPeak); got != tc.offPeakOutput {
			t.Errorf("%s off-peak output rate = %v, want %v", tc.model, got, tc.offPeakOutput)
		}
		// A full million of each half costs exactly the published pair, which is
		// the unit the table is written in.
		full := TokenUsage{InputTokens: millionTokens, OutputTokens: millionTokens}
		if got, want := price.Cost(full, peak), tc.peakInput+tc.peakOutput; !floatMatches(got, want) {
			t.Errorf("%s peak cost of 1M/1M = %v, want %v", tc.model, got, want)
		}
		if got, want := price.Cost(full, offPeak), tc.offPeakInput+tc.offPeakOutput; !floatMatches(got, want) {
			t.Errorf("%s off-peak cost of 1M/1M = %v, want %v", tc.model, got, want)
		}
	}
	// The discount is a discount: an off-peak call is never charged more than
	// the same call at peak.
	reasoner, _ := PriceFor("deepseek/deepseek-reasoner", peak)
	sample := TokenUsage{InputTokens: 12345, OutputTokens: 6789}
	if reasoner.Cost(sample, offPeak) >= reasoner.Cost(sample, peak) {
		t.Errorf("off-peak reasoner cost %v is not below peak cost %v",
			reasoner.Cost(sample, offPeak), reasoner.Cost(sample, peak))
	}
}

// TestModelPriceCostScalesPerMillionTokens checks a price quoted per 1M tokens
// is charged in direct proportion to a non-round token count instead of being
// rounded to whole millions or cents.
func TestModelPriceCostScalesPerMillionTokens(t *testing.T) {
	peak := time.Date(2025, 9, 1, 12, 0, 0, 0, time.UTC)
	offPeak := time.Date(2025, 9, 1, 20, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		model string
		when  time.Time
		usage TokenUsage
		want  float64
	}{
		{"gpt-4.1 12345 in and 6789 out", "openai/gpt-4.1", peak, TokenUsage{InputTokens: 12345, OutputTokens: 6789}, 0.079002},
		{"gpt-4.1 12345 input only", "openai/gpt-4.1", peak, TokenUsage{InputTokens: 12345}, 0.02469},
		{"gpt-5-mini 12345 input only", "openai/gpt-5-mini", peak, TokenUsage{InputTokens: 12345}, 0.00308625},
		{"reasoner 12345 in and 6789 out at peak", "deepseek/deepseek-reasoner", peak, TokenUsage{InputTokens: 12345, OutputTokens: 6789}, 0.00630798},
		{"reasoner 12345 in and 6789 out off peak", "deepseek/deepseek-reasoner", offPeak, TokenUsage{InputTokens: 12345, OutputTokens: 6789}, 0.00228984},
		{"one million of each half", "openai/gpt-4.1", peak, TokenUsage{InputTokens: millionTokens, OutputTokens: millionTokens}, 10},
		{"no tokens", "deepseek/deepseek-chat", offPeak, TokenUsage{}, 0},
	}
	for _, tc := range cases {
		price, ok := PriceFor(tc.model, tc.when)
		if !ok {
			t.Fatalf("%s has no curated price", tc.model)
		}
		got := price.Cost(tc.usage, tc.when)
		if !floatMatches(got, tc.want) {
			t.Errorf("%s: cost = %v, want %v", tc.name, got, tc.want)
		}
		doubled := TokenUsage{InputTokens: tc.usage.InputTokens * 2, OutputTokens: tc.usage.OutputTokens * 2}
		if got := price.Cost(doubled, tc.when); !floatMatches(got, tc.want*2) {
			t.Errorf("%s: doubling the tokens cost %v, want %v", tc.name, got, tc.want*2)
		}
	}
}

// TestPriceForRejectsUnpricedSelectors proves an unknown selector is reported as
// unpriced rather than priced as some other model: a session total must never
// claim a cost it cannot support.
func TestPriceForRejectsUnpricedSelectors(t *testing.T) {
	now := time.Date(2025, 9, 1, 12, 0, 0, 0, time.UTC)
	for _, selector := range []string{
		"",
		"   ",
		"\t\n",
		"deepseek",
		"provider/",
		"nosuch/model",
		"openai/gpt-4",
		"openrouter/deepseek-chat",
		"openai/gpt-4.1-2024-08-06",
	} {
		if price, ok := PriceFor(selector, now); ok {
			t.Errorf("selector %q was priced as %+v", selector, price)
		}
	}
}

// TestPriceForNormalizesSelector checks a selector the adapter reports with
// stray case or spacing still resolves to the curated entry, and reports the
// canonical selector so the per-model breakdown does not split one model in two.
func TestPriceForNormalizesSelector(t *testing.T) {
	now := time.Date(2025, 9, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		selector  string
		wantModel string
		wantTOD   bool
	}{
		{" OpenAI/GPT-4.1 ", "openai/gpt-4.1", false},
		{"DeepSeek/DeepSeek-Reasoner", "deepseek/deepseek-reasoner", true},
		{"deepseek/deepseek-chat\n", "deepseek/deepseek-chat", true},
	}
	for _, tc := range cases {
		price, ok := PriceFor(tc.selector, now)
		if !ok {
			t.Errorf("selector %q was not priced", tc.selector)
			continue
		}
		if price.Model != tc.wantModel {
			t.Errorf("selector %q priced as %q, want %q", tc.selector, price.Model, tc.wantModel)
		}
		if price.TimeOfDay != tc.wantTOD {
			t.Errorf("selector %q time-of-day = %v, want %v", tc.selector, price.TimeOfDay, tc.wantTOD)
		}
	}
}
