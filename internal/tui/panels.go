package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"pilharmonic/internal/orchestrator"
)

func panel(content string, width, height int) string {
	return panelWith(content, width, height, false)
}

func focusedPanel(content string, width, height int, focused bool) string {
	return panelWith(content, width, height, focused)
}

func panelWith(content string, width, height int, focused bool) string {
	if width < 4 || height < 2 {
		return fit(content, width, height)
	}
	style := panelStyle
	if focused {
		style = panelStyle.BorderForeground(accent)
	}
	return style.Width(width - 2).Height(height - 2).MaxWidth(width).MaxHeight(height).Render(fit(content, width-4, height-2))
}

// windowRows returns the visible slice bounds [start, end) for a list of n rows
// scrolled so the selected row stays inside slots visible rows.
func windowRows(selected, n, slots int) (start, end int) {
	slots = max(1, slots)
	if selected >= slots {
		start = min(selected-slots+1, max(0, n-slots))
	}
	return start, min(n, start+slots)
}

func modeBadge(label string, color lipgloss.Color) string {
	return lipgloss.NewStyle().
		Foreground(color).
		Bold(true).
		Render(label)
}

func selectionRow(text string, width int) string {
	if width < 1 {
		return ""
	}
	if width == 1 {
		return selectionRailStyle.Render("▌")
	}
	if width == 2 {
		return selectionRailStyle.Render("▌ ")
	}
	body := selectionBodyStyle.Width(width - 2).Render(fit(text, width-2, 1))
	return selectionRailStyle.Render("▌ ") + body
}

func (m model) taskPanel(width, height int) string {
	inner := m.layout().panelInner(width)
	var lines []string
	lines = append(lines, accentStyle.Render("TASKS")+mutedStyle.Render(fmt.Sprintf("  / %02d", len(m.tasks))))
	lines = append(lines, "")
	if len(m.tasks) == 0 {
		lines = append(lines,
			mutedStyle.Render("No tasks yet."),
			mutedStyle.Render("n creates one."))
	} else {
		rows := taskListRows(m.tasks)
		selected := 0
		for i, row := range rows {
			if row.index == m.selected {
				selected = i
				break
			}
		}
		start, end := windowRows(selected, len(rows), (height-4)/2)
		for i := start; i < end; i++ {
			row := rows[i]
			lines = append(lines,
				m.taskTitleRow(row.task, inner, row.index == m.selected),
				taskMetaLine(row.task, inner))
		}
	}
	return focusedPanel(strings.Join(lines, "\n"), width, height, !m.detailFocus && !m.fullscreen)
}

// sidebarVisible reports whether View renders the task sidebar. The selected
// task position is only needed when the sidebar is hidden.
func (m model) sidebarVisible() bool {
	return m.layout().sidebar
}

// selectedPosition returns the selected task's one-based position in grouped
// display order, which is the order the sidebar and navigation use.
func (m model) selectedPosition() (position, total int, ok bool) {
	rows := taskListRows(m.tasks)
	for i, row := range rows {
		if row.index == m.selected {
			return i + 1, len(rows), true
		}
	}
	return 0, 0, false
}

// executorLabel shows the executor and, when chosen, its profile.
func executorLabel(t orchestrator.Task) string {
	if t.ParentID != "" {
		return t.Agent + " · subagent"
	}
	if t.ProfileName == "" || (t.ProfileSpec != nil && t.ProfileSpec.UsesExecutorDefaults()) {
		return t.Agent
	}
	return t.Agent + " · profile " + t.ProfileName
}

func profileLabel(p orchestrator.Profile) string {
	if p.UsesExecutorDefaults() {
		return "Executor defaults"
	}
	return p.Name
}

func (m model) profilePanel(width, height int) string {
	lines := []string{titleStyle.Render("SELECT PROFILE"), mutedStyle.Render("Queued task only"), ""}
	start, end := windowRows(m.profilePicker.index, len(m.profilePicker.choices), (height-7)/2)
	for i := start; i < end; i++ {
		profile := m.profilePicker.choices[i]
		label := terminalText(profileLabel(profile))
		row := titleStyle.Render("  " + label)
		if i == m.profilePicker.index {
			row = selectionRow(label, width-4)
		}
		details := profile.Description
		if profile.Model != "" {
			details += " · model " + profile.Model
		}
		if profile.NoIntegrate {
			details += " · no auto-integration"
		}
		if profile.NoSubagents {
			details += " · no delegation"
		}
		if len(profile.Tools) > 0 {
			details += " · tools " + strings.Join(profile.Tools, ",")
		}
		lines = append(lines, row, mutedStyle.Render("  "+terminalText(strings.TrimPrefix(details, " · "))))
	}
	if len(m.profilePicker.choices) == 0 {
		lines = append(lines, mutedStyle.Render("No registered profiles."))
	}
	return panel(strings.Join(lines, "\n"), width, height)
}

func (m model) agentPanel(width, height int) string {
	lines := []string{titleStyle.Render("SELECT AGENT"), mutedStyle.Render("Queued task only"), ""}
	start, end := windowRows(m.agentPicker.index, len(m.agentPicker.choices), (height-7)/2)
	for i := start; i < end; i++ {
		agent := m.agentPicker.choices[i]
		capability := "One-shot · no live context"
		if agent.Capabilities.LiveContext {
			capability = "Live follow-up context"
		}
		if agent.Capabilities.ToolActivity {
			capability += " · tool activity"
		}
		if agent.Capabilities.StructuredReports {
			capability += " · structured reports"
		}
		label := terminalText(agent.Name)
		if agent.Description != "" {
			label += " · " + terminalText(agent.Description)
		}
		row := titleStyle.Render("  " + label)
		if i == m.agentPicker.index {
			row = selectionRow(label, width-4)
		}
		lines = append(lines, row, mutedStyle.Render("  "+capability))
	}
	if len(m.agentPicker.choices) == 0 {
		lines = append(lines, mutedStyle.Render("No registered agents."))
	}
	return panel(strings.Join(lines, "\n"), width, height)
}
