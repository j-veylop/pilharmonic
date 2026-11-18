package orchestrator

import (
	"regexp"
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
// per 1M tokens. CacheReadPerMTok prices prompt tokens served from the provider's
// prompt cache, and CacheWritePerMTok prices tokens written to it. The OffPeak
// pairs are the discounted prices for a provider that publishes a daily off-peak
// window; TimeOfDay marks which models have one.
type ModelPrice struct {
	Model                    string  `json:"model"`
	InputPerMTok             float64 `json:"input_per_mtok"`
	OutputPerMTok            float64 `json:"output_per_mtok"`
	CacheReadPerMTok         float64 `json:"cache_read_per_mtok,omitempty"`
	CacheWritePerMTok        float64 `json:"cache_write_per_mtok,omitempty"`
	OffPeakInputPerMTok      float64 `json:"off_peak_input_per_mtok,omitempty"`
	OffPeakOutputPerMTok     float64 `json:"off_peak_output_per_mtok,omitempty"`
	OffPeakCacheReadPerMTok  float64 `json:"off_peak_cache_read_per_mtok,omitempty"`
	OffPeakCacheWritePerMTok float64 `json:"off_peak_cache_write_per_mtok,omitempty"`
	TimeOfDay                bool    `json:"time_of_day,omitempty"`
	// Tiers are request-wide price changes. The highest InputTokensAbove below
	// the request's prompt tokens prices the whole request. An empty list uses
	// the base rates above.
	Tiers []PriceTier `json:"tiers,omitempty"`
}

// PriceTier is one request-wide price tier. InputTokensAbove is the exclusive
// threshold; a request with more prompt tokens than the threshold may use the
// tier. The OffPeak fields apply only to a time-of-day model.
type PriceTier struct {
	InputTokensAbove         int64   `json:"input_tokens_above"`
	InputPerMTok             float64 `json:"input_per_mtok"`
	OutputPerMTok            float64 `json:"output_per_mtok"`
	CacheReadPerMTok         float64 `json:"cache_read_per_mtok"`
	CacheWritePerMTok        float64 `json:"cache_write_per_mtok"`
	OffPeakInputPerMTok      float64 `json:"off_peak_input_per_mtok,omitempty"`
	OffPeakOutputPerMTok     float64 `json:"off_peak_output_per_mtok,omitempty"`
	OffPeakCacheReadPerMTok  float64 `json:"off_peak_cache_read_per_mtok,omitempty"`
	OffPeakCacheWritePerMTok float64 `json:"off_peak_cache_write_per_mtok,omitempty"`
}

// UsageCost is the estimated cost of one provider call, split by token bucket.
// Total is the sum of the four components.
type UsageCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
	Total      float64 `json:"total"`
}

// modelPrices is a curated snapshot of published prices in US dollars per 1M
// tokens. It is advisory bookkeeping for the operator, not billing: providers
// change prices, and a model that is not listed (or a reseller such as
// OpenRouter, whose price depends on the upstream provider) reports tokens
// without a cost. DeepSeek discounts its off-peak window, so those models carry
// the discounted input, output and cache-read rates as well.
var modelPrices = map[string]ModelPrice{
	// OpenAI list prices. Cached input is billed at the published cache-read
	// rate; OpenAI does not charge a separate cache-write rate.
	"openai/gpt-5":        {InputPerMTok: 1.25, OutputPerMTok: 10, CacheReadPerMTok: 0.125},
	"openai/gpt-5-mini":   {InputPerMTok: 0.25, OutputPerMTok: 2, CacheReadPerMTok: 0.025},
	"openai/gpt-4.1":      {InputPerMTok: 2, OutputPerMTok: 8, CacheReadPerMTok: 0.5},
	"openai/gpt-4.1-mini": {InputPerMTok: 0.4, OutputPerMTok: 1.6, CacheReadPerMTok: 0.1},

	// ChatGPT subscription usage is not billed per token, but the API list price
	// of the same model is a useful estimate of what the work would have cost.
	// The published Codex rates are used for the catalog's subscription models.
	"openai-subscription/gpt-5-codex":       {InputPerMTok: 1.25, OutputPerMTok: 10, CacheReadPerMTok: 0.125},
	"openai-subscription/gpt-5.1-codex":     {InputPerMTok: 1.25, OutputPerMTok: 10, CacheReadPerMTok: 0.125},
	"openai-subscription/gpt-5.1-codex-max": {InputPerMTok: 1.25, OutputPerMTok: 10, CacheReadPerMTok: 0.125},
	"openai-subscription/gpt-5.1-codex-mini": {
		InputPerMTok: 0.25, OutputPerMTok: 2, CacheReadPerMTok: 0.025,
	},
	"openai-subscription/gpt-5.2-codex": {InputPerMTok: 1.75, OutputPerMTok: 14, CacheReadPerMTok: 0.175},

	// DeepSeek list prices plus its off-peak discount, which is half the peak
	// rate for every current model. deepseek-flash and deepseek-v4-pro are the
	// models DeepSeek's API currently serves; deepseek-chat and deepseek-reasoner
	// are kept for selectors pinned before the rename. A cache hit is billed at
	// the published cache-read rate; cache misses are already in the uncached
	// input bucket.
	"deepseek/deepseek-flash": {
		InputPerMTok: 0.30, OutputPerMTok: 1.20, CacheReadPerMTok: 0.006,
		OffPeakInputPerMTok: 0.15, OffPeakOutputPerMTok: 0.60, OffPeakCacheReadPerMTok: 0.003,
		TimeOfDay: true,
	},
	"deepseek/deepseek-v4-pro": {
		InputPerMTok: 1.32, OutputPerMTok: 3.96, CacheReadPerMTok: 0.044,
		OffPeakInputPerMTok: 0.66, OffPeakOutputPerMTok: 1.98, OffPeakCacheReadPerMTok: 0.022,
		TimeOfDay: true,
	},
	"deepseek/deepseek-chat": {
		InputPerMTok: 0.28, OutputPerMTok: 0.42, CacheReadPerMTok: 0.028,
		OffPeakInputPerMTok: 0.14, OffPeakOutputPerMTok: 0.21, OffPeakCacheReadPerMTok: 0.014,
		TimeOfDay: true,
	},
	"deepseek/deepseek-reasoner": {
		InputPerMTok: 0.28, OutputPerMTok: 0.42, CacheReadPerMTok: 0.028,
		OffPeakInputPerMTok: 0.07, OffPeakOutputPerMTok: 0.21, OffPeakCacheReadPerMTok: 0.014,
		TimeOfDay: true,
	},
}

// bedrockPrices holds AWS Bedrock on-demand prices in US dollars per 1M tokens,
// keyed by the model family with its geography prefix, date stamp and version
// suffix removed (see bedrockModelKey). Bedrock bills Anthropic models at
// Anthropic's list price, and cache writes at the 5-minute rate. Amazon Nova
// cache reads are billed at a quarter of the input price. The table ignores
// the premium some regional endpoints charge over global ones.
var bedrockPrices = map[string]ModelPrice{
	"anthropic.claude-opus-4-8":   {InputPerMTok: 5, OutputPerMTok: 25, CacheReadPerMTok: 0.5, CacheWritePerMTok: 6.25},
	"anthropic.claude-opus-4-6":   {InputPerMTok: 5, OutputPerMTok: 25, CacheReadPerMTok: 0.5, CacheWritePerMTok: 6.25},
	"anthropic.claude-opus-4-5":   {InputPerMTok: 5, OutputPerMTok: 25, CacheReadPerMTok: 0.5, CacheWritePerMTok: 6.25},
	"anthropic.claude-opus-4-1":   {InputPerMTok: 15, OutputPerMTok: 75, CacheReadPerMTok: 1.5, CacheWritePerMTok: 18.75},
	"anthropic.claude-opus-4":     {InputPerMTok: 15, OutputPerMTok: 75, CacheReadPerMTok: 1.5, CacheWritePerMTok: 18.75},
	"anthropic.claude-sonnet-4-6": {InputPerMTok: 3, OutputPerMTok: 15, CacheReadPerMTok: 0.3, CacheWritePerMTok: 3.75},
	"anthropic.claude-sonnet-4-5": {InputPerMTok: 3, OutputPerMTok: 15, CacheReadPerMTok: 0.3, CacheWritePerMTok: 3.75},
	"anthropic.claude-sonnet-4":   {InputPerMTok: 3, OutputPerMTok: 15, CacheReadPerMTok: 0.3, CacheWritePerMTok: 3.75},
	"anthropic.claude-3-7-sonnet": {InputPerMTok: 3, OutputPerMTok: 15, CacheReadPerMTok: 0.3, CacheWritePerMTok: 3.75},
	"anthropic.claude-3-5-sonnet": {InputPerMTok: 3, OutputPerMTok: 15, CacheReadPerMTok: 0.3, CacheWritePerMTok: 3.75},
	"anthropic.claude-haiku-4-5":  {InputPerMTok: 1, OutputPerMTok: 5, CacheReadPerMTok: 0.1, CacheWritePerMTok: 1.25},
	"anthropic.claude-3-5-haiku":  {InputPerMTok: 0.8, OutputPerMTok: 4, CacheReadPerMTok: 0.08, CacheWritePerMTok: 1},
	"anthropic.claude-3-haiku":    {InputPerMTok: 0.25, OutputPerMTok: 1.25},
	"amazon.nova-micro":           {InputPerMTok: 0.035, OutputPerMTok: 0.14, CacheReadPerMTok: 0.00875},
	"amazon.nova-lite":            {InputPerMTok: 0.06, OutputPerMTok: 0.24, CacheReadPerMTok: 0.015},
	"amazon.nova-pro":             {InputPerMTok: 0.8, OutputPerMTok: 3.2, CacheReadPerMTok: 0.2},
	"meta.llama3-3-70b-instruct":  {InputPerMTok: 0.72, OutputPerMTok: 0.72},
}

var (
	// bedrockGeoPrefix is a cross-region inference profile's geography prefix.
	bedrockGeoPrefix = regexp.MustCompile(`^(?:global|us|eu|apac|jp|au|ca|us-gov)\.`)
	// bedrockVersionSuffix is the trailing model version, such as "-v1:0".
	bedrockVersionSuffix = regexp.MustCompile(`-v\d+(?::\d+)?(?::\d+k)?$`)
	// bedrockDateStamp is the release date Anthropic models carry, such as
	// "-20250929".
	bedrockDateStamp = regexp.MustCompile(`-\d{8}$`)
)

// bedrockModelKey reduces a lowercased "bedrock/..." selector to the model
// family key of bedrockPrices. A foundation-model ID, a cross-region inference
// profile ID and an inference-profile ARN of the same model share one key.
func bedrockModelKey(selector string) (string, bool) {
	id, ok := strings.CutPrefix(selector, "bedrock/")
	if !ok {
		return "", false
	}
	if strings.HasPrefix(id, "arn:") {
		// The model or profile ID follows the last slash of the resource.
		id = id[strings.LastIndex(id, "/")+1:]
	}
	id = bedrockGeoPrefix.ReplaceAllString(id, "")
	id = bedrockVersionSuffix.ReplaceAllString(id, "")
	id = bedrockDateStamp.ReplaceAllString(id, "")
	return id, id != ""
}

// bedrockPriceFor prices a lowercased Bedrock selector. An application
// inference profile ARN names no model, so it stays unpriced.
func bedrockPriceFor(selector string) (ModelPrice, bool) {
	id, ok := bedrockModelKey(selector)
	if !ok {
		return ModelPrice{}, false
	}
	price, ok := bedrockPrices[id]
	return price, ok
}

// PriceFor reports the curated price of a model selector and whether it is
// known. An unknown selector is reported as unpriced rather than guessed, so a
// session total never claims a cost it cannot support.
func PriceFor(model string, now time.Time) (ModelPrice, bool) {
	key := strings.ToLower(strings.TrimSpace(model))
	price, ok := modelPrices[key]
	if !ok {
		price, ok = bedrockPriceFor(key)
	}
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

// priceRates is one effective per-million rate table after tier selection and
// any time-of-day discount.
type priceRates struct {
	input, output, cacheRead, cacheWrite float64
}

// ratesFor resolves the rates that price one request. Tier selection uses the
// whole request input, including cache reads and writes, and then the
// time-of-day window applies to the selected tier.
func (p ModelPrice) ratesFor(promptTokens int64, t time.Time) priceRates {
	offPeak := p.TimeOfDay && deepseekOffPeak(t)
	rates := priceRates{
		input: p.InputPerMTok, output: p.OutputPerMTok,
		cacheRead: p.CacheReadPerMTok, cacheWrite: p.CacheWritePerMTok,
	}
	if offPeak {
		rates = priceRates{
			input: p.OffPeakInputPerMTok, output: p.OffPeakOutputPerMTok,
			cacheRead: p.OffPeakCacheReadPerMTok, cacheWrite: p.OffPeakCacheWritePerMTok,
		}
	}
	matched := int64(-1)
	for _, tier := range p.Tiers {
		if promptTokens > tier.InputTokensAbove && tier.InputTokensAbove > matched {
			matched = tier.InputTokensAbove
			rates = priceRates{
				input: tier.InputPerMTok, output: tier.OutputPerMTok,
				cacheRead: tier.CacheReadPerMTok, cacheWrite: tier.CacheWritePerMTok,
			}
			if offPeak {
				rates = priceRates{
					input: tier.OffPeakInputPerMTok, output: tier.OffPeakOutputPerMTok,
					cacheRead: tier.OffPeakCacheReadPerMTok, cacheWrite: tier.OffPeakCacheWritePerMTok,
				}
			}
		}
	}
	return rates
}

// InputRate is the base price per 1M uncached input tokens in force at t.
func (p ModelPrice) InputRate(t time.Time) float64 { return p.ratesFor(0, t).input }

// OutputRate is the base price per 1M output tokens in force at t.
func (p ModelPrice) OutputRate(t time.Time) float64 { return p.ratesFor(0, t).output }

// CacheReadRate is the base price per 1M prompt-cache read tokens in force at t.
func (p ModelPrice) CacheReadRate(t time.Time) float64 { return p.ratesFor(0, t).cacheRead }

// CacheWriteRate is the base price per 1M prompt-cache write tokens in force at t.
func (p ModelPrice) CacheWriteRate(t time.Time) float64 { return p.ratesFor(0, t).cacheWrite }

// Cost estimates the total price of one token count at the rate in force at t.
func (p ModelPrice) Cost(usage TokenUsage, t time.Time) float64 {
	return p.CostBreakdown(usage, t).Total
}

// CostBreakdown estimates each token bucket of one provider call. The request's
// prompt size selects any request-wide tier; time-of-day pricing then applies.
func (p ModelPrice) CostBreakdown(usage TokenUsage, t time.Time) UsageCost {
	rates := p.ratesFor(usage.PromptTokens(), t)
	cost := UsageCost{
		Input:      float64(usage.InputTokens) / millionTokens * rates.input,
		Output:     float64(usage.OutputTokens) / millionTokens * rates.output,
		CacheRead:  float64(usage.CacheReadTokens) / millionTokens * rates.cacheRead,
		CacheWrite: float64(usage.CacheWriteTokens) / millionTokens * rates.cacheWrite,
	}
	cost.Total = cost.Input + cost.Output + cost.CacheRead + cost.CacheWrite
	return cost
}
