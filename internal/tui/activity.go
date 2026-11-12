package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
)

func (m model) selectedActivity() (orchestrator.ToolActivity, int, bool) {
	if len(m.tasks) == 0 || m.selected < 0 || m.selected >= len(m.tasks) {
		return orchestrator.ToolActivity{}, 0, false
	}
	activities := m.visibleActivities()
	if len(activities) == 0 {
		return orchestrator.ToolActivity{}, 0, false
	}
	for i, a := range activities {
		if a.ID == m.activityID && (m.activityAttempt == "" || a.AttemptID == m.activityAttempt) {
			return a, i, true
		}
	}
	if m.activityID != "" {
		return orchestrator.ToolActivity{}, 0, false
	}
	return activities[0], 0, true
}
func (m *model) activityKey(key string) bool {
	if !m.activity {
		return false
	}
	if key == "f" {
		m.activityFilter = (m.activityFilter + 1) % 3
		m.activityID = ""
		m.activityAttempt = ""
		m.activityExpanded = false
		m.historyOffset = 0
		return true
	}
	if key == "esc" && m.activityExpanded {
		m.activityExpanded = false
		m.historyOffset = 0
		return true
	}
	if key == "enter" {
		a, _, ok := m.selectedActivity()
		if ok {
			m.activityID = a.ID
			m.activityAttempt = a.AttemptID
			m.activityExpanded = !m.activityExpanded
			m.historyOffset = m.maxScroll()
		}
		return true
	}
	if m.activityExpanded || (!m.detailFocus && !m.fullscreen) {
		return false
	}
	if key != "j" && key != "k" && key != "up" && key != "down" {
		return false
	}
	_, i, ok := m.selectedActivity()
	if !ok {
		return true
	}
	delta := 1
	if key == "k" || key == "up" {
		delta = -1
	}
	activities := m.visibleActivities()
	i = max(0, min(len(activities)-1, i+delta))
	m.activityID = activities[i].ID
	m.activityAttempt = activities[i].AttemptID
	// Keep the cursor visible while retaining the existing line-scroll controls.
	m.historyOffset = max(0, len(activities)-i-1-m.detailPage()/2)
	return true
}
func prettyPayload(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "(not available)"
	}
	var b bytes.Buffer
	if err := json.Indent(&b, raw, "", "  "); err == nil {
		return terminalText(b.String())
	}
	return terminalText(string(raw))
}
func (m model) activityContent(width int) []string {
	var lines []string
	add := func(s string) { lines = append(lines, strings.Split(ansi.Hardwrap(s, width, true), "\n")...) }
	if len(m.tasks) == 0 {
		return nil
	}
	activities := m.visibleActivities()
	if m.activityExpanded {
		a, _, ok := m.selectedActivity()
		if !ok {
			add("This call is no longer retained. Esc returns to the list.")
			return lines
		}
		statusStyle, glyph := m.toolStatusStyle(a.Status)
		add(titleStyle.Render(terminalText(a.Name)) + "  " + statusStyle.Bold(true).Render(glyph+" "+a.Status))
		add(mutedStyle.Render("Attempt " + a.AttemptID + "  ·  " + a.StartedAt.Local().Format("15:04:05")))
		if a.DurationMS != nil {
			add(fmt.Sprintf("Duration %.0f ms", *a.DurationMS))
		}
		add("")
		add(accentStyle.Render("ARGUMENTS"))
		add(prettyPayload(a.Args))
		add("")
		add(accentStyle.Render("RESULT"))
		add(readableResult(a.Result))
		return lines
	}
	scope := []string{"all attempts", "latest attempt", "errors only"}[m.activityFilter]
	add(mutedStyle.Render("Filter: " + scope + " (f)"))
	if len(activities) == 0 {
		if m.activityFilter == 0 {
			add("No tool calls yet.")
		} else {
			add("No calls match this filter.")
		}
		return lines
	}
	selected, _, ok := m.selectedActivity()
	for _, a := range activities {
		isSelected := ok && a.ID == selected.ID && a.AttemptID == selected.AttemptID
		marker := "  "
		if isSelected {
			marker = "▸ "
		}
		style, glyph := m.toolStatusStyle(a.Status)
		stamp := a.StartedAt.Local().Format("15:04:05")
		summary := terminalText(a.Summary())
		duration := ""
		if a.DurationMS != nil {
			duration = fmt.Sprintf(" · %.0fms", *a.DurationMS)
		}
		plain := marker + stamp + " " + glyph + " " + a.Status
		if summary != "" {
			plain += " · " + summary
		}
		plain += duration
		if isSelected {
			add(selectionRow(plain, width))
			continue
		}
		row := marker + mutedStyle.Render(stamp) + " " + style.Bold(true).Render(glyph+" "+a.Status)
		if summary != "" {
			row += mutedStyle.Render(" · ") + styledToolSummary(a.Summary())
		}
		row += mutedStyle.Render(duration)
		add(fit(row, width, 1))
	}
	return lines
}

// readableResult preserves actual line breaks in Pi text results instead of
// making command output readable only through JSON escape sequences.
func readableResult(raw json.RawMessage) string {
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Details json.RawMessage `json:"details"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return prettyPayload(raw)
	}
	var blocks []string
	for _, block := range result.Content {
		if block.Type == "text" {
			blocks = append(blocks, terminalText(block.Text))
		} else {
			return prettyPayload(raw)
		}
	}
	if len(blocks) == 0 {
		return prettyPayload(raw)
	}
	if len(result.Details) > 0 && string(result.Details) != "null" && string(result.Details) != "{}" {
		blocks = append(blocks, "Metadata:\n"+prettyPayload(result.Details))
	}
	return strings.Join(blocks, "\n\n")
}

func (m model) visibleActivities() []orchestrator.ToolActivity {
	if m.selected < 0 || m.selected >= len(m.tasks) {
		return nil
	}
	t := m.tasks[m.selected]
	if m.activityFilter == 0 {
		return t.Activities
	}
	latest := ""
	if len(t.Attempts) > 0 {
		latest = t.Attempts[len(t.Attempts)-1].ID
	}
	var result []orchestrator.ToolActivity
	for _, a := range t.Activities {
		if m.activityFilter == 1 && a.AttemptID != latest {
			continue
		}
		if m.activityFilter == 2 && a.Status != "failed" && a.Status != "interrupted" {
			continue
		}
		result = append(result, a)
	}
	return result
}

func (m model) currentAction(t orchestrator.Task) string {
	if t.Status != orchestrator.Running || len(t.Attempts) == 0 {
		return ""
	}
	attempt := t.Attempts[len(t.Attempts)-1].ID
	for i := len(t.Activities) - 1; i >= 0; i-- {
		a := t.Activities[i]
		if a.AttemptID == attempt && a.Status == "running" {
			return m.runningGlyph() + " " + terminalText(a.Summary())
		}
	}
	return ""
}
