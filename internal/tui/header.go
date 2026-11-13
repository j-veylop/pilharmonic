package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"pilharmonic/internal/orchestrator"
)

// attentionCount counts tasks whose derived stage needs an operator decision.
func (m model) attentionCount() int {
	count := 0
	for _, task := range m.tasks {
		if needsAttention(task) {
			count++
		}
	}
	return count
}

// headerCounts derives running from the displayed state so a parent waiting for
// subagents is not reported as running, while queued remains the process status
// (scheduled tasks are still queued).
func (m model) headerCounts() (running, queued int) {
	for _, task := range m.tasks {
		if task.Status == orchestrator.Queued {
			queued++
		}
		if _, label, _ := m.taskState(task); label == "RUNNING" {
			running++
		}
	}
	return running, queued
}

// headerModeBadge is the board's right-aligned mode badge. It comes last on the
// top row, so the counters and the session total give up room before it does.
func (m model) headerModeBadge() string {
	label, color := " TASKS ", accent
	if m.detailFocus || m.fullscreen {
		label, color = " DETAILS ", cyan
	}
	if m.input.kind != "" {
		label, color = " INSERT ", amber
	}
	if m.router.open {
		label, color = " REQUEST ", accent
	}
	return modeBadge(label, color)
}

// headerRow composes the board's top line: the wordmark on the left, then the
// task counters, then the session total, with the mode badge right-aligned. The
// usage segment only has the gap that used to separate the counters from the
// badge, so it disappears first as the terminal narrows.
func (m model) headerRow(width int) string {
	header := gradientText("◈ PILHARMONIC")
	mode := m.headerModeBadge()
	// Always keep one cell of breathing room before the badge: the badge is
	// never truncated, whatever has to be dropped to hold it.
	available := max(1, width-lipgloss.Width(header)-lipgloss.Width(mode)-1)
	summary, usage := m.headerSummaryAndUsage(available)
	if usage != "" {
		usage = mutedStyle.Render(usage)
	}
	gap := max(1, width-lipgloss.Width(header+summary+usage)-lipgloss.Width(mode))
	return fit(header+summary+usage+strings.Repeat(" ", gap)+mode, width, 1)
}

// headerSummaryAndUsage picks the counters and the session total that fit in
// available cells together. Counters are tried richest first and each is
// combined with the richest usage segment that fits, so the session total is
// dropped before a counter loses its wording: naming what needs action matters
// more than a total the operator can read in the detail panel.
func (m model) headerSummaryAndUsage(available int) (summary, usage string) {
	options := m.headerCountOptions()
	variants := m.headerUsageVariants()
	for _, option := range options {
		for _, variant := range variants {
			if lipgloss.Width(option)+lipgloss.Width(variant) <= available {
				return option, variant
			}
		}
	}
	// Nothing fits, so fall back to the counters on their own with the same
	// progressive dropping the header had before the total was added.
	return m.headerSummary(available), ""
}

// headerUsageVariants lists the session-usage segment, richest first and ending
// with an empty variant. A session that has recorded no tokens has no segment at
// all, so an untouched board renders exactly as it did before.
func (m model) headerUsageVariants() []string {
	usage := m.usage
	if usage.TotalTokens == 0 {
		return []string{""}
	}
	variants := usageVariants(usage.InputTokens, usage.OutputTokens, usage.CostUSD, usage.Priced)
	for i, variant := range variants {
		if variant != "" {
			variants[i] = "  Σ " + variant
		}
	}
	return variants
}

// headerSummary chooses the most descriptive counter format that fits in
// available cells, so narrow terminals are not truncated with an ellipsis.
// Running is the displayed state; queued is the process status.
func (m model) headerSummary(available int) string {
	options := m.headerCountOptions()
	for _, option := range options {
		if lipgloss.Width(option) <= available {
			return option
		}
	}
	return options[len(options)-1]
}

// headerCountOptions lists the counter formats, richest first. The last form
// keeps the attention count alone, because a task waiting for a decision is the
// one thing the header must always show.
func (m model) headerCountOptions() []string {
	running, queued := m.headerCounts()
	attention := m.attentionCount()
	actionText := "need action"
	if attention == 1 {
		actionText = "needs action"
	}
	tasks := len(m.tasks)
	format := func(taskText, runningText, queuedText, attentionText string) string {
		return mutedStyle.Render(taskText) +
			lipgloss.NewStyle().Foreground(statusRunning).Render(runningText) +
			lipgloss.NewStyle().Foreground(statusWaiting).Render(queuedText) +
			lipgloss.NewStyle().Foreground(cyan).Render(attentionText)
	}
	return []string{
		format(fmt.Sprintf("  %d tasks", tasks),
			fmt.Sprintf("  ● %d running", running),
			fmt.Sprintf("  ◷ %d queued", queued),
			fmt.Sprintf("  ◆ %d %s", attention, actionText)),
		format(fmt.Sprintf("  %d tasks", tasks),
			fmt.Sprintf("  ● %d run", running),
			fmt.Sprintf("  ◷ %d wait", queued),
			fmt.Sprintf("  ◆ %d action", attention)),
		format(fmt.Sprintf("  %d tasks", tasks),
			fmt.Sprintf("  ●%d", running),
			fmt.Sprintf("  ◷%d", queued),
			fmt.Sprintf("  ◆%d", attention)),
		format(fmt.Sprintf("  %d tasks", tasks), "", "", fmt.Sprintf("  ◆%d", attention)),
	}
}
