package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"orchestrator/internal/orchestrator"
)

// attentionStages are the task stages that need a person. They are intentionally
// separate from process status: a completed process is not automatically
// reviewed, and a queued or running task needs no action unless it reports a
// block or an undelivered input.
var attentionStages = map[string]bool{
	"blocked":           true,
	"waiting":           true,
	"awaiting review":   true,
	"input undelivered": true,
	"merge failed":      true,
	"failed":            true,
	"interrupted":       true,
}

func needsAttention(task orchestrator.Task) bool {
	return attentionStages[task.Stage()]
}

func (m model) attentionTasks() []orchestrator.Task {
	var result []orchestrator.Task
	for _, task := range m.tasks {
		if needsAttention(task) {
			result = append(result, task)
		}
	}
	return result
}

func (m *model) clampRouter() {
	if m.routerFocus < 0 || m.routerFocus > 1 {
		m.routerFocus = 0
	}
	attention := m.attentionTasks()
	limit := min(len(attention), 8)
	if m.routerSelected >= limit {
		m.routerSelected = limit - 1
	}
	if m.routerSelected < 0 {
		m.routerSelected = 0
	}
}

func routeWorkspace(task orchestrator.Task) string {
	if task.Isolated {
		return "isolated worktree"
	}
	if task.ParentID != "" {
		return "inherited workspace"
	}
	return "shared workspace"
}

func routeNotice(task orchestrator.Task) string {
	return "Routed to " + terminalText(executorLabel(task)) + " · " + string(task.Status) + " · " + routeWorkspace(task) + " — " + terminalText(task.Title)
}

// routerKey handles the unified entry point. Typing goes to the composer; Tab
// moves to the attention list so it can be inspected without leaving the router.
func (m model) routerKey(msg tea.KeyMsg) (model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.router = false
		m.routerFocus = 0
		m.routerDraft = ""
		m.routerCursor = 0
		m.routerScroll = 0
		m.clampScroll()
		return m, nil
	case "tab":
		m.routerFocus = 1 - m.routerFocus
		m.clampRouter()
		return m, nil
	}
	if m.routerFocus == 1 {
		switch msg.String() {
		case "j", "down":
			m.routerSelected++
		case "k", "up":
			m.routerSelected--
		case "enter":
			attention := m.attentionTasks()
			if len(attention) > 0 {
				target := attention[min(m.routerSelected, len(attention)-1)]
				m.tasks = m.service.ListTasks()
				m.router = false
				m.routerFocus = 0
				m.historyOffset = 0
				m.detailFocus = false
				m.clamp()
				for i, task := range m.tasks {
					if task.ID == target.ID {
						m.selected = i
						break
					}
				}
				m.notice = "Inspecting " + terminalText(target.Title)
			}
		}
		m.clampRouter()
		return m, nil
	}
	switch msg.String() {
	case "enter":
		text := strings.TrimSpace(m.routerDraft)
		if text == "" {
			m.notice = "Enter a request first."
			return m, nil
		}
		if m.service == nil {
			m.notice = "Router is not connected to a service."
			return m, nil
		}
		task, err := m.service.SubmitRequest(text, m.workspace)
		if err != nil {
			m.notice = err.Error()
		} else {
			m.notice = routeNotice(task)
		}
		m.tasks = m.service.ListTasks()
		m.routerDraft = ""
		m.routerCursor = 0
		m.routerScroll = 0
		m.routerSelected = 0
		for i, current := range m.tasks {
			if current.ID == task.ID {
				m.selected = i
				break
			}
		}
		m.clamp()
		m.clampScroll()
	default:
		e := m.routerEditor()
		e.handleKey(msg, m.routerComposerWidth(), m.routerComposerHeight())
		m.setRouterEditor(e)
	}
	return m, nil
}

func (m model) routerEditor() editor {
	return editor{text: m.routerDraft, cursor: m.routerCursor, scroll: m.routerScroll}
}

func (m *model) setRouterEditor(e editor) {
	m.routerDraft = e.text
	m.routerCursor = e.cursor
	m.routerScroll = e.scroll
}

func (m model) routerComposerWidth() int {
	width := m.width
	if width <= 0 {
		width = 80
	}
	inner := max(1, width-4)
	return max(1, inner-lipgloss.Width("▸ ")-1)
}

func (m model) routerComposerHeight() int {
	height := m.height
	if height <= 0 {
		height = 24
	}
	width := m.width
	if width <= 0 {
		width = 80
	}
	lines := max(2, min(composerMaxLines, height/4))
	// The footer (notice and wrapped hints) and the panel border, header and
	// blank share the terminal with the composer. Cap the composer rows so the
	// cursor row is never clipped by the panel on short terminals.
	footer := m.footer(width)
	body := height - 1 - (strings.Count(footer, "\n") + 1)
	return max(1, min(lines, body-4))
}

func (m model) routerPanel(width, height int) string {
	inner := max(1, width-4)
	var lines []string
	policy := lipgloss.NewStyle().Foreground(cyan).Bold(true).Render(orchestrator.LocalPolicyVersion)
	lines = append(lines, accentStyle.Render("ROUTE WORK")+mutedStyle.Render("  ·  policy ")+policy)
	lines = append(lines, "")
	lines = append(lines, m.routerComposerLines(inner)...)
	lines = append(lines, "")
	attention := m.attentionTasks()
	lines = append(lines, accentStyle.Render(fmt.Sprintf("NEEDS ATTENTION  %02d", len(attention))))
	if len(attention) == 0 {
		lines = append(lines, mutedStyle.Render("Nothing needs attention."))
	} else {
		limit := min(len(attention), 8)
		for i := 0; i < limit; i++ {
			lines = append(lines, m.routerAttentionRow(attention[i], i, inner))
		}
		if len(attention) > limit {
			lines = append(lines, mutedStyle.Render(fmt.Sprintf("+%d more", len(attention)-limit)))
		}
	}
	lines = append(lines, "")
	lines = append(lines, accentStyle.Render("RECENT ROUTES"))
	if len(m.tasks) == 0 {
		lines = append(lines, mutedStyle.Render("No requests yet."))
	} else {
		limit := min(len(m.tasks), 8)
		for i := 0; i < limit; i++ {
			lines = append(lines, m.routerRecentRow(m.tasks[i], inner))
		}
		if len(m.tasks) > limit {
			lines = append(lines, mutedStyle.Render(fmt.Sprintf("+%d earlier", len(m.tasks)-limit)))
		}
	}
	return panel(strings.Join(lines, "\n"), width, height)
}

func (m model) routerComposer() string {
	return strings.Join(m.routerComposerLines(max(1, m.width-4)), "\n")
}

func (m model) routerComposerLines(width int) []string {
	prefix := accentStyle.Render("▸ ")
	pad := strings.Repeat(" ", lipgloss.Width("▸ "))
	if m.routerFocus == 1 {
		prefix = mutedStyle.Render("▸ ")
	}
	if m.routerDraft == "" && m.routerFocus == 0 {
		return []string{prefix + mutedStyle.Render("Describe the work…") + "▏"}
	}
	contentWidth := max(1, width-lipgloss.Width("▸ ")-1)
	visible := m.routerEditor().viewLines(contentWidth, m.routerComposerHeight())
	lines := make([]string, 0, len(visible))
	for i, line := range visible {
		if i == 0 {
			lines = append(lines, prefix+line)
		} else {
			lines = append(lines, pad+line)
		}
	}
	return lines
}

func (m model) routerAttentionRow(task orchestrator.Task, index, width int) string {
	title := strings.ReplaceAll(terminalText(task.Title), "\n", " ")
	plain := "● " + strings.ToUpper(task.Stage()) + "  " + title
	if m.routerFocus == 1 && index == m.routerSelected {
		return selectionRow(plain, width)
	}
	return fit(taskLabel(task)+"  "+workspaceBadgeFor(task).glyphOnly()+"  "+titleStyle.Render(title), width, 1)
}

func (m model) routerRecentRow(task orchestrator.Task, width int) string {
	stamp := task.CreatedAt.Local().Format("15:04")
	title := strings.ReplaceAll(terminalText(task.Title), "\n", " ")
	status := statusLabel(task.Status)
	if task.Status == orchestrator.Queued && task.ScheduleRequested {
		status = taskLabel(task)
	}
	row := mutedStyle.Render(stamp) + "  " + status + "  " +
		titleStyle.Render(terminalText(executorLabel(task))) + mutedStyle.Render(" · ") + workspaceBadgeFor(task).render() + "  " + title
	return fit(row, width, 1)
}
