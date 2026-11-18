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

// TestDeepSeekOffPeakBoundaries pins both peak windows, the weekend exception
// and a Chinese public holiday to the second. DeepSeek publishes peak hours in
// UTC: 01:00–04:00 and 06:00–10:00, Monday through Friday, excluding Chinese
// public holidays.
func TestDeepSeekOffPeakBoundaries(t *testing.T) {
	monday := func(hour, minute, second int) time.Time {
		return time.Date(2025, 9, 1, hour, minute, second, 0, time.UTC)
	}
	cases := []struct {
		name string
		when time.Time
		want bool
	}{
		{"before the first peak window", monday(0, 59, 59), true},
		{"the first peak window opens", monday(1, 0, 0), false},
		{"the first peak window closes", monday(4, 0, 0), true},
		{"between the peak windows", monday(5, 59, 59), true},
		{"the second peak window opens", monday(6, 0, 0), false},
		{"inside the second peak window", monday(9, 59, 59), false},
		{"the second peak window closes", monday(10, 0, 0), true},
		{"after the second peak window", monday(23, 59, 59), true},
		{"a Saturday stays off-peak", time.Date(2025, 9, 6, 2, 0, 0, 0, time.UTC), true},
		{"a Sunday stays off-peak", time.Date(2025, 9, 7, 2, 0, 0, 0, time.UTC), true},
		{"a weekday holiday stays off-peak", time.Date(2025, 10, 1, 2, 0, 0, 0, time.UTC), true},
	}
	for _, tc := range cases {
		if got := deepseekOffPeak(tc.when); got != tc.want {
			t.Errorf("%s (%s): off-peak = %v, want %v", tc.name, tc.when.Format(time.RFC3339), got, tc.want)
		}
	}

	// The window is published in UTC, so the same instant in any zone must be
	// judged by its UTC time: 02:00 UTC is peak, 12:00 UTC is off-peak.
	beijing := time.FixedZone("CST", 8*60*60)
	if deepseekOffPeak(time.Date(2025, 9, 1, 10, 0, 0, 0, beijing)) {
		t.Error("10:00 Beijing (02:00 UTC) should be peak")
	}
	if !deepseekOffPeak(time.Date(2025, 9, 1, 20, 0, 0, 0, beijing)) {
		t.Error("20:00 Beijing (12:00 UTC) should be off-peak")
	}
}

// TestProviderRateNamesTheWindowForAnyModel covers the badge on selectors the
// curated price table does not list: the window belongs to the provider, so any
// DeepSeek model carries it, while all-day and unknown providers carry none.
func TestProviderRateNamesTheWindowForAnyModel(t *testing.T) {
	peak := time.Date(2025, 9, 1, 2, 0, 0, 0, time.UTC)
	offPeak := time.Date(2025, 9, 1, 12, 0, 0, 0, time.UTC)
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
	if _, ok := PriceFor("deepseek/deepseek-unlisted", offPeak); ok {
		t.Errorf("unpriced DeepSeek model gained a curated cost")
	}
}

// TestPriceForDeepSeekDiscountedRates checks every DeepSeek model applies its
// own off-peak pair rather than one shared discount: deepseek-chat is half
// price, deepseek-reasoner is 75% off input and half price on output, and the
// current flash and v4-pro models are half price across every bucket.
func TestPriceForDeepSeekDiscountedRates(t *testing.T) {
	peak := time.Date(2025, 9, 1, 2, 0, 0, 0, time.UTC)
	offPeak := time.Date(2025, 9, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		model                                 string
		peakInput, peakOutput, peakCache      float64
		offPeakInput, offPeakOutput, offCache float64
	}{
		{"deepseek/deepseek-flash", 0.30, 1.20, 0.006, 0.15, 0.60, 0.003},
		{"deepseek/deepseek-v4-pro", 1.32, 3.96, 0.044, 0.66, 1.98, 0.022},
		{"deepseek/deepseek-chat", 0.28, 0.42, 0.028, 0.14, 0.21, 0.014},
		{"deepseek/deepseek-reasoner", 0.28, 0.42, 0.028, 0.07, 0.21, 0.014},
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
		if got := price.CacheReadRate(peak); got != tc.peakCache {
			t.Errorf("%s peak cache-read rate = %v, want %v", tc.model, got, tc.peakCache)
		}
		if got := price.CacheReadRate(offPeak); got != tc.offCache {
			t.Errorf("%s off-peak cache-read rate = %v, want %v", tc.model, got, tc.offCache)
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

// TestSubscriptionModelsUseAPIPrices covers the ChatGPT subscription catalog:
// subscription usage is not billed per token, so its estimate assumes the API
// list price of the same Codex model.
func TestSubscriptionModelsUseAPIPrices(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		model      string
		input, out float64
		cache      float64
	}{
		{"openai-subscription/gpt-5-codex", 1.25, 10, 0.125},
		{"openai-subscription/gpt-5.1-codex", 1.25, 10, 0.125},
		{"openai-subscription/gpt-5.1-codex-max", 1.25, 10, 0.125},
		{"openai-subscription/gpt-5.1-codex-mini", 0.25, 2, 0.025},
		{"openai-subscription/gpt-5.2-codex", 1.75, 14, 0.175},
	}
	for _, tc := range cases {
		price, ok := PriceFor(tc.model, now)
		if !ok {
			t.Fatalf("%s has no curated price", tc.model)
		}
		if price.Rate(now) != "" {
			t.Errorf("%s is time-of-day priced", tc.model)
		}
		usage := TokenUsage{
			InputTokens: millionTokens, OutputTokens: millionTokens,
			CacheReadTokens: millionTokens,
		}
		if got, want := price.Cost(usage, now), tc.input+tc.out+tc.cache; !floatMatches(got, want) {
			t.Errorf("%s cost of 1M of each bucket = %v, want %v", tc.model, got, want)
		}
	}
}

// TestModelPriceCostScalesPerMillionTokens checks a price quoted per 1M tokens
// is charged in direct proportion to a non-round token count instead of being
// rounded to whole millions or cents.
func TestModelPriceCostScalesPerMillionTokens(t *testing.T) {
	peak := time.Date(2025, 9, 1, 2, 0, 0, 0, time.UTC)
	offPeak := time.Date(2025, 9, 1, 12, 0, 0, 0, time.UTC)
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
		{"deepseek/deepseek-unlisted", "", false},
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

// TestPriceForBedrockSelectors checks every spelling of one Bedrock model, from
// foundation-model ID to cross-region profile and ARN, resolves to its price,
// and a model without a published price stays unpriced.
func TestPriceForBedrockSelectors(t *testing.T) {
	now := time.Date(2025, 9, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		selector string
		in, out  float64
	}{
		{"bedrock/us.anthropic.claude-sonnet-4-6-v1:0", 3, 15},
		{"bedrock/global.anthropic.claude-sonnet-4-6-v1:0", 3, 15},
		{"bedrock/us.anthropic.claude-haiku-4-5-v1:0", 1, 5},
		{"bedrock/anthropic.claude-haiku-4-5-20251001-v1:0", 1, 5},
		{"bedrock/us.anthropic.claude-opus-4-8-v1:0", 5, 25},
		{"bedrock/eu.anthropic.claude-opus-4-1-20250805-v1:0", 15, 75},
		{"bedrock/anthropic.claude-3-5-sonnet-20241022-v2:0", 3, 15},
		{"bedrock/us.amazon.nova-pro-v1:0", 0.8, 3.2},
		{"Bedrock/arn:aws:bedrock:us-east-1:123456789012:inference-profile/us.anthropic.claude-sonnet-4-6-v1:0", 3, 15},
	}
	for _, tc := range cases {
		price, ok := PriceFor(tc.selector, now)
		if !ok {
			t.Errorf("%s has no price", tc.selector)
			continue
		}
		if price.Rate(now) != "" || !floatMatches(price.InputRate(now), tc.in) || !floatMatches(price.OutputRate(now), tc.out) {
			t.Errorf("%s priced %v in / %v out, want %v / %v", tc.selector, price.InputRate(now), price.OutputRate(now), tc.in, tc.out)
		}
	}
	for _, selector := range []string{
		"bedrock/",
		"bedrock/us.unknown.model-v1:0",
		"bedrock/arn:aws:bedrock:us-east-1:123456789012:application-inference-profile/abc123",
		"bedrock/us.anthropic.claude-sonnet-9-v1:0",
	} {
		if price, ok := PriceFor(selector, now); ok {
			t.Errorf("%s priced as %+v, want unpriced", selector, price)
		}
	}
}

func TestModelPriceCostBreakdownCoversCacheAndTiers(t *testing.T) {
	price := ModelPrice{
		InputPerMTok: 2, OutputPerMTok: 8,
		CacheReadPerMTok: 0.5, CacheWritePerMTok: 1,
	}
	usage := TokenUsage{
		InputTokens: millionTokens, OutputTokens: millionTokens,
		CacheReadTokens: millionTokens, CacheWriteTokens: millionTokens,
	}
	cost := price.CostBreakdown(usage, time.Now())
	if !floatMatches(cost.Input, 2) || !floatMatches(cost.Output, 8) ||
		!floatMatches(cost.CacheRead, 0.5) || !floatMatches(cost.CacheWrite, 1) ||
		!floatMatches(cost.Total, 11.5) {
		t.Fatalf("cost breakdown = %+v, want 2/8/0.5/1 and 11.5", cost)
	}

	// The request's whole prompt selects a tier, including cache buckets.
	price.Tiers = []PriceTier{{
		InputTokensAbove: 3*millionTokens - 1,
		InputPerMTok:     4, OutputPerMTok: 16,
		CacheReadPerMTok: 1, CacheWritePerMTok: 2,
	}}
	cost = price.CostBreakdown(usage, time.Now())
	if !floatMatches(cost.Input, 4) || !floatMatches(cost.Output, 16) ||
		!floatMatches(cost.CacheRead, 1) || !floatMatches(cost.CacheWrite, 2) ||
		!floatMatches(cost.Total, 23) {
		t.Fatalf("tiered cost breakdown = %+v, want the tier rates and 23", cost)
	}
	// A smaller request stays on the base rates.
	small := TokenUsage{InputTokens: millionTokens, OutputTokens: millionTokens}
	if cost := price.CostBreakdown(small, time.Now()); !floatMatches(cost.Total, 10) {
		t.Fatalf("small request cost = %v, want base 10", cost)
	}
}
