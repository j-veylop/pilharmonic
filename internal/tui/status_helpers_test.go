package tui

import (
	"github.com/charmbracelet/lipgloss"
	"pilharmonic/internal/orchestrator"
)

// These helpers project a task's state without its peer context, which only the
// tests need; production always uses the model methods so a running parent with
// active children reads as waiting for subagents.

func taskLabel(task orchestrator.Task) string {
	_, label, color := taskState(task)
	return lipgloss.NewStyle().Foreground(color).Bold(true).Render("● " + label)
}

func taskStatusIcon(task orchestrator.Task) string {
	glyph, _, color := taskState(task)
	return lipgloss.NewStyle().Foreground(color).Bold(true).Render(glyph)
}

func taskTitleRow(task orchestrator.Task, width int, selected bool) string {
	return taskTitleRowWithIcon(task, taskStatusIcon(task), width, selected)
}
