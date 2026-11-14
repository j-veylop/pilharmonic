package tui

import (
	"fmt"
	"strings"

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

// pickerPanel lists a picker's choices, each as a label row and a muted detail
// row, in place of the task details.
func pickerPanel[T any](p pickerState[T], title, empty string, describe func(T) (label, detail string), width, height int) string {
	lines := []string{titleStyle.Render(title), mutedStyle.Render("Queued task only"), ""}
	start, end := windowRows(p.index, len(p.choices), (height-7)/2)
	for i := start; i < end; i++ {
		label, detail := describe(p.choices[i])
		row := titleStyle.Render("  " + label)
		if i == p.index {
			row = selectionRow(label, width-4)
		}
		lines = append(lines, row, mutedStyle.Render("  "+detail))
	}
	if len(p.choices) == 0 {
		lines = append(lines, mutedStyle.Render(empty))
	}
	return panel(strings.Join(lines, "\n"), width, height)
}

func describeProfile(profile orchestrator.Profile) (label, detail string) {
	details := []string{profile.Description}
	if profile.Model != "" {
		details = append(details, "model "+profile.Model)
	}
	if profile.NoIntegrate {
		details = append(details, "no auto-integration")
	}
	if profile.NoSubagents {
		details = append(details, "no delegation")
	}
	if len(profile.Tools) > 0 {
		details = append(details, "tools "+strings.Join(profile.Tools, ","))
	}
	if details[0] == "" {
		details = details[1:]
	}
	return terminalText(profileLabel(profile)), terminalText(strings.Join(details, " · "))
}

func describeAgent(agent orchestrator.AgentInfo) (label, detail string) {
	detail = "One-shot · no live context"
	if agent.Capabilities.LiveContext {
		detail = "Live follow-up context"
	}
	if agent.Capabilities.ToolActivity {
		detail += " · tool activity"
	}
	if agent.Capabilities.StructuredReports {
		detail += " · structured reports"
	}
	label = terminalText(agent.Name)
	if agent.Description != "" {
		label += " · " + terminalText(agent.Description)
	}
	return label, detail
}
