package orchestrator

import (
	"strings"
	"time"
)

// Rate labels describe which published price applied to a token count. They are
// recorded so a later reader can tell an estimate made at peak rates from one
// made during a discounted window, and the terminal can label the rate in force
// now. Only providers with time-of-day pricing use anything but the empty label.
const (
	RatePeak    = "peak"
	RateOffPeak = "off-peak"
	// RateMixed marks an attempt that was priced under both rates.
	RateMixed = "mixed"
)

// millionTokens is the unit the published prices use, so a price table entry
// reads like the provider's price list: US dollars per 1M tokens.
const millionTokens = 1_000_000

// ModelPrice is the operator-visible price of one model selector in US dollars
// per 1M tokens. OffPeakInputPerMTok and OffPeakOutputPerMTok are the
// discounted prices for a provider that publishes a daily off-peak window;
// TimeOfDay marks which models have one.
type ModelPrice struct {
	Model                string  `json:"model"`
	InputPerMTok         float64 `json:"input_per_mtok"`
	OutputPerMTok        float64 `json:"output_per_mtok"`
	OffPeakInputPerMTok  float64 `json:"off_peak_input_per_mtok,omitempty"`
	OffPeakOutputPerMTok float64 `json:"off_peak_output_per_mtok,omitempty"`
	TimeOfDay            bool    `json:"time_of_day,omitempty"`
}

// modelPrices is a curated snapshot of published prices in US dollars per 1M
// tokens. It is advisory bookkeeping for the operator, not billing: providers
// change prices, cached input is billed differently, and a model that is not
// listed (or a reseller such as OpenRouter, whose price depends on the upstream
// provider) reports tokens without a cost. DeepSeek discounts its off-peak
// window, so those models carry the discounted pair as well.
var modelPrices = map[string]ModelPrice{
	// OpenAI list prices.
	"openai/gpt-5":        {InputPerMTok: 1.25, OutputPerMTok: 10},
	"openai/gpt-5-mini":   {InputPerMTok: 0.25, OutputPerMTok: 2},
	"openai/gpt-4.1":      {InputPerMTok: 2, OutputPerMTok: 8},
	"openai/gpt-4.1-mini": {InputPerMTok: 0.4, OutputPerMTok: 1.6},

	// DeepSeek list prices plus its off-peak discount: deepseek-chat is half
	// price, deepseek-reasoner is 75% off input and half price on output.
	"deepseek/deepseek-chat":     {InputPerMTok: 0.28, OutputPerMTok: 0.42, OffPeakInputPerMTok: 0.14, OffPeakOutputPerMTok: 0.21, TimeOfDay: true},
	"deepseek/deepseek-reasoner": {InputPerMTok: 0.28, OutputPerMTok: 0.42, OffPeakInputPerMTok: 0.07, OffPeakOutputPerMTok: 0.21, TimeOfDay: true},
}

// PriceFor reports the curated price of a model selector and whether it is
// known. An unknown selector is reported as unpriced rather than guessed, so a
// session total never claims a cost it cannot support.
func PriceFor(model string, now time.Time) (ModelPrice, bool) {
	key := strings.ToLower(strings.TrimSpace(model))
	price, ok := modelPrices[key]
	if !ok {
		return ModelPrice{}, false
	}
	price.Model = key
	return price, true
}

// deepseekOffPeak reports whether t falls inside DeepSeek's daily off-peak
// discount window. DeepSeek publishes the window as 00:30–08:30 Beijing time
// (UTC+8), which is 16:30–00:30 UTC.
func deepseekOffPeak(t time.Time) bool {
	utc := t.UTC()
	minutes := utc.Hour()*60 + utc.Minute()
	return minutes >= 16*60+30 || minutes < 30
}

// Rate names the price in force at t: peak, off-peak, or empty for a model
// priced the same all day.
func (p ModelPrice) Rate(t time.Time) string {
	if !p.TimeOfDay {
		return ""
	}
	if deepseekOffPeak(t) {
		return RateOffPeak
	}
	return RatePeak
}

// InputRate is the price per 1M input tokens in force at t.
func (p ModelPrice) InputRate(t time.Time) float64 {
	if p.TimeOfDay && deepseekOffPeak(t) {
		return p.OffPeakInputPerMTok
	}
	return p.InputPerMTok
}

// OutputRate is the price per 1M output tokens in force at t.
func (p ModelPrice) OutputRate(t time.Time) float64 {
	if p.TimeOfDay && deepseekOffPeak(t) {
		return p.OffPeakOutputPerMTok
	}
	return p.OutputPerMTok
}

// Cost estimates the price of one token count at the rate in force at t.
func (p ModelPrice) Cost(usage TokenUsage, t time.Time) float64 {
	return float64(usage.InputTokens)/millionTokens*p.InputRate(t) +
		float64(usage.OutputTokens)/millionTokens*p.OutputRate(t)
}
