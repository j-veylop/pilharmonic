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
// discount window. DeepSeek publishes peak hours in UTC: 01:00–04:00 and
// 06:00–10:00, Monday through Friday, excluding Chinese public holidays. Every
// other hour, including weekends and holidays in full, is off-peak.
func deepseekOffPeak(t time.Time) bool {
	utc := t.UTC()
	if weekday := utc.Weekday(); weekday == time.Saturday || weekday == time.Sunday || chinesePublicHoliday(utc) {
		return true
	}
	minutes := utc.Hour()*60 + utc.Minute()
	return !(minutes >= 1*60 && minutes < 4*60 || minutes >= 6*60 && minutes < 10*60)
}

// chineseHolidayRanges lists the Chinese public holidays announced by the State
// Council, inclusively, as local calendar dates. They are held in UTC here
// because DeepSeek's peak windows (01:00–10:00 UTC) fall inside the same
// Beijing calendar day. Extend the table when a later year is published.
var chineseHolidayRanges = [][2]string{
	// 2025
	{"2025-01-01", "2025-01-01"},
	{"2025-01-28", "2025-02-04"},
	{"2025-04-04", "2025-04-06"},
	{"2025-05-01", "2025-05-05"},
	{"2025-05-31", "2025-06-02"},
	{"2025-10-01", "2025-10-08"},
	// 2026
	{"2026-01-01", "2026-01-03"},
	{"2026-02-15", "2026-02-23"},
	{"2026-04-04", "2026-04-06"},
	{"2026-05-01", "2026-05-05"},
	{"2026-06-19", "2026-06-21"},
	{"2026-09-25", "2026-09-27"},
	{"2026-10-01", "2026-10-07"},
	// 2027
	{"2027-01-01", "2027-01-01"},
	{"2027-02-05", "2027-02-13"},
	{"2027-04-05", "2027-04-05"},
	{"2027-05-01", "2027-05-05"},
	{"2027-06-09", "2027-06-09"},
	{"2027-09-15", "2027-09-15"},
	{"2027-10-01", "2027-10-07"},
}

// chinesePublicHoliday reports whether t's date is a Chinese public holiday.
func chinesePublicHoliday(t time.Time) bool {
	day := t.UTC().Format("2006-01-02")
	for _, span := range chineseHolidayRanges {
		if day >= span[0] && day <= span[1] {
			return true
		}
	}
	return false
}

// providerWindows maps a provider prefix to the window function for a daily
// discount it publishes. The window belongs to the provider, so it can be
// named for any model under that prefix even when the curated price table does
// not list the individual model.
var providerWindows = map[string]func(time.Time) bool{
	"deepseek": deepseekOffPeak,
}

// ProviderRate names the time-of-day price in force at t for a provider that
// publishes a daily discount window, or "" for one priced the same all day.
func ProviderRate(provider string, t time.Time) string {
	window, ok := providerWindows[strings.ToLower(strings.TrimSpace(provider))]
	if !ok {
		return ""
	}
	if window(t) {
		return RateOffPeak
	}
	return RatePeak
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
