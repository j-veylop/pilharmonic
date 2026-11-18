package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/j-veylop/pilharmonic/internal/orchestrator"
	"github.com/j-veylop/pilharmonic/internal/tui/editor"
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

func routeNotice(task orchestrator.Task) string {
	workspace := "shared workspace"
	if task.Isolated {
		workspace = "isolated worktree"
	} else if task.ParentID != "" {
		workspace = "inherited workspace"
	}
	return "Request accepted · " + terminalText(executorLabel(task)) + " · " + string(task.Status) + " · " + workspace + " — " + terminalText(task.Title)
}

func (m *model) clearRouterDraft() {
	m.router.Editor = editor.Editor{}
	m.router.history = m.router.history.reset()
}

// requestHistory remembers submitted requests so Up and Down can recall them,
// like a shell. It lives only for the session; durable history is the tasks.
// index == len(entries) means the operator is not browsing, and draft holds
// the unsent text that browsing replaced.
type requestHistory struct {
	entries []string
	index   int
	draft   string
}

// record appends a submitted request, skipping an immediate repeat.
func (h requestHistory) record(text string) requestHistory {
	if n := len(h.entries); n == 0 || h.entries[n-1] != text {
		h.entries = append(h.entries[:n:n], text)
	}
	return h.reset()
}

func (h requestHistory) reset() requestHistory {
	h.index, h.draft = len(h.entries), ""
	return h
}

// browseHistory moves through submitted requests and reports whether it did.
// It only acts when the cursor cannot move further in the draft, so Up and
// Down still edit multi-line text.
func (m *model) browseHistory(delta int) bool {
	h, e := m.router.history, &m.router.Editor
	if h.index > len(h.entries) {
		h = h.reset()
	}
	atEdge := e.OnFirstLine(m.composerWidth())
	if delta > 0 {
		atEdge = e.OnLastLine(m.composerWidth())
	}
	target := h.index + delta
	if !atEdge || target < 0 || target > len(h.entries) {
		return false
	}
	if h.index == len(h.entries) {
		h.draft = e.Text
	}
	text := h.draft
	if target < len(h.entries) {
		text = h.entries[target]
	}
	h.index = target
	e.Text, e.Cursor = text, len(text)
	e.HandleKey(tea.KeyMsg{Type: tea.KeyCtrlEnd}, m.composerWidth(), m.composerHeight())
	m.router.history = h
	return true
}

// routerKey handles the request box. Typing goes to the draft; Tab completes a
// leading @executor mention; Up and Down recall earlier requests from the
// draft's first and last line.
func (m model) routerKey(msg tea.KeyMsg) (model, tea.Cmd) {
	switch key := msg.String(); key {
	case "ctrl+c":
		return m.requestQuit(), nil
	case "esc":
		m.router.open = false
		m.clampScroll()
	case "ctrl+o":
		return m.openExternalEditor(true)
	case "tab":
		m.completeRouterMention()
	case "enter", "ctrl+s":
		return m.submitRequest(key == "ctrl+s")
	case "up", "down":
		delta := 1
		if key == "up" {
			delta = -1
		}
		if !m.browseHistory(delta) {
			m.router.HandleKey(msg, m.composerWidth(), m.composerHeight())
		}
	default:
		m.router.HandleKey(msg, m.composerWidth(), m.composerHeight())
	}
	return m, nil
}

// chatFollowTarget is the selected task while the detailed view owns the
// keyboard. The main view's request box starts a new task instead.
func (m model) chatFollowTarget() (orchestrator.Task, bool) {
	if !(m.detailFocus || m.fullscreen) {
		return orchestrator.Task{}, false
	}
	return m.selectedTask()
}

// submitRequest dispatches a slash command or submits the draft off the update
// loop: routed and started, or held queued for setup. Nothing can run without
// a provider, so login comes first.
func (m model) submitRequest(hold bool) (model, tea.Cmd) {
	text := strings.TrimSpace(m.router.Text)
	switch {
	case strings.HasPrefix(text, "/"):
		m.router.history = m.router.history.record(text)
		m = m.slashCommand(text)
		m.clearRouterDraft()
		cmd := m.takePendingCommand()
		return m, cmd
	case text == "":
		m.notice.text = "Enter a request first."
		return m, nil
	case !m.service.AnyProviderConfigured():
		m.login.menu, m.login.index = true, 0
		m.notice.text = forcedLoginNotice
		return m, nil
	}
	if target, ok := m.chatFollowTarget(); ok {
		if hold {
			m.notice.text = "Ctrl-s holds new tasks; this message continues the selected task."
			return m, nil
		}
		m.router.history = m.router.history.record(text)
		service, id, attachments := m.service, target.ID, append([]string(nil), m.attachments...)
		return m, func() tea.Msg {
			prompt, err := composeRequest(text, attachments)
			if err != nil {
				return routerResultMsg{err: err}
			}
			notice, err := sendFollowUp(service, id, prompt)
			if notice == "" {
				return routerResultMsg{err: err}
			}
			return routerResultMsg{task: target, notice: notice, err: err}
		}
	}
	m.router.history = m.router.history.record(text)
	service, workspace, attachments := m.service, m.workspace, append([]string(nil), m.attachments...)
	if hold {
		return m, func() tea.Msg {
			prompt, err := composeRequest(text, attachments)
			if err != nil {
				return routerResultMsg{err: err}
			}
			task, err := service.CreateTask(orchestrator.DerivedTitle(prompt), prompt, workspace)
			return routerResultMsg{task: task, notice: "Task held: " + task.ID + "; o to set up, r to start.", err: err}
		}
	}
	return m, func() tea.Msg {
		prompt, err := composeRequest(text, attachments)
		if err != nil {
			return routerResultMsg{err: err}
		}
		task, err := service.SubmitRequest(prompt, workspace)
		return routerResultMsg{task: task, notice: routeNotice(task), err: err}
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
	text := m.router.Text
	cursor := min(max(0, m.router.Cursor), len(text))
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
	m.router.Text = m.router.Text[:query.start] + replacement + m.router.Text[query.end:]
	cursor := query.start + len(replacement)
	if cursor == len(m.router.Text) {
		m.router.Text += " "
		cursor++
	} else if r, _ := utf8.DecodeRuneInString(m.router.Text[cursor:]); !unicode.IsSpace(r) {
		m.router.Text = m.router.Text[:cursor] + " " + m.router.Text[cursor:]
		cursor++
	}
	m.router.Cursor = cursor
	m.router.Scroll = 0
	return true
}

// routerMentionHintLines shows available executors while the leading mention is
// being edited. The composer keeps the first matching name for Tab completion.
func (m model) routerMentionHintLines(width int) []string {
	if m.login.provider != "" {
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
