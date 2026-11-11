package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"pilharmonic/internal/orchestrator"
)

func panel(content string, width, height int) string {
	if width < 4 || height < 2 {
		return fit(content, width, height)
	}
	return panelStyle.Width(width - 2).Height(height - 2).MaxWidth(width).MaxHeight(height).Render(fit(content, width-4, height-2))
}

func focusedPanel(content string, width, height int, focused bool) string {
	rendered := panel(content, width, height)
	if !focused || width < 4 || height < 2 {
		return rendered
	}
	return panelStyle.BorderForeground(accent).Width(width - 2).Height(height - 2).MaxWidth(width).MaxHeight(height).Render(fit(content, width-4, height-2))
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
	inner := width - 4
	var lines []string
	lines = append(lines, accentStyle.Render("TASKS")+mutedStyle.Render(fmt.Sprintf("  / %02d", len(m.tasks))))
	lines = append(lines, "")
	if len(m.tasks) == 0 {
		lines = append(lines,
			mutedStyle.Render("No tasks yet."),
			mutedStyle.Render("n creates one."))
	} else {
		slots := (height - 4) / 2
		if slots < 1 {
			slots = 1
		}
		rows := taskListRows(m.tasks)
		selected := 0
		for i, row := range rows {
			if row.index == m.selected {
				selected = i
				break
			}
		}
		start := 0
		if selected >= slots {
			start = selected - slots + 1
		}
		for i := start; i < len(rows) && i < start+slots; i++ {
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
	w := m.width
	if w <= 0 {
		w = 80
	}
	return w >= 76 && !m.fullscreen
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

// detailGeometry matches the responsive layout used by View.
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
	slots := max(1, (height-7)/2)
	start := max(0, m.profilePicker.index-slots+1)
	for i := start; i < len(m.profilePicker.choices) && i < start+slots; i++ {
		profile := m.profilePicker.choices[i]
		prefix := "  "
		style := titleStyle
		if i == m.profilePicker.index {
			prefix = ""
			style = accentStyle
		}
		row := style.Render(prefix + terminalText(profileLabel(profile)))
		if i == m.profilePicker.index {
			row = selectionRow(terminalText(profileLabel(profile)), width-4)
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
	slots := max(1, (height-7)/2)
	start := max(0, m.agentPicker.index-slots+1)
	for i := start; i < len(m.agentPicker.choices) && i < start+slots; i++ {
		agent := m.agentPicker.choices[i]
		prefix := "  "
		style := titleStyle
		if i == m.agentPicker.index {
			prefix = ""
			style = accentStyle
		}
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
		row := style.Render(prefix + label)
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
