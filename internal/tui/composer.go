package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"orchestrator/internal/orchestrator"
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

// The target is captured when the composer opens. Task ordering can change
// while the operator is typing.
type composer struct {
	kind         inputKind
	target       string
	reviewCommit string
	text         string
}

func editText(text string, msg tea.KeyMsg) string {
	switch msg.Type {
	case tea.KeyBackspace, tea.KeyCtrlH:
		_, n := utf8.DecodeLastRuneInString(text)
		return text[:len(text)-n]
	case tea.KeyRunes:
		return text + string(msg.Runes)
	case tea.KeySpace:
		return text + " "
	}
	return text
}

func (m model) composerKey(msg tea.KeyMsg) (model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyEsc:
		m.input = composer{}
	case tea.KeyEnter:
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
		m.input.text = editText(m.input.text, msg)
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
