package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	"pilharmonic/internal/orchestrator"
)

// binding is one board key: its footer label, its help row and its handler.
type binding struct {
	key, hint, help, topic string
	run                    func(*model) tea.Cmd
}

// boardKeys is the board's one key table, in help order. Dispatch, the help
// overlay and the footer all read it. A binding without help is an alias.
var boardKeys []binding

// init fills boardKeys because its handlers render the footer, which reads it.
func init() {
	focusRequest := func(m *model) tea.Cmd {
		m.router.open = true
		m.router.Cursor = len(m.router.Text)
		m.router.Scroll = 0
		m.notice.text = ""
		return nil
	}
	boardKeys = []binding{
		{"enter", "details", "Open the selected task; in details, keep it open (Esc returns); in ACTIVITY, expand / collapse the call", "Navigation", func(m *model) tea.Cmd {
			if !m.detailFocus && !m.fullscreen {
				m.detailFocus = true
				m.clampScroll()
			}
			return nil
		}},
		{"z", "full width", "Toggle full-width details", "Navigation", func(m *model) tea.Cmd { m.fullscreen = !m.fullscreen; return nil }},
		{"B", "sidebar", "Toggle the task sidebar", "Navigation", func(m *model) tea.Cmd { m.sidebarHidden = !m.sidebarHidden; return nil }},
		{"E", "done", "Expand or fold completed tasks while active work is on the board", "Navigation", func(m *model) tea.Cmd { m.toggleDone(); return nil }},
		{"[", "prev subtask", "Select the previous subtask in the detailed view", "Navigation", func(m *model) tea.Cmd { m.cycleSubtask(-1); return nil }},
		{"]", "next subtask", "Select the next subtask in the detailed view", "Navigation", func(m *model) tea.Cmd { m.cycleSubtask(1); return nil }},
		{"esc", "leave", "Leave the input, return to the task list, or collapse the expanded call", "Navigation", func(m *model) tea.Cmd { m.fullscreen, m.detailFocus = false, false; return nil }},
		{"!", "action", "Next task needing action", "Navigation", func(m *model) tea.Cmd {
			if !m.jumpAttention() {
				m.notice.text = "No tasks need action."
			}
			return nil
		}},
		{"i", "input", "Focus the request box: Enter routes and starts, Ctrl-s holds the task for setup; Esc leaves it visible", "Tasks", focusRequest},
		{"o", "setup", "Set executor, profile and isolation of a held task", "Tasks", onTask(func(m *model, t orchestrator.Task) tea.Cmd {
			if configurable(t, "Set up a task only while it is held: queued and not started.", &m.notice.text) {
				m.setup = setupState{target: t.ID}
			}
			return nil
		})},
		{"r", "run", "Start a held task; retry and start a failed one", "Tasks", onTask(func(m *model, t orchestrator.Task) tea.Cmd {
			if !retryable(t) {
				return serviceCmd("Execution requested; queued tasks start when capacity frees.", func(s Service) error { return s.Start(context.Background(), t.ID) }, m.service)
			}
			return serviceCmd("Retry requested; queued tasks start when capacity frees.", func(s Service) error {
				if err := s.Retry(t.ID); err != nil {
					return err
				}
				return s.Start(context.Background(), t.ID)
			}, m.service)
		})},
		{"c", "cancel", "Cancel task", "Tasks", onTask(func(m *model, t orchestrator.Task) tea.Cmd {
			return serviceCmd("Cancellation requested.", func(s Service) error { return s.Cancel(t.ID) }, m.service)
		})},
		{"v", "review", "Review the prepared commit: y approves + merges, n rejects", "Tasks", onTask(func(m *model, t orchestrator.Task) tea.Cmd {
			if !t.AwaitingReview() {
				m.notice.text = "No worktree commit awaits review."
				return nil
			}
			m.reviewDiff = reviewDiffState{target: t.ID, commit: t.ReviewCommit, title: t.Title, loading: true}
			m.historyOffset = 0
			m.notice.text = ""
			return m.loadReviewDiff(t.ID, t.ReviewCommit, t.Title)
		})},
		{"f", "follow up", "Continue the conversation; a finished task resumes immediately", "Tasks", onTask(func(m *model, t orchestrator.Task) tea.Cmd {
			notice := "Follow-up continues the conversation from the existing output."
			if t.Status == orchestrator.Queued || t.Status == orchestrator.Running {
				notice = "Context joins the running conversation; the mandate does not expand."
			}
			m.openComposer(followUpInput, t.ID, notice)
			return nil
		})},
		{"a", "related", "Related task in the selected thread", "Tasks", onTask(func(m *model, t orchestrator.Task) tea.Cmd {
			m.openComposer(relatedTaskInput, t.ID, "")
			return nil
		})},
		{"b", "subagent", "Spawn a subagent from a running task", "Tasks", onTask(func(m *model, t orchestrator.Task) tea.Cmd {
			if t.Status != orchestrator.Running {
				m.notice.text = "Spawn subagents only from a running task."
				return nil
			}
			m.openComposer(subagentInput, t.ID, "Enter a self-contained mandate for the subagent.")
			return nil
		})},
		{"m", "message", "Send context to thread subscribers", "Tasks", onTask(func(m *model, t orchestrator.Task) tea.Cmd {
			m.openComposer(threadInput, t.ID, "")
			return nil
		})},
		{"s", "subscribe", "Pause / resume thread subscription", "Tasks", onTask(func(m *model, t orchestrator.Task) tea.Cmd {
			m.notice.text = resultFor("Context subscription updated.", m.service.SetContextSubscription(t.ID, t.ContextPaused)).text()
			return nil
		})},
		{"p", "report", "Report: kind + message progress, waiting, blocked, ready, failed", "Tasks", onTask(func(m *model, t orchestrator.Task) tea.Cmd {
			m.openComposer(reportInput, t.ID, "Report: kind and message (progress, waiting, blocked, ready, failed)")
			return nil
		})},
		{"d", "delete", "Delete inactive task + worktree (confirm y)", "Tasks", func(m *model) tea.Cmd { m.prepareCleanup(deleteTask); return nil }},
		{"D", "delete completed", "Delete completed tasks + worktrees (confirm y)", "Tasks", func(m *model) tea.Cmd { m.prepareCleanup(deleteCompleted); return nil }},
		{"x", "discard", "Discard selected merged worktree (confirm y)", "Tasks", func(m *model) tea.Cmd { m.prepareCleanup(discardTask); return nil }},
		{"X", "discard merged", "Discard all merged worktrees (confirm y)", "Tasks", func(m *model) tea.Cmd { m.prepareCleanup(discardMerged); return nil }},
		{"ctrl+x", "force remove", "Permanently remove selected retained worktree including dirty/unmerged work (confirm y)", "Tasks", onTask(func(m *model, t orchestrator.Task) tea.Cmd { m.prepareCleanup(forceDiscard); return nil })},
		{"tab", "output/info/activity", "Next detail tab: OUTPUT, INFO, ACTIVITY", "Details", func(m *model) tea.Cmd { m.showTab((m.tab + 1) % 3); return nil }},
		{"shift+tab", "previous tab", "Previous detail tab", "Details", func(m *model) tea.Cmd { m.showTab((m.tab + 2) % 3); return nil }},
		{"t", "thinking", "Show / hide thinking blocks", "Details", func(m *model) tea.Cmd { m.thinking = !m.thinking; return nil }},
		{"e", "filter", "ACTIVITY filter: all calls / latest attempt / errors", "Details", func(m *model) tea.Cmd {
			if m.tab != activityTab {
				m.notice.text = "The filter applies to the ACTIVITY tab."
				return nil
			}
			m.activity.filter = (m.activity.filter + 1) % 3
			m.resetActivitySelection()
			m.historyOffset = 0
			return nil
		}},
		{"q", "quit", "Quit; a dialog asks y / Enter to confirm, n / Esc to stay, h/l switch buttons (also Ctrl-c)", "General", func(m *model) tea.Cmd { *m = m.requestQuit(); return nil }},
		{"ctrl+c", "quit", "", "General", func(m *model) tea.Cmd { *m = m.requestQuit(); return nil }},
	}
}

// onTask adapts an action on the selected task; it does nothing while the
// board is empty.
func onTask(run func(*model, orchestrator.Task) tea.Cmd) func(*model) tea.Cmd {
	return func(m *model) tea.Cmd {
		if t, ok := m.selectedTask(); ok {
			return run(m, t)
		}
		return nil
	}
}

func (m model) boardKey(key string) (model, tea.Cmd) {
	var cmd tea.Cmd
	for _, b := range boardKeys {
		if b.key == key {
			cmd = b.run(&m)
			break
		}
	}
	m.refreshTasks()
	m.clampScroll()
	return m, cmd
}

// keyHint is a binding's footer label, such as "q quit".
func keyHint(key string) string {
	for _, b := range boardKeys {
		if b.key == key {
			return b.key + " " + b.hint
		}
	}
	return key
}

// showTab switches the detail tab; ACTIVITY opens at the top of its list.
func (m *model) showTab(tab detailTab) {
	m.tab = tab
	m.resetActivitySelection()
	m.historyOffset = 0
	if tab == activityTab {
		m.historyOffset = m.maxScroll()
	}
}

// configurable reports whether t can still be configured, setting the refusal
// notice when it cannot.
func configurable(t orchestrator.Task, refusal string, notice *string) bool {
	if t.Status != orchestrator.Queued || t.ScheduleRequested {
		*notice = refusal
		return false
	}
	*notice = ""
	return true
}

// openComposer captures the target task when the composer opens.
func (m *model) openComposer(kind inputKind, target, notice string) {
	m.input = composer{kind: kind, target: target}
	m.router.open = false
	m.notice.text = notice
}

// serviceCmd runs a mutating service call off the update loop.
func serviceCmd(notice string, call func(Service) error, service Service) tea.Cmd {
	return func() tea.Msg { return resultFor(notice, call(service)) }
}

func resultFor(notice string, err error) actionResultMsg {
	if err != nil {
		return actionResultMsg{err: err}
	}
	return actionResultMsg{notice: notice}
}

// text shows err after any notice that still applies despite it.
func (r actionResultMsg) text() string {
	switch {
	case r.err == nil:
		return r.notice
	case r.notice == "":
		return r.err.Error()
	}
	return r.notice + " · " + r.err.Error()
}
