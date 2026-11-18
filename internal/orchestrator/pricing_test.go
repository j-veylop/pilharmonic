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

// TestDeepSeekOffPeakBoundaries pins both edges of the daily discount window
// (16:30-00:30 UTC, published as 00:30-08:30 Beijing time) to the second.
func TestDeepSeekOffPeakBoundaries(t *testing.T) {
	beijing := time.FixedZone("CST", 8*60*60)
	cases := []struct {
		when time.Time
		want bool
	}{
		{time.Date(2025, 9, 1, 16, 29, 59, 0, time.UTC), false},
		{time.Date(2025, 9, 1, 16, 30, 0, 0, time.UTC), true},
		{time.Date(2025, 9, 1, 0, 29, 59, 0, time.UTC), true},
		{time.Date(2025, 9, 1, 0, 30, 0, 0, time.UTC), false},
		{time.Date(2025, 9, 1, 0, 30, 0, 0, beijing), true},
		{time.Date(2025, 9, 1, 8, 30, 0, 0, beijing), false},
	}
	for _, tc := range cases {
		if got := deepseekOffPeak(tc.when); got != tc.want {
			t.Errorf("%s: off-peak = %v, want %v", tc.when.Format(time.RFC3339), got, tc.want)
		}
	}
}

// TestProviderRateNamesTheWindowForAnyModel covers the badge on selectors the
// curated price table does not list: the window belongs to the provider, so any
// DeepSeek model carries it, while all-day and unknown providers carry none.
func TestProviderRateNamesTheWindowForAnyModel(t *testing.T) {
	peak := time.Date(2025, 9, 1, 12, 0, 0, 0, time.UTC)
	offPeak := time.Date(2025, 9, 1, 20, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		provider string
		at       time.Time
		want     string
	}{
		{"deepseek", peak, RatePeak},
		{"DeepSeek", offPeak, RateOffPeak},
		{" deepseek ", offPeak, RateOffPeak},
		{"openai", offPeak, ""},
		{"openrouter", offPeak, ""},
		{"", offPeak, ""},
	} {
		if got := ProviderRate(tc.provider, tc.at); got != tc.want {
			t.Errorf("%q at %s: rate %q, want %q", tc.provider, tc.at.Format("15:04"), got, tc.want)
		}
	}
	// Naming the provider window must not invent a price for a model the
	// curated table does not list.
	if _, ok := PriceFor("deepseek/deepseek-flash", offPeak); ok {
		t.Errorf("unpriced DeepSeek model gained a curated cost")
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
	}
}

// TestPriceForSelectors checks an unknown selector is unpriced rather than
// priced as some other model, and a selector with stray case or spacing
// resolves to the canonical entry so the per-model breakdown does not split.
func TestPriceForSelectors(t *testing.T) {
	now := time.Date(2025, 9, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		selector  string
		wantModel string
		wantTOD   bool
	}{
		{"", "", false},
		{"\t\n", "", false},
		{"deepseek", "", false},
		{"provider/", "", false},
		{"nosuch/model", "", false},
		{"openai/gpt-4", "", false},
		{"openrouter/deepseek-chat", "", false},
		{"openai/gpt-4.1-2024-08-06", "", false},
		{" OpenAI/GPT-4.1 ", "openai/gpt-4.1", false},
		{"DeepSeek/DeepSeek-Reasoner", "deepseek/deepseek-reasoner", true},
		{"deepseek/deepseek-chat\n", "deepseek/deepseek-chat", true},
	}
	for _, tc := range cases {
		price, ok := PriceFor(tc.selector, now)
		if ok != (tc.wantModel != "") {
			t.Errorf("selector %q priced = %v (%+v)", tc.selector, ok, price)
			continue
		}
		if !ok {
			continue
		}
		if price.Model != tc.wantModel {
			t.Errorf("selector %q priced as %q, want %q", tc.selector, price.Model, tc.wantModel)
		}
		if price.TimeOfDay != tc.wantTOD || (!tc.wantTOD && price.Rate(now) != "") {
			t.Errorf("selector %q time-of-day = %v, want %v", tc.selector, price.TimeOfDay, tc.wantTOD)
		}
	}
}
