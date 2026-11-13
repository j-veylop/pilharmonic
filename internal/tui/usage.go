package tui

import (
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"pilharmonic/internal/orchestrator"
)

// The board and the detail panel print token counts and dollar estimates. Both
// are long numbers the operator reads at a glance rather than adds up, so one
// spelling per magnitude is chosen here: the header, the task summary and an
// attempt row never disagree about the same total.

// formatTokens writes a token count compactly: 950, 12.3k, 1.2M. The single
// decimal is kept because it separates 12.3k from 12k, and dropped when it is a
// zero so a round count reads as 12k.
func formatTokens(tokens int64) string {
	if tokens < 0 {
		tokens = 0
	}
	if tokens < 1_000 {
		return strconv.FormatInt(tokens, 10)
	}
	// A count this close to a million would round to "1000k", so that band uses
	// the next unit instead of printing four digits.
	if tokens < 999_950 {
		return trimZero(strconv.FormatFloat(float64(tokens)/1_000, 'f', 1, 64)) + "k"
	}
	return trimZero(strconv.FormatFloat(float64(tokens)/1_000_000, 'f', 1, 64)) + "M"
}

// trimZero removes the decimal point of a one-decimal figure that has no
// fractional part, so 12.0 becomes 12.
func trimZero(s string) string { return strings.TrimSuffix(s, ".0") }

// formatCost writes a dollar estimate. Below one cent four decimals are needed
// to stay meaningful, and a positive cost that would round to zero is shown as
// an upper bound rather than as a reassuring $0.
func formatCost(usd float64) string {
	if usd < 0.01 {
		if usd < 0.00005 {
			return "<$0.0001"
		}
		return "$" + strconv.FormatFloat(usd, 'f', 4, 64)
	}
	return "$" + strconv.FormatFloat(usd, 'f', 2, 64)
}

// formatPrice writes a per-1M-token price the way a provider's price list does:
// at most two decimals with trailing zeros trimmed, so $2, $0.14 and $1.25 all
// read naturally.
func formatPrice(usd float64) string {
	trimmed := strings.TrimRight(strconv.FormatFloat(usd, 'f', 2, 64), "0")
	return "$" + strings.TrimSuffix(trimmed, ".")
}

// usageVariants writes one token count and its estimate, richest first and
// ending with an empty variant so a caller can drop the segment when the space
// it has will not hold even the shortest form. A cost appears only when it was
// priced: the terminal never prints a dollar figure it cannot support.
func usageVariants(input, output int64, cost float64, priced bool) []string {
	detailed := formatTokens(input) + " in · " + formatTokens(output) + " out"
	compact := formatTokens(input) + "/" + formatTokens(output)
	if !priced {
		return []string{detailed, compact, ""}
	}
	amount := formatCost(cost)
	return []string{detailed + " · " + amount, compact + " · " + amount, amount, ""}
}

// taskUsageLine is the detail panel's usage line for one task: the tokens it
// recorded and, only when every counted token had a price, the estimate.
func taskUsageLine(usage orchestrator.TaskUsage) string {
	line := formatTokens(usage.InputTokens) + " in · " + formatTokens(usage.OutputTokens) + " out"
	if usage.Priced {
		return line + " · " + formatCost(usage.CostUSD)
	}
	return line + " · cost unknown"
}

// priceLine describes the price in force now for the model a task last used.
// The recorded rate only says what an attempt was charged, so the current rate
// is what the operator needs when deciding whether to run more work.
func priceLine(selector, rate string, now time.Time) (string, bool) {
	price, known := orchestrator.PriceFor(selector, now)
	if !known {
		return "", false
	}
	line := "Price: " + formatPrice(price.InputRate(now)) + "/M in · " +
		formatPrice(price.OutputRate(now)) + "/M out"
	if !price.TimeOfDay {
		return line, true
	}
	label := price.Rate(now)
	if rate == orchestrator.RateMixed {
		// One attempt straddled the window, so naming a single rate would be
		// wrong about the work that was already charged.
		label = orchestrator.RatePeak + "/" + orchestrator.RateOffPeak
	}
	return line + " · " + providerName(selector) + " " + label, true
}

// providerName is the selector prefix a price belongs to, so a time-of-day
// label names the provider whose window it describes.
func providerName(selector string) string {
	if at := strings.Index(selector, "/"); at > 0 {
		return selector[:at]
	}
	return selector
}

// attemptUsageSuffix appends an attempt's own usage to its history row. The row
// is one line per run, so the suffix is shortened rather than wrapped: the token
// counts collapse into one figure, then give way to the cost alone, and a row
// with no room at all keeps just its time, status and ID.
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
