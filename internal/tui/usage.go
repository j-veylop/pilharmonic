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

// usageVariants lists a usage segment richest first, ending with "" so a
// caller can drop it. A cost appears only when every token was priced.
func usageVariants(input, output int64, cost float64, priced bool) []string {
	detailed := formatTokens(input) + " in · " + formatTokens(output) + " out"
	compact := formatTokens(input) + "/" + formatTokens(output)
	if !priced {
		return []string{detailed, compact, ""}
	}
	amount := formatCost(cost)
	return []string{detailed + " · " + amount, compact + " · " + amount, amount, ""}
}

func taskUsageLine(usage orchestrator.TaskUsage) string {
	if usage.Priced {
		return usageVariants(usage.InputTokens, usage.OutputTokens, usage.CostUSD, true)[0]
	}
	return usageVariants(usage.InputTokens, usage.OutputTokens, 0, false)[0] + " · cost unknown"
}

// priceLine describes the price in force now for the model a task last used,
// which is what matters when deciding whether to run more work.
func priceLine(selector string, now time.Time) (string, bool) {
	price, known := orchestrator.PriceFor(selector, now)
	if !known {
		return "", false
	}
	return "Price: " + formatPrice(price.InputRate(now)) + "/M in · " +
		formatPrice(price.OutputRate(now)) + "/M out", true
}

// attemptUsageSuffix is the richest usage variant that fits on an attempt's
// one-line history row, or nothing.
func attemptUsageSuffix(usage *orchestrator.AttemptUsage, available int) string {
	if usage == nil || usage.Usage().IsZero() {
		return ""
	}
	for _, variant := range usageVariants(usage.InputTokens, usage.OutputTokens, usage.CostUSD, usage.Priced) {
		if variant == "" {
			break
		}
		if suffix := " · " + variant; lipgloss.Width(suffix) <= available {
			return suffix
		}
	}
	return ""
}
