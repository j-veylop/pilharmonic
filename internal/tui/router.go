package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
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

func routeState(task orchestrator.Task) string {
	if task.Status == orchestrator.Queued && task.ScheduleRequested {
		return "queued for capacity"
	}
	return string(task.Status)
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
	return "Routed to " + terminalText(task.Agent) + " · " + routeState(task) + " · " + routeWorkspace(task) + " · " + terminalText(task.Title)
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
	switch msg.Type {
	case tea.KeyEnter:
		text := strings.TrimSpace(m.routerDraft)
		if text == "" {
			m.notice = "Describe the work before routing."
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
		m.routerSelected = 0
		for i, current := range m.tasks {
			if current.ID == task.ID {
				m.selected = i
				break
			}
		}
		m.clamp()
		m.clampScroll()
	case tea.KeyBackspace, tea.KeyCtrlH:
		if len(m.routerDraft) > 0 {
			_, n := utf8.DecodeLastRuneInString(m.routerDraft)
			m.routerDraft = m.routerDraft[:len(m.routerDraft)-n]
		}
	case tea.KeyRunes:
		m.routerDraft += string(msg.Runes)
	case tea.KeySpace:
		m.routerDraft += " "
	}
	return m, nil
}

func (m model) routerPanel(width, height int) string {
	inner := max(1, width-4)
	var lines []string
	lines = append(lines, accentStyle.Render("ROUTE WORK")+mutedStyle.Render("  ·  deterministic local policy "+orchestrator.LocalPolicyVersion))
	lines = append(lines, mutedStyle.Render("Describe the work once. The router assigns the executor, starts it, and surfaces what needs you."))
	lines = append(lines, "")
	lines = append(lines, fit(m.routerComposer(), inner, 1))
	lines = append(lines, mutedStyle.Render("enter route · tab attention list · esc task board"))
	lines = append(lines, "")
	attention := m.attentionTasks()
	lines = append(lines, accentStyle.Render(fmt.Sprintf("NEEDS ATTENTION  %02d", len(attention))))
	if len(attention) == 0 {
		lines = append(lines, mutedStyle.Render("Nothing needs you right now."))
	} else {
		limit := min(len(attention), 8)
		for i := 0; i < limit; i++ {
			lines = append(lines, m.routerAttentionRow(attention[i], i, inner))
		}
		if len(attention) > limit {
			lines = append(lines, mutedStyle.Render(fmt.Sprintf("+%d more in the task board", len(attention)-limit)))
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
	lines = append(lines, "")
	lines = append(lines, mutedStyle.Render("Every trusted local request is accepted; the decision, assignment and start are recorded in task history."))
	return panel(strings.Join(lines, "\n"), width, height)
}

func (m model) routerComposer() string {
	cursor := accentStyle.Render("▏")
	prefix := accentStyle.Render("▸ ")
	if m.routerFocus == 1 {
		prefix = mutedStyle.Render("▸ ")
		cursor = mutedStyle.Render("▏")
	}
	draft := terminalText(m.routerDraft)
	if draft == "" && m.routerFocus == 0 {
		return prefix + mutedStyle.Render("Describe the work…") + cursor
	}
	return prefix + draft + cursor
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
		titleStyle.Render(terminalText(task.Agent)) + mutedStyle.Render(" · ") + workspaceBadgeFor(task).render() + "  " + title
	return fit(row, width, 1)
}

// routerFooterHint keeps the router footer in one place.
func routerFooterHint() string {
	return "type request · enter route · tab attention · esc tasks · ? help · ctrl+c quit"
}
