package tui

import (
	"fmt"

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

// headerSummary chooses the most descriptive counter format that fits beside
// the wordmark and mode badge, so narrow terminals are not truncated with an
// ellipsis. Running is the displayed state; queued is the process status.
func (m model) headerSummary(available int) string {
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
	options := []string{
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
	for _, option := range options {
		if lipgloss.Width(option) <= available {
			return option
		}
	}
	return options[len(options)-1]
}
