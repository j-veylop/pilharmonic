package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"pilharmonic/internal/orchestrator"
)

type inputKind string

const (
	newTaskInput      inputKind = "NEW TASK"
	relatedTaskInput  inputKind = "RELATED TASK"
	subagentInput     inputKind = "SUBAGENT TASK"
	threadInput       inputKind = "THREAD CONTEXT"
	followUpInput     inputKind = "FOLLOW UP"
	reportInput       inputKind = "REPORT (kind message)"
	rejectReviewInput inputKind = "REJECT REVIEW"
)

const composerMaxLines = 6

// The target is captured when the composer opens. Task ordering can change
// while the operator is typing.
type composer struct {
	kind         inputKind
	target       string
	reviewCommit string
	text         string
	cursor       int
	scroll       int
}

func (c composer) editorState() editor {
	return editor{text: c.text, cursor: c.cursor, scroll: c.scroll}
}

func (c *composer) setEditorState(e editor) {
	c.text, c.cursor, c.scroll = e.text, e.cursor, e.scroll
}

func (c composer) viewLines(width, height int) []string {
	return c.editorState().viewLines(width, height)
}

// editText keeps the old single-value editing helper for callers that only
// need an append-only draft. Composers use editor so wrapped lines and cursor
// movement remain possible.
func editText(text string, msg tea.KeyMsg) string {
	e := editor{text: text, cursor: len(text)}
	switch msg.Type {
	case tea.KeyBackspace, tea.KeyCtrlH:
		e.backspace()
	case tea.KeyRunes:
		e.insert(string(msg.Runes))
	case tea.KeySpace:
		e.insert(" ")
	}
	return e.text
}

func (m model) composerWidth() int {
	width := m.width
	if width <= 0 {
		width = 80
	}
	return max(1, width-lipgloss.Width(string(m.input.kind)+"  › ")-1)
}

func (m model) composerHeight() int {
	height := m.height
	if height <= 0 {
		height = 24
	}
	return max(2, min(composerMaxLines, height/4))
}

func (m model) composerKey(msg tea.KeyMsg) (model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.input = composer{}
	case "enter":
		prompt := strings.TrimSpace(m.input.text)
		if prompt == "" {
			m.notice = "Enter a task first."
			return m, nil
		}
		notice, err := m.submitInput(prompt)
		m.notice = notice
		if err != nil {
			m.notice = err.Error()
		}
		m.input = composer{}
		m.tasks = m.service.ListTasks()
		m.clamp()
	default:
		e := m.input.editorState()
		e.handleKey(msg, m.composerWidth(), m.composerHeight())
		m.input.setEditorState(e)
	}
	return m, nil
}

func (m model) submitInput(prompt string) (string, error) {
	id := m.input.target
	switch m.input.kind {
	case rejectReviewInput:
		return "Review rejected; worktree unchanged.", m.service.RejectReview(id, m.input.reviewCommit, prompt)
	case reportInput:
		kind, message, _ := strings.Cut(prompt, " ")
		return "Report recorded.", m.service.RecordReport(id, kind, message)
	case threadInput:
		return "Context recorded; delivery status is in history.", m.service.AddManualThreadContext(id, prompt)
	case followUpInput:
		requeued, err := m.service.FollowUp(id, prompt)
		if requeued {
			return "Follow-up recorded; requeued. Press r to start.", err
		}
		return "Follow-up recorded; delivery status is in history.", err
	case subagentInput:
		task, err := m.service.SpawnSubagent(id, "", prompt, "")
		return "Subagent started: " + task.ID, err
	case newTaskInput, relatedTaskInput:
		title := prompt
		if _, request, explicit := orchestrator.SplitAgentMention(prompt); explicit {
			title = request
		}
		if runes := []rune(title); len(runes) > 60 {
			title = string(runes[:57]) + "..."
		}
		var task orchestrator.Task
		var err error
		if m.input.kind == relatedTaskInput {
			task, err = m.service.CreateRelatedTask(id, title, prompt, m.workspace)
		} else {
			task, err = m.service.CreateTask(title, prompt, m.workspace)
		}
		return "Task queued: " + task.ID, err
	default:
		return "", fmt.Errorf("unknown input kind %q", m.input.kind)
	}
}
