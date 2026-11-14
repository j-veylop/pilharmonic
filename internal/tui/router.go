package tui

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"pilharmonic/internal/orchestrator"
)

// attentionStages need a person. They are separate from process status: a
// completed process is not automatically reviewed, and a queued or running
// task needs no action unless it reports a block or an undelivered input.
var attentionStages = map[string]bool{
	orchestrator.StageBlocked:          true,
	orchestrator.StageWaiting:          true,
	orchestrator.StageAwaitingReview:   true,
	orchestrator.StageInputUndelivered: true,
	orchestrator.StageMergeFailed:      true,
	orchestrator.StageFailed:           true,
	orchestrator.StageInterrupted:      true,
}

func needsAttention(task orchestrator.Task) bool { return attentionStages[task.Stage()] }

func (m model) attentionTasks() []orchestrator.Task {
	var result []orchestrator.Task
	for _, task := range m.tasks {
		if needsAttention(task) {
			result = append(result, task)
		}
	}
	return result
}

// Router focus modes. The composer owns typing; Tab moves to a list so its
// rows can be inspected without submitting a request.
const (
	routerFocusComposer = iota
	routerFocusAttention
	routerFocusRecent
)

// routerListLimit caps how many rows of each list are selectable.
const routerListLimit = 8

func clampRouterSelection(index, count int) int {
	return max(0, min(index, min(count, routerListLimit)-1))
}

// routerList is the task list a focus mode moves through; the composer has none.
func (m model) routerList(focus int) []orchestrator.Task {
	switch focus {
	case routerFocusAttention:
		return m.attentionTasks()
	case routerFocusRecent:
		return m.tasks
	}
	return nil
}

func (m *model) routerIndex() *int {
	if m.router.focus == routerFocusRecent {
		return &m.router.recentSelected
	}
	return &m.router.selected
}

func (m model) routerFocusHasRows(focus int) bool {
	return focus == routerFocusComposer || len(m.routerList(focus)) > 0
}

func (m *model) clampRouter() {
	if m.router.focus < routerFocusComposer || m.router.focus > routerFocusRecent {
		m.router.focus = routerFocusComposer
	}
	m.router.selected = clampRouterSelection(m.router.selected, len(m.attentionTasks()))
	m.router.recentSelected = clampRouterSelection(m.router.recentSelected, len(m.tasks))
	if !m.routerFocusHasRows(m.router.focus) {
		// Say so rather than letting the next Enter submit the draft where the
		// operator expected a no-op on the list.
		m.notice.text = "That list is empty; the request draft has focus."
		m.router.focus = routerFocusComposer
	}
}

// nextRouterFocus cycles through the focus modes, skipping empty lists.
func (m model) nextRouterFocus(delta int) int {
	focus := m.router.focus
	for step := 0; step < 3; step++ {
		focus = (focus + delta + 3) % 3
		if m.routerFocusHasRows(focus) {
			return focus
		}
	}
	return routerFocusComposer
}

// moveRouterSelection moves within the focused list without wrapping.
func (m *model) moveRouterSelection(delta int) {
	if rows := min(len(m.routerList(m.router.focus)), routerListLimit); rows > 0 {
		index := m.routerIndex()
		*index = max(0, min(rows-1, *index+delta))
	}
}

func (m model) routerFocusedTask() (orchestrator.Task, bool) {
	list := m.routerList(m.router.focus)
	if len(list) == 0 {
		return orchestrator.Task{}, false
	}
	return list[min(*m.routerIndex(), len(list)-1)], true
}

// openRouterTask leaves the request form for the board with target selected.
func (m *model) openRouterTask(target orchestrator.Task) {
	m.refreshTasks()
	m.router.open = false
	m.router.focus = routerFocusComposer
	m.historyOffset = 0
	m.detailFocus = false
	m.resetActivitySelection()
	m.selectTask(target.ID)
	m.notice.text = "Inspecting " + terminalText(target.Title)
}

func routeNotice(task orchestrator.Task) string {
	workspace := "shared workspace"
	if task.Isolated {
		workspace = "isolated worktree"
	} else if task.ParentID != "" {
		workspace = "inherited workspace"
	}
	return "Request accepted · " + terminalText(executorLabel(task)) + " · " + string(task.Status) + " · " + workspace + " — " + terminalText(task.Title)
}

func (m model) recentSelectedID() string {
	if m.router.recentSelected < 0 || m.router.recentSelected >= len(m.tasks) {
		return ""
	}
	return m.tasks[m.router.recentSelected].ID
}

// anchorRecentSelection keeps the recent-list rail on its task after a
// refresh, clamped to the rows that are drawn.
func (m *model) anchorRecentSelection(id string) {
	for i, task := range m.tasks {
		if id != "" && task.ID == id {
			m.router.recentSelected = i
			break
		}
	}
	m.router.recentSelected = clampRouterSelection(m.router.recentSelected, len(m.tasks))
}

func (m *model) clearRouterDraft() { m.router.editor = editor{} }

// routerKey handles the request form. Typing goes to the draft; Tab moves to
// the action or recent list so a task can be inspected without submitting.
func (m model) routerKey(msg tea.KeyMsg) (model, tea.Cmd) {
	key := msg.String()
	switch {
	case key == "ctrl+c":
		return m.requestQuit(), nil
	case key == "esc":
		m.router.open = false
		m.router.focus = routerFocusComposer
		m.clampScroll()
	case key == "tab" && m.router.focus == routerFocusComposer && m.completeRouterMention():
	case key == "tab" || key == "shift+tab":
		delta := 1
		if key == "shift+tab" {
			delta = -1
		}
		m.router.focus = m.nextRouterFocus(delta)
		m.clampRouter()
	case m.router.focus != routerFocusComposer:
		switch key {
		case "j", "down":
			m.moveRouterSelection(1)
		case "k", "up":
			m.moveRouterSelection(-1)
		case "enter":
			if target, ok := m.routerFocusedTask(); ok {
				m.openRouterTask(target)
			}
		}
		m.clampRouter()
	case key == "enter":
		return m.submitRequest()
	default:
		m.router.handleKey(msg, m.composerWidth(), m.composerHeight())
	}
	return m, nil
}

// submitRequest dispatches a slash command or submits the draft off the update
// loop. Nothing can be routed without a provider, so login comes first.
func (m model) submitRequest() (model, tea.Cmd) {
	text := strings.TrimSpace(m.router.text)
	switch {
	case strings.HasPrefix(text, "/"):
		m = m.slashCommand(text)
		m.clearRouterDraft()
		return m, nil
	case text == "":
		m.notice.text = "Enter a request first."
		return m, nil
	case !m.service.AnyProviderConfigured():
		m.login.menu, m.login.index = true, 0
		m.notice.text = forcedLoginNotice
		return m, nil
	}
	service, workspace := m.service, m.workspace
	return m, func() tea.Msg {
		task, err := service.SubmitRequest(text, workspace)
		return routerResultMsg{task: task, err: err}
	}
}

// mentionQuery describes the leading @executor token being typed. Completion
// and hints only apply to that first token so an @ inside prose stays data.
type mentionQuery struct {
	start  int
	end    int
	prefix string
}

func (m model) routerMentionQuery() (mentionQuery, bool) {
	text := m.router.text
	cursor := min(max(0, m.router.cursor), len(text))
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

// routerMatchingAgents lists registered executors matching the typed prefix,
// the default route first so a bare @ completes predictably.
func (m model) routerMatchingAgents(prefix string) []orchestrator.AgentInfo {
	var matching []orchestrator.AgentInfo
	for _, agent := range m.service.ListAgents() {
		switch {
		case !strings.HasPrefix(strings.ToLower(agent.Name), strings.ToLower(prefix)):
		case agent.Name == orchestrator.DefaultRouteAgent:
			matching = append([]orchestrator.AgentInfo{agent}, matching...)
		default:
			matching = append(matching, agent)
		}
	}
	return matching
}

// completeRouterMention completes a leading @executor being typed and reports
// whether there was one.
func (m *model) completeRouterMention() bool {
	query, ok := m.routerMentionQuery()
	if !ok {
		return false
	}
	agents := m.routerMatchingAgents(query.prefix)
	if len(agents) == 0 {
		m.notice.text = "No registered executor matches @" + query.prefix
		return true
	}
	replacement := "@" + agents[0].Name
	m.router.text = m.router.text[:query.start] + replacement + m.router.text[query.end:]
	cursor := query.start + len(replacement)
	if cursor == len(m.router.text) {
		m.router.text += " "
		cursor++
	} else if r, _ := utf8.DecodeRuneInString(m.router.text[cursor:]); !unicode.IsSpace(r) {
		m.router.text = m.router.text[:cursor] + " " + m.router.text[cursor:]
		cursor++
	}
	m.router.cursor = cursor
	m.router.scroll = 0
	return true
}

// routerMentionHintLines shows available executors while the leading mention is
// being edited. The composer keeps the first matching name for Tab completion.
func (m model) routerMentionHintLines(width int) []string {
	if m.login.provider != "" || m.router.focus != routerFocusComposer {
		return nil
	}
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

// routerPanel lists the work that needs action and the newest tasks above
// the docked request box. A focused list scrolls so its selected row stays
// visible together with its heading when both fit.
func (m model) routerPanel(width, height int) string {
	inner := max(1, width-4)
	lines := []string{mutedStyle.Render("Accepted requests become tasks."), ""}
	selectedLine := -1
	section := func(focus int, heading, empty, more string, tasks []orchestrator.Task, row func(orchestrator.Task, bool, int) string) {
		lines = append(lines, accentStyle.Render(heading))
		if len(tasks) == 0 {
			lines = append(lines, mutedStyle.Render(empty))
		}
		limit := min(len(tasks), routerListLimit)
		for i, task := range tasks[:limit] {
			selected := m.router.focus == focus && i == *m.routerIndex()
			if selected {
				selectedLine = len(lines)
			}
			lines = append(lines, row(task, selected, inner))
		}
		if len(tasks) > limit {
			lines = append(lines, mutedStyle.Render(fmt.Sprintf(more, len(tasks)-limit)))
		}
	}
	action := m.attentionTasks()
	section(routerFocusAttention, fmt.Sprintf("NEEDS ACTION  %02d", len(action)), "Nothing needs action.", "+%d more", action, m.routerAttentionRow)
	lines = append(lines, "")
	section(routerFocusRecent, "RECENT TASKS", "No tasks yet. Submit a request to create one.", "+%d earlier", m.tasks, m.routerRecentRow)
	if interior := height - 2; selectedLine >= 0 && len(lines) > interior && interior > 0 {
		lines = lines[max(0, selectedLine-interior+1):]
	}
	return panel(strings.Join(lines, "\n"), width, height)
}

func (m model) routerAttentionRow(task orchestrator.Task, selected bool, width int) string {
	title := singleLine(task.Title)
	if selected {
		return selectionRow("● "+strings.ToUpper(task.Stage())+"  "+title, width)
	}
	return fit(m.taskLabel(task)+"  "+workspaceBadgeFor(task).glyphOnly()+"  "+titleStyle.Render(title), width, 1)
}

func (m model) routerRecentRow(task orchestrator.Task, selected bool, width int) string {
	stamp := task.CreatedAt.Local().Format("15:04")
	title := singleLine(task.Title)
	executor := terminalText(executorLabel(task))
	if selected {
		_, label, _ := m.taskState(task)
		return selectionRow(stamp+"  ● "+label+"  "+executor+"  "+title, width)
	}
	row := mutedStyle.Render(stamp) + "  " + m.taskLabel(task) + "  " +
		titleStyle.Render(executor) + mutedStyle.Render(" · ") + workspaceBadgeFor(task).render() + "  " + title
	return fit(row, width, 1)
}
