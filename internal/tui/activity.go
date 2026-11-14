package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
)

// activityFilter selects which tool calls the activity panel lists.
type activityFilter int

const (
	activityFilterAll activityFilter = iota
	activityFilterLatest
	activityFilterErrors
)

// resetActivitySelection clears the activity cursor so a task change does not
// carry one task's selected tool call to another.
func (m *model) resetActivitySelection() {
	m.activity.id = ""
	m.activity.attempt = ""
	m.activity.expanded = false
}

func (m model) selectedActivity() (orchestrator.ToolActivity, int, bool) {
	if len(m.tasks) == 0 || m.selected < 0 || m.selected >= len(m.tasks) {
		return orchestrator.ToolActivity{}, 0, false
	}
	activities := m.visibleActivities()
	if len(activities) == 0 {
		return orchestrator.ToolActivity{}, 0, false
	}
	for i, a := range activities {
		if a.ID == m.activity.id && (m.activity.attempt == "" || a.AttemptID == m.activity.attempt) {
			return a, i, true
		}
	}
	if m.activity.id != "" {
		return orchestrator.ToolActivity{}, 0, false
	}
	return activities[0], 0, true
}
func (m *model) activityKey(key string) bool {
	if m.tab != activityTab {
		return false
	}
	if key == "esc" && m.activity.expanded {
		m.activity.expanded = false
		m.historyOffset = 0
		return true
	}
	focused := m.detailFocus || m.fullscreen
	if key == "enter" && focused {
		a, _, ok := m.selectedActivity()
		if ok {
			m.activity.id = a.ID
			m.activity.attempt = a.AttemptID
			m.activity.expanded = !m.activity.expanded
			m.historyOffset = m.maxScroll()
		}
		return ok
	}
	if m.activity.expanded || !focused {
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
	m.activity.id = activities[i].ID
	m.activity.attempt = activities[i].AttemptID
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
	add := func(rows ...string) {
		for _, s := range rows {
			lines = append(lines, strings.Split(ansi.Hardwrap(s, width, true), "\n")...)
		}
	}
	if len(m.tasks) == 0 {
		return nil
	}
	if m.activity.expanded {
		m.activityDetail(add)
		return lines
	}
	activities := m.visibleActivities()
	scope := []string{"all attempts", "latest attempt", "errors only"}[int(m.activity.filter)]
	add(mutedStyle.Render("Filter: " + scope + " (e)"))
	if len(activities) == 0 {
		if m.activity.filter == activityFilterAll {
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
		style, glyph := toolStatusStyle(a.Status, m.runningGlyph())
		stamp := a.StartedAt.Local().Format("15:04:05")
		summary := terminalText(a.Summary())
		duration := ""
		if a.DurationMS != nil {
			duration = fmt.Sprintf(" · %.0fms", *a.DurationMS)
		}
		if isSelected {
			plain := marker + stamp + " " + glyph
			if summary != "" {
				plain += " · " + summary
			}
			add(selectionRow(plain+duration, width))
			continue
		}
		row := marker + mutedStyle.Render(stamp) + " " + style.Bold(true).Render(glyph)
		if summary != "" {
			row += mutedStyle.Render(" · ") + styledToolSummary(a.Summary())
		}
		add(fit(row+mutedStyle.Render(duration), width, 1))
	}
	return lines
}

// activityDetail shows the expanded call's arguments and result.
func (m model) activityDetail(add func(...string)) {
	a, _, ok := m.selectedActivity()
	if !ok {
		add("This call is no longer retained. Esc returns to the list.")
		return
	}
	statusStyle, glyph := toolStatusStyle(a.Status, m.runningGlyph())
	add(titleStyle.Render(terminalText(a.Name))+"  "+statusStyle.Bold(true).Render(glyph),
		mutedStyle.Render("Attempt "+a.AttemptID+"  ·  "+a.StartedAt.Local().Format("15:04:05")))
	if a.DurationMS != nil {
		add(fmt.Sprintf("Duration %.0f ms", *a.DurationMS))
	}
	add("", accentStyle.Render("ARGUMENTS"), prettyPayload(a.Args), "", accentStyle.Render("RESULT"), readableResult(a.Result))
}

// readableResult preserves actual line breaks in text results instead of
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
	if m.activity.filter == activityFilterAll {
		return t.Activities
	}
	latest := ""
	if len(t.Attempts) > 0 {
		latest = t.Attempts[len(t.Attempts)-1].ID
	}
	var result []orchestrator.ToolActivity
	for _, a := range t.Activities {
		if m.activity.filter == activityFilterLatest && a.AttemptID != latest {
			continue
		}
		if m.activity.filter == activityFilterErrors && a.Status != orchestrator.ActivityFailed && a.Status != orchestrator.ActivityInterrupted {
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
		if a.AttemptID == attempt && a.Status == orchestrator.ActivityRunning {
			return m.runningGlyph() + " " + terminalText(a.Summary())
		}
	}
	return ""
}
