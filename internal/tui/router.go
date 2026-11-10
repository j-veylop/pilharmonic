package tui

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

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
		if m.routerFocus == 0 {
			if query, ok := m.routerMentionQuery(); ok {
				m.completeRouterMention(query)
				return m, nil
			}
		}
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
		if task.ID == "" {
			// Nothing was accepted, so the draft is still the operator's only
			// copy of the request. Keep it, and the cursor, for correction.
			if err != nil {
				m.notice = err.Error()
			}
			return m, nil
		}
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

// mentionQuery describes the leading @executor token being typed. Completion
// and hints only apply to that first token so an @ inside prose stays data.
type mentionQuery struct {
	start  int
	end    int
	prefix string
}

func (m model) routerMentionQuery() (mentionQuery, bool) {
	text := m.routerDraft
	cursor := min(max(0, m.routerCursor), len(text))
	if cursor == 0 {
		return mentionQuery{}, false
	}
	start := cursor
	for start > 0 {
		r, size := utf8.DecodeLastRuneInString(text[:start])
		if unicode.IsSpace(r) {
			break
		}
		start -= size
	}
	if start >= len(text) || text[start] != '@' || cursor <= start {
		return mentionQuery{}, false
	}
	if strings.TrimSpace(text[:start]) != "" {
		return mentionQuery{}, false
	}
	end := cursor
	for end < len(text) {
		r, size := utf8.DecodeRuneInString(text[end:])
		if unicode.IsSpace(r) {
			break
		}
		end += size
	}
	return mentionQuery{start: start, end: end, prefix: text[start+1 : cursor]}, true
}

// routerMatchingAgents lists registered executors matching the typed prefix.
// general stays first so a bare @ completes predictably.
func (m model) routerMatchingAgents(prefix string) []orchestrator.AgentInfo {
	if m.service == nil {
		return nil
	}
	agents := m.service.ListAgents()
	prefix = strings.ToLower(prefix)
	ordered := make([]orchestrator.AgentInfo, 0, len(agents))
	for _, agent := range agents {
		if agent.Name == orchestrator.DefaultRouteAgent {
			ordered = append(ordered, agent)
		}
	}
	for _, agent := range agents {
		if agent.Name != orchestrator.DefaultRouteAgent {
			ordered = append(ordered, agent)
		}
	}
	matching := ordered[:0]
	for _, agent := range ordered {
		if strings.HasPrefix(strings.ToLower(agent.Name), prefix) {
			matching = append(matching, agent)
		}
	}
	return matching
}

func (m *model) completeRouterMention(query mentionQuery) {
	agents := m.routerMatchingAgents(query.prefix)
	if len(agents) == 0 {
		m.notice = "No registered executor matches @" + query.prefix
		return
	}
	replacement := "@" + agents[0].Name
	m.routerDraft = m.routerDraft[:query.start] + replacement + m.routerDraft[query.end:]
	cursor := query.start + len(replacement)
	if cursor == len(m.routerDraft) {
		m.routerDraft += " "
		cursor++
	} else if r, _ := utf8.DecodeRuneInString(m.routerDraft[cursor:]); !unicode.IsSpace(r) {
		m.routerDraft = m.routerDraft[:cursor] + " " + m.routerDraft[cursor:]
		cursor++
	}
	m.routerCursor = cursor
	m.routerScroll = 0
}

// routerMentionHintLines shows available executors while the leading mention is
// being edited. The composer keeps the first matching name for Tab completion.
func (m model) routerMentionHintLines(width int) []string {
	query, ok := m.routerMentionQuery()
	if !ok {
		return nil
	}
	agents := m.routerMatchingAgents(query.prefix)
	if len(agents) == 0 {
		return []string{fit(mutedStyle.Render("No registered executor matches @"+query.prefix), width, 1)}
	}
	names := make([]string, 0, len(agents))
	for _, agent := range agents {
		names = append(names, "@"+agent.Name)
	}
	hint := "Executors: " + strings.Join(names, "  ") + "  ·  tab completes @" + agents[0].Name
	return []string{fit(mutedStyle.Render(hint), width, 1)}
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
	// The footer (notice and wrapped hints), the panel border and the header
	// share the terminal with the bottom composer. Cap the composer rows so the
	// cursor row is never clipped by the panel on short terminals.
	footer := m.footer(width)
	body := height - 1 - (strings.Count(footer, "\n") + 1)
	return max(1, min(lines, body-5))
}

func (m model) routerPanel(width, height int) string {
	inner := max(1, width-4)
	policy := lipgloss.NewStyle().Foreground(cyan).Bold(true).Render(orchestrator.LocalPolicyVersion)
	header := accentStyle.Render("ROUTE WORK") + mutedStyle.Render("  ·  policy ") + policy

	var top []string
	attention := m.attentionTasks()
	top = append(top, accentStyle.Render(fmt.Sprintf("NEEDS ATTENTION  %02d", len(attention))))
	if len(attention) == 0 {
		top = append(top, mutedStyle.Render("Nothing needs attention."))
	} else {
		limit := min(len(attention), 8)
		for i := 0; i < limit; i++ {
			top = append(top, m.routerAttentionRow(attention[i], i, inner))
		}
		if len(attention) > limit {
			top = append(top, mutedStyle.Render(fmt.Sprintf("+%d more", len(attention)-limit)))
		}
	}
	top = append(top, "")
	top = append(top, accentStyle.Render("RECENT ROUTES"))
	if len(m.tasks) == 0 {
		top = append(top, mutedStyle.Render("No requests yet."))
	} else {
		limit := min(len(m.tasks), 8)
		for i := 0; i < limit; i++ {
			top = append(top, m.routerRecentRow(m.tasks[i], inner))
		}
		if len(m.tasks) > limit {
			top = append(top, mutedStyle.Render(fmt.Sprintf("+%d earlier", len(m.tasks)-limit)))
		}
	}

	// Keep the composer at the bottom of the panel. Drop list rows first when
	// the terminal is too short for both, then the header, so the composer and
	// its cursor stay visible.
	composer := append(m.routerComposerLines(inner), m.routerMentionHintLines(inner)...)
	contentHeight := max(1, height-2)
	maxTop := max(0, contentHeight-len(composer)-3)
	if len(top) > maxTop {
		top = top[:maxTop]
	}
	lines := make([]string, 0, 3+len(top)+len(composer))
	lines = append(lines, header)
	if len(top) > 0 {
		lines = append(lines, "")
		lines = append(lines, top...)
	}
	if len(lines)+1+len(composer) <= contentHeight {
		lines = append(lines, "")
	}
	lines = append(lines, composer...)
	if len(lines) > contentHeight {
		drop := min(len(lines)-contentHeight, len(lines)-len(composer))
		lines = lines[drop:]
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
		return []string{prefix + mutedStyle.Render("Describe the work… type @ to choose an executor") + "▏"}
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
	return fit(m.taskLabel(task)+"  "+workspaceBadgeFor(task).glyphOnly()+"  "+titleStyle.Render(title), width, 1)
}

func (m model) routerRecentRow(task orchestrator.Task, width int) string {
	stamp := task.CreatedAt.Local().Format("15:04")
	title := strings.ReplaceAll(terminalText(task.Title), "\n", " ")
	status := m.taskLabel(task)
	row := mutedStyle.Render(stamp) + "  " + status + "  " +
		titleStyle.Render(terminalText(executorLabel(task))) + mutedStyle.Render(" · ") + workspaceBadgeFor(task).render() + "  " + title
	return fit(row, width, 1)
}
