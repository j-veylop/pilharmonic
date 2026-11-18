package tui

import (
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"pilharmonic/internal/orchestrator"
)

// Token and cost formats are shared by the header, the task summary and the
// attempt rows so they never spell the same total differently.

// formatTokens writes a token count compactly: 950, 12.3k, 12k, 1.2M.
func formatTokens(tokens int64) string {
	if tokens < 0 {
		tokens = 0
	}
	if tokens < 1_000 {
		return strconv.FormatInt(tokens, 10)
	}
	if tokens < 999_950 {
		return trimZero(strconv.FormatFloat(float64(tokens)/1_000, 'f', 1, 64)) + "k"
	}
	return trimZero(strconv.FormatFloat(float64(tokens)/1_000_000, 'f', 1, 64)) + "M"
}

func trimZero(s string) string { return strings.TrimSuffix(s, ".0") }

// formatCost writes a dollar estimate, with four decimals below a cent and an
// upper bound rather than $0 for a cost too small to show.
func formatCost(usd float64) string {
	if usd < 0.01 {
		if usd < 0.00005 {
			return "<$0.0001"
		}
		return "$" + strconv.FormatFloat(usd, 'f', 4, 64)
	}
	return "$" + strconv.FormatFloat(usd, 'f', 2, 64)
}

// formatPrice writes a per-1M-token price like a price list: $2, $0.14, $1.25.
func formatPrice(usd float64) string {
	trimmed := strings.TrimRight(strconv.FormatFloat(usd, 'f', 2, 64), "0")
	return "$" + strings.TrimSuffix(trimmed, ".")
}

// formatCacheHit writes a cache-read share as a whole percentage.
func formatCacheHit(rate float64) string {
	return strconv.FormatFloat(rate*100, 'f', 0, 64) + "%"
}

// usageVariants lists a usage segment richest first, ending with "" so a
// caller can drop it. Prompt input is the whole request, including cache reads
// and writes; the detailed form names those buckets, and the compact form keeps
// the cached amount when there was one. A cost appears only when every token
// was priced.
func usageVariants(tokens orchestrator.TokenUsage, cost float64, priced bool) []string {
	prompt := tokens.PromptTokens()
	detailed := formatTokens(prompt) + " in"
	if tokens.CacheReadTokens > 0 {
		detailed += " · " + formatTokens(tokens.CacheReadTokens) + " cached"
		if prompt > 0 {
			detailed += " (" + formatCacheHit(float64(tokens.CacheReadTokens)/float64(prompt)) + ")"
		}
	}
	if tokens.CacheWriteTokens > 0 {
		detailed += " · " + formatTokens(tokens.CacheWriteTokens) + " written"
	}
	detailed += " · " + formatTokens(tokens.OutputTokens) + " out"
	compact := formatTokens(prompt) + "/" + formatTokens(tokens.OutputTokens)
	if tokens.CacheReadTokens > 0 {
		compact += " · " + formatTokens(tokens.CacheReadTokens) + " cached"
	}
	if !priced {
		return []string{detailed, compact, ""}
	}
	amount := formatCost(cost)
	return []string{detailed + " · " + amount, compact + " · " + amount, amount, ""}
}

func taskUsageLine(usage orchestrator.TaskUsage) string {
	if usage.Priced {
		return usageVariants(usage.TokenUsage, usage.CostUSD, true)[0]
	}
	return usageVariants(usage.TokenUsage, 0, false)[0] + " · cost unknown"
}

// taskListUsage is the richest task usage segment that fits in available
// cells, or "" when the task recorded nothing. The list row drops the detail
// before it wraps or crowds out the rest of the row.
func taskListUsage(usage orchestrator.TaskUsage, available int) string {
	if usage.IsZero() || available <= 0 {
		return ""
	}
	for _, variant := range usageVariants(usage.TokenUsage, usage.CostUSD, usage.Priced) {
		if variant == "" {
			break
		}
		if lipgloss.Width(variant) <= available {
			return variant
		}
	}
	return ""
}

// priceLine describes the price in force now for the model a task last used,
// which is what matters when deciding whether to run more work.
func priceLine(selector string, now time.Time) (string, bool) {
	price, known := orchestrator.PriceFor(selector, now)
	if !known {
		return "", false
	}
	line := "Price: " + formatPrice(price.InputRate(now)) + "/M in"
	if rate := price.CacheReadRate(now); rate > 0 {
		line += " · " + formatPrice(rate) + "/M cached"
	}
	return line + " · " + formatPrice(price.OutputRate(now)) + "/M out", true
}

// attemptUsageSuffix is the richest usage variant that fits on an attempt's
// one-line history row, or nothing.
func attemptUsageSuffix(usage *orchestrator.AttemptUsage, available int) string {
	if usage == nil || usage.Usage().IsZero() {
		return ""
	}
	for _, variant := range usageVariants(usage.Usage(), usage.CostUSD, usage.Priced) {
		if variant == "" {
			break
		}
		if suffix := " · " + variant; lipgloss.Width(suffix) <= available {
			return suffix
		}
	}
	return ""
}
