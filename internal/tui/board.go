package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	"pilharmonic/internal/orchestrator"
)

// boardActions are the task board's keys that do not need a selected task.
var boardActions = map[string]func(*model) tea.Cmd{
	"q":      func(m *model) tea.Cmd { *m = m.requestQuit(); return nil },
	"ctrl+c": func(m *model) tea.Cmd { *m = m.requestQuit(); return nil },
	"enter": func(m *model) tea.Cmd {
		m.detailFocus = !m.detailFocus && !m.fullscreen
		m.fullscreen = false
		return nil
	},
	"z":   func(m *model) tea.Cmd { m.fullscreen = !m.fullscreen; return nil },
	"esc": func(m *model) tea.Cmd { m.fullscreen, m.detailFocus = false, false; return nil },
	"t": func(m *model) tea.Cmd {
		m.activity.open = !m.activity.open
		m.resetActivitySelection()
		m.detailFocus = true
		m.historyOffset = 0
		if m.activity.open {
			m.historyOffset = m.maxScroll()
		}
		return nil
	},
	"T": func(m *model) tea.Cmd { m.thinking = !m.thinking; return nil },
	"tab": func(m *model) tea.Cmd {
		m.activity.open, m.activity.expanded = false, false
		m.history = !m.history
		m.historyOffset = 0
		return nil
	},
	"!": func(m *model) tea.Cmd {
		if !m.jumpAttention() {
			m.notice.text = "No tasks need action."
		}
		return nil
	},
	"L": func(m *model) tea.Cmd { m.login.menu, m.login.index = true, 0; return nil },
	"m": func(m *model) tea.Cmd { m.models = modelMenuState{menu: true}; return nil },
	"/": func(m *model) tea.Cmd {
		m.router.open = true
		m.router.focus = routerFocusComposer
		m.router.cursor = len(m.router.text)
		m.router.scroll = 0
		m.notice.text = ""
		m.clampRouter()
		return nil
	},
	"n":    func(m *model) tea.Cmd { m.openComposer(newTaskInput, "", ""); return nil },
	"[":    func(m *model) tea.Cmd { m.scrollDetail(1); return nil },
	"]":    func(m *model) tea.Cmd { m.scrollDetail(-1); return nil },
	"home": func(m *model) tea.Cmd { m.historyOffset = m.maxScroll(); return nil },
	"end":  func(m *model) tea.Cmd { m.historyOffset = 0; return nil },
	"d":    func(m *model) tea.Cmd { m.prepareCleanup(deleteTask); return nil },
	"D":    func(m *model) tea.Cmd { m.prepareCleanup(deleteCompleted); return nil },
	"x":    func(m *model) tea.Cmd { m.prepareCleanup(discardTask); return nil },
	"X":    func(m *model) tea.Cmd { m.prepareCleanup(discardMerged); return nil },
}

// taskActions are the board keys that act on the selected task. They do
// nothing while the board is empty.
var taskActions = map[string]func(*model, orchestrator.Task) tea.Cmd{
	"A": func(m *model, t orchestrator.Task) tea.Cmd {
		if configurable(t, "Choose an agent before starting.", &m.notice.text) {
			openPicker(&m.agentPicker, t.ID, m.service.ListAgents(), func(a orchestrator.AgentInfo) bool { return a.Name == t.Agent })
		}
		return nil
	},
	"P": func(m *model, t orchestrator.Task) tea.Cmd {
		if configurable(t, "Choose a profile before starting.", &m.notice.text) {
			openPicker(&m.profilePicker, t.ID, m.service.ListProfiles(), func(p orchestrator.Profile) bool { return p.Name == t.ProfileName })
		}
		return nil
	},
	"w": func(m *model, t orchestrator.Task) tea.Cmd {
		if m.isolationBusy || !configurable(t, "Choose isolation before starting.", &m.notice.text) {
			return nil
		}
		m.isolationBusy = true
		m.notice.text = "Checking workspace isolation…"
		service, enabled := m.service, !t.Isolated
		return func() tea.Msg {
			notice := "Isolation off: task uses the source workspace."
			if enabled {
				notice = "Isolation enabled: starts from committed HEAD; uncommitted files are excluded."
			}
			return isolationResultMsg(resultFor(notice, service.SetTaskIsolation(t.ID, enabled)))
		}
	},
	"a": func(m *model, t orchestrator.Task) tea.Cmd { m.openComposer(relatedTaskInput, t.ID, ""); return nil },
	"b": func(m *model, t orchestrator.Task) tea.Cmd {
		if t.Status != orchestrator.Running {
			m.notice.text = "Spawn subagents only from a running task."
			return nil
		}
		m.openComposer(subagentInput, t.ID, "Enter a self-contained mandate for the subagent.")
		return nil
	},
	"f": func(m *model, t orchestrator.Task) tea.Cmd { m.openComposer(threadInput, t.ID, ""); return nil },
	"F": func(m *model, t orchestrator.Task) tea.Cmd {
		notice := "Follow-up requeues the task; press r to start."
		if t.Status == orchestrator.Queued || t.Status == orchestrator.Running {
			notice = "Context does not expand the mandate."
		}
		m.openComposer(followUpInput, t.ID, notice)
		return nil
	},
	"p": func(m *model, t orchestrator.Task) tea.Cmd {
		m.openComposer(reportInput, t.ID, "Report: kind and message (progress, waiting, blocked, ready, failed)")
		return nil
	},
	"s": func(m *model, t orchestrator.Task) tea.Cmd {
		m.notice.text = resultFor("Context subscription updated.", m.service.SetContextSubscription(t.ID, t.ContextPaused)).text()
		return nil
	},
	"r": func(m *model, t orchestrator.Task) tea.Cmd {
		return serviceCmd("Execution requested; queued tasks start when capacity frees.", func(s Service) error { return s.Start(context.Background(), t.ID) }, m.service)
	},
	"R": func(m *model, t orchestrator.Task) tea.Cmd {
		return serviceCmd("Task queued for retry; press r to start.", func(s Service) error { return s.Retry(t.ID) }, m.service)
	},
	"c": func(m *model, t orchestrator.Task) tea.Cmd {
		return serviceCmd("Cancellation requested.", func(s Service) error { return s.Cancel(t.ID) }, m.service)
	},
	"v": func(m *model, t orchestrator.Task) tea.Cmd {
		if !t.AwaitingReview() {
			m.notice.text = "No worktree commit awaits review."
			return nil
		}
		commit := t.ReviewCommit
		return serviceCmd("Review approved for commit "+commit+"; merge scheduled.", func(s Service) error { return s.ApproveReview(t.ID, commit, "") }, m.service)
	},
	"V": func(m *model, t orchestrator.Task) tea.Cmd {
		if !t.AwaitingReview() {
			m.notice.text = "No worktree commit awaits review."
			return nil
		}
		m.openComposer(rejectReviewInput, t.ID, "Enter a reason to reject; the worktree is kept for F follow-up.")
		m.input.reviewCommit = t.ReviewCommit
		return nil
	},
}

func (m model) boardKey(key string) (model, tea.Cmd) {
	var cmd tea.Cmd
	if action, ok := boardActions[key]; ok {
		cmd = action(&m)
	} else if action, ok := taskActions[key]; ok {
		if t, ok := m.selectedTask(); ok {
			cmd = action(&m, t)
		}
	}
	m.refreshTasks()
	m.clampScroll()
	return m, cmd
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
