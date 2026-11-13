package tui

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"pilharmonic/internal/orchestrator"
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

// Router focus modes. The composer owns typing; Tab moves to a list so its
// rows can be inspected without submitting a request.
const (
	routerFocusComposer = iota
	routerFocusAttention
	routerFocusRecent
)

// routerListLimit caps how many rows of each list are selectable. Focusing a
// list scrolls the panel so its selected row is always visible.
const routerListLimit = 8

func clampRouterSelection(index, count int) int {
	limit := min(count, routerListLimit)
	if index >= limit {
		index = limit - 1
	}
	return max(0, index)
}

func (m *model) clampRouter() {
	if m.router.focus < routerFocusComposer || m.router.focus > routerFocusRecent {
		m.router.focus = routerFocusComposer
	}
	m.router.selected = clampRouterSelection(m.router.selected, len(m.attentionTasks()))
	m.router.recentSelected = clampRouterSelection(m.router.recentSelected, len(m.tasks))
	if !m.routerFocusHasRows(m.router.focus) {
		// Do not let the composer take keys silently after a list empties:
		// the next Enter would submit the draft where the operator expected
		// a no-op on the list.
		if m.router.focus != routerFocusComposer {
			m.notice.text = "That list is empty; the request draft has focus."
		}
		m.router.focus = routerFocusComposer
	}
}

// routerFocusHasRows reports whether a focus mode has a row to move through.
// The composer always has one.
func (m model) routerFocusHasRows(focus int) bool {
	switch focus {
	case routerFocusAttention:
		return len(m.attentionTasks()) > 0
	case routerFocusRecent:
		return len(m.tasks) > 0
	}
	return true
}

// nextRouterFocus cycles through the focus modes, skipping empty lists so Tab
// always lands somewhere useful.
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

// routerFocusRows is the number of selectable rows in the focused list.
func (m model) routerFocusRows() int {
	switch m.router.focus {
	case routerFocusAttention:
		return min(len(m.attentionTasks()), routerListLimit)
	case routerFocusRecent:
		return min(len(m.tasks), routerListLimit)
	}
	return 0
}

// moveRouterSelection moves within the focused list without wrapping.
func (m *model) moveRouterSelection(delta int) {
	rows := m.routerFocusRows()
	if rows == 0 {
		return
	}
	if m.router.focus == routerFocusRecent {
		m.router.recentSelected = max(0, min(rows-1, m.router.recentSelected+delta))
		return
	}
	m.router.selected = max(0, min(rows-1, m.router.selected+delta))
}

// routerFocusedTask returns the task under the selection in the focused list.
func (m model) routerFocusedTask() (orchestrator.Task, bool) {
	switch m.router.focus {
	case routerFocusAttention:
		attention := m.attentionTasks()
		if len(attention) == 0 {
			return orchestrator.Task{}, false
		}
		return attention[min(m.router.selected, len(attention)-1)], true
	case routerFocusRecent:
		if len(m.tasks) == 0 {
			return orchestrator.Task{}, false
		}
		return m.tasks[min(m.router.recentSelected, len(m.tasks)-1)], true
	}
	return orchestrator.Task{}, false
}

// openRouterTask leaves the request form on the task board with target
// selected, whether it was chosen from the action list or the recent list.
func (m *model) openRouterTask(target orchestrator.Task) {
	m.refreshTasks()
	m.router.open = false
	m.router.focus = routerFocusComposer
	m.historyOffset = 0
	m.detailFocus = false
	// The previous task's activity selection does not apply to the task being
	// opened; the tick only clears it when it observes the selection change.
	m.resetActivitySelection()
	for i, task := range m.tasks {
		if task.ID == target.ID {
			m.selected = i
			break
		}
	}
	m.clamp()
	m.notice.text = "Inspecting " + terminalText(target.Title)
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
	return "Request accepted · " + terminalText(executorLabel(task)) + " · " + string(task.Status) + " · " + routeWorkspace(task) + " — " + terminalText(task.Title)
}

// recentSelectedID is the task under the recent-list selection, so a refresh
// that inserts a newer task can keep the rail on the same task.
func (m model) recentSelectedID() string {
	if m.router.recentSelected < 0 || m.router.recentSelected >= len(m.tasks) {
		return ""
	}
	return m.tasks[m.router.recentSelected].ID
}

// anchorRecentSelection restores the recent-list rail after the task snapshot
// changed, mirroring how refreshTasks keeps the board selection on its task.
// The rail stays on the same task while that task remains in the drawn rows;
// otherwise it clamps to the row that is still drawn.
func (m *model) anchorRecentSelection(id string) {
	if id != "" {
		for i, task := range m.tasks {
			if task.ID == id {
				m.router.recentSelected = i
				break
			}
		}
	}
	m.router.recentSelected = clampRouterSelection(m.router.recentSelected, len(m.tasks))
}

// clearRouterDraft empties the request form's draft and cursor.
func (m *model) clearRouterDraft() {
	m.router.draft, m.router.cursor, m.router.scroll = "", 0, 0
}

// routerKey handles the unified entry point. Typing goes to the composer; Tab
// moves to the action or recent list so a task can be inspected without leaving
// the router. Login key entry is intercepted in Update before the router sees
// any key.
func (m model) routerKey(msg tea.KeyMsg) (model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m.requestQuit(), nil
	case "esc":
		m.router.open = false
		m.router.focus = routerFocusComposer
		m.clampScroll()
		return m, nil
	case "tab", "shift+tab":
		if msg.String() == "tab" && m.router.focus == routerFocusComposer {
			if query, ok := m.routerMentionQuery(); ok {
				m.completeRouterMention(query)
				return m, nil
			}
		}
		delta := 1
		if msg.String() == "shift+tab" {
			delta = -1
		}
		m.router.focus = m.nextRouterFocus(delta)
		m.clampRouter()
		return m, nil
	}
	if m.router.focus != routerFocusComposer {
		switch msg.String() {
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
		return m, nil
	}
	switch msg.String() {
	case "enter":
		text := strings.TrimSpace(m.router.draft)
		if strings.HasPrefix(text, "/") {
			m = m.slashCommand(text)
			m.clearRouterDraft()
			return m, nil
		}
		if text == "" {
			m.notice.text = "Enter a request first."
			return m, nil
		}
		if m.service == nil {
			m.notice.text = "Request form is not connected to a service."
			return m, nil
		}
		// Nothing can be routed without a usable provider, so route the
		// operator to the login menu instead of accepting the draft.
		if !m.service.AnyProviderConfigured() {
			m.login.menu = true
			m.login.index = 0
			m.notice.text = forcedLoginNotice
			return m, nil
		}
		service, workspace := m.service, m.workspace
		return m, func() tea.Msg {
			task, err := service.SubmitRequest(text, workspace)
			return routerResultMsg{task: task, err: err}
		}
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
	text := m.router.draft
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
		m.notice.text = "No registered executor matches @" + query.prefix
		return
	}
	replacement := "@" + agents[0].Name
	m.router.draft = m.router.draft[:query.start] + replacement + m.router.draft[query.end:]
	cursor := query.start + len(replacement)
	if cursor == len(m.router.draft) {
		m.router.draft += " "
		cursor++
	} else if r, _ := utf8.DecodeRuneInString(m.router.draft[cursor:]); !unicode.IsSpace(r) {
		m.router.draft = m.router.draft[:cursor] + " " + m.router.draft[cursor:]
		cursor++
	}
	m.router.cursor = cursor
	m.router.scroll = 0
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

func (m model) routerEditor() editor {
	return editor{text: m.router.draft, cursor: m.router.cursor, scroll: m.router.scroll}
}

func (m *model) setRouterEditor(e editor) {
	m.router.draft = e.text
	m.router.cursor = e.cursor
	m.router.scroll = e.scroll
}

func (m model) routerComposerWidth() int {
	return m.composerWidth()
}

func (m model) routerComposerHeight() int {
	return m.composerHeight()
}

// routerPanel lists the work that needs action and the newest tasks. The
// request box is docked below it by View. When a list has focus the content
// scrolls so the selected row stays visible, like the task sidebar; otherwise
// the newest rows are shown and the rest are clipped.
func (m model) routerPanel(width, height int) string {
	inner := max(1, width-4)
	lines := []string{mutedStyle.Render("Accepted requests become tasks."), ""}
	selectedLine := -1

	action := m.attentionTasks()
	lines = append(lines, accentStyle.Render(fmt.Sprintf("NEEDS ACTION  %02d", len(action))))
	if len(action) == 0 {
		lines = append(lines, mutedStyle.Render("Nothing needs action."))
	} else {
		limit := min(len(action), routerListLimit)
		for i := 0; i < limit; i++ {
			if m.router.focus == routerFocusAttention && i == m.router.selected {
				selectedLine = len(lines)
			}
			lines = append(lines, m.routerAttentionRow(action[i], i, inner))
		}
		if len(action) > limit {
			lines = append(lines, mutedStyle.Render(fmt.Sprintf("+%d more", len(action)-limit)))
		}
	}
	lines = append(lines, "")
	lines = append(lines, accentStyle.Render("RECENT TASKS"))
	if len(m.tasks) == 0 {
		lines = append(lines, mutedStyle.Render("No tasks yet. Submit a request to create one."))
	} else {
		limit := min(len(m.tasks), routerListLimit)
		for i := 0; i < limit; i++ {
			if m.router.focus == routerFocusRecent && i == m.router.recentSelected {
				selectedLine = len(lines)
			}
			lines = append(lines, m.routerRecentRow(m.tasks[i], i, inner))
		}
		if len(m.tasks) > limit {
			lines = append(lines, mutedStyle.Render(fmt.Sprintf("+%d earlier", len(m.tasks)-limit)))
		}
	}
	if interior := height - 2; selectedLine >= 0 && len(lines) > interior && interior > 0 {
		// Scroll the focused list so its selected row stays visible. The window
		// ends at that row, so the section heading above it stays visible
		// whenever the two fit together.
		lines = lines[max(0, selectedLine-interior+1):]
	}
	return panel(strings.Join(lines, "\n"), width, height)
}

func (m model) routerAttentionRow(task orchestrator.Task, index, width int) string {
	title := singleLine(task.Title)
	plain := "● " + strings.ToUpper(task.Stage()) + "  " + title
	if m.router.focus == routerFocusAttention && index == m.router.selected {
		return selectionRow(plain, width)
	}
	return fit(m.taskLabel(task)+"  "+workspaceBadgeFor(task).glyphOnly()+"  "+titleStyle.Render(title), width, 1)
}

func (m model) routerRecentRow(task orchestrator.Task, index, width int) string {
	stamp := task.CreatedAt.Local().Format("15:04")
	title := singleLine(task.Title)
	executor := terminalText(executorLabel(task))
	if m.router.focus == routerFocusRecent && index == m.router.recentSelected {
		_, label, _ := m.taskState(task)
		plain := stamp + "  ● " + label + "  " + executor + "  " + title
		return selectionRow(plain, width)
	}
	row := mutedStyle.Render(stamp) + "  " + m.taskLabel(task) + "  " +
		titleStyle.Render(executor) + mutedStyle.Render(" · ") + workspaceBadgeFor(task).render() + "  " + title
	return fit(row, width, 1)
}
