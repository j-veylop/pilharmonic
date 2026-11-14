package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"pilharmonic/internal/orchestrator"
)

func (m model) attentionCount() int { return len(m.attentionTasks()) }

func (m model) headerModeBadge() string {
	label, color := " TASKS ", accent
	switch {
	case m.router.open:
		label, color = " REQUEST ", accent
	case m.input.kind != "":
		label, color = " INSERT ", amber
	case m.detailFocus || m.fullscreen:
		label, color = " DETAILS ", cyan
	}
	return lipgloss.NewStyle().Foreground(color).Bold(true).Render(label)
}

// headerRow is the wordmark, the task counters and the session total, with the
// mode badge right-aligned. The badge is never truncated; the session total is
// dropped before a counter loses its wording.
func (m model) headerRow(width int) string {
	header := gradientText("◈ PILHARMONIC")
	mode := m.headerModeBadge()
	available := max(1, width-lipgloss.Width(header)-lipgloss.Width(mode)-1)
	options := m.headerCountOptions()
	summary, usage := options[len(options)-1], ""
fit:
	for _, option := range options {
		for _, variant := range m.headerUsageVariants() {
			if lipgloss.Width(option)+lipgloss.Width(variant) <= available {
				summary, usage = option, variant
				break fit
			}
		}
	}
	if usage != "" {
		usage = mutedStyle.Render(usage)
	}
	gap := max(1, width-lipgloss.Width(header+summary+usage)-lipgloss.Width(mode))
	return fit(header+summary+usage+strings.Repeat(" ", gap)+mode, width, 1)
}

// headerUsageVariants lists the session total, richest first and ending with
// an empty variant. An untouched session has no segment at all.
func (m model) headerUsageVariants() []string {
	if m.usage.TotalTokens == 0 {
		return []string{""}
	}
	variants := usageVariants(m.usage.InputTokens, m.usage.OutputTokens, m.usage.CostUSD, m.usage.Priced)
	for i, variant := range variants {
		if variant != "" {
			variants[i] = "  Σ " + variant
		}
	}
	return variants
}

// headerCountOptions lists the counter formats, richest first. Running is the
// displayed state, so a parent waiting for subagents is not counted; queued is
// the process status. The last form keeps only the attention count.
func (m model) headerCountOptions() []string {
	running, queued := 0, 0
	for _, task := range m.tasks {
		if task.Status == orchestrator.Queued {
			queued++
		}
		if _, label, _ := m.taskState(task); label == "RUNNING" {
			running++
		}
	}
	attention := m.attentionCount()
	actionText := "need action"
	if attention == 1 {
		actionText = "needs action"
	}
	tasks := fmt.Sprintf("  %d tasks", len(m.tasks))
	format := func(runningText, queuedText, attentionText string) string {
		return mutedStyle.Render(tasks) +
			lipgloss.NewStyle().Foreground(statusRunning).Render(runningText) +
			lipgloss.NewStyle().Foreground(statusWaiting).Render(queuedText) +
			lipgloss.NewStyle().Foreground(cyan).Render(attentionText)
	}
	return []string{
		format(fmt.Sprintf("  ● %d running", running), fmt.Sprintf("  ◷ %d queued", queued), fmt.Sprintf("  ◆ %d %s", attention, actionText)),
		format(fmt.Sprintf("  ● %d run", running), fmt.Sprintf("  ◷ %d wait", queued), fmt.Sprintf("  ◆ %d action", attention)),
		format(fmt.Sprintf("  ●%d", running), fmt.Sprintf("  ◷%d", queued), fmt.Sprintf("  ◆%d", attention)),
		format("", "", fmt.Sprintf("  ◆%d", attention)),
	}
}
