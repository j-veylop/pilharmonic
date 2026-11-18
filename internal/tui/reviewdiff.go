package tui

import (
	"errors"
	"os/exec"
	"strings"

	"pilharmonic/internal/orchestrator"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func (m model) reviewDiffView(width, height int) string {
	r := m.reviewDiff
	inner := max(1, width-4)
	lines := []string{m.detailTabRow(m.selectedTaskOrZero(), inner), fit("REVIEW  "+singleLine(r.title)+"  ·  "+shortID(r.commit), inner, 1)}
	switch {
	case r.loading:
		lines = append(lines, mutedStyle.Render("Loading reviewed commit diff…"))
	case r.diff == "" && m.notice.text != "":
		lines = append(lines, mutedStyle.Render(singleLine(m.notice.text)))
	case r.diff == "":
		lines = append(lines, mutedStyle.Render("No changes in this commit."))
	default:
		for _, line := range strings.Split(r.diff, "\n") {
			line = terminalText(line)
			switch {
			case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
				line = diffAddedStyle.Render(line)
			case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
				line = diffRemovedStyle.Render(line)
			case strings.HasPrefix(line, "@@"), strings.HasPrefix(line, "diff "), strings.HasPrefix(line, "index "), strings.HasPrefix(line, "---"), strings.HasPrefix(line, "+++"):
				line = diffHunkStyle.Render(line)
			}
			lines = append(lines, line)
		}
	}
	content := strings.Join(lines, "\n")
	visible := max(1, height)
	all := strings.Split(content, "\n")
	end := max(0, len(all)-m.historyOffset)
	start := max(0, end-visible)
	if end-start > visible {
		start = end - visible
	}
	content = strings.Join(all[start:end], "\n")
	return lipgloss.NewStyle().Width(width).Height(height).Padding(0, 2).Render(content)
}

func (m model) reviewTask() (orchestrator.Task, bool) {
	for _, task := range m.tasks {
		if task.ID == m.reviewDiff.target {
			return task, true
		}
	}
	return orchestrator.Task{}, false
}

func openReviewTool(key string, task orchestrator.Task) tea.Cmd {
	dir, refusal := worktreeDir(task)
	if refusal != "" {
		return func() tea.Msg { return actionResultMsg{err: errors.New(refusal)} }
	}
	name := "nvim"
	if key == "ctrl+g" {
		name = "lazygit"
	}
	cmd := exec.Command(name)
	cmd.Dir = dir
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			return actionResultMsg{err: err}
		}
		return nil
	})
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func (m model) reviewDiffKey(msg tea.KeyMsg) (model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+o" || key == "ctrl+g" || key == "ctrl+t" {
		t, ok := m.reviewTask()
		if !ok {
			m.notice.text = "Review task is no longer available."
			return m, nil
		}
		if key == "ctrl+t" {
			return m.worktreeTerminal(t)
		}
		return m, openReviewTool(key, t)
	}
	switch key {
	case "esc", "q":
		m.reviewDiff = reviewDiffState{}
		m.historyOffset = 0
	case "y":
		r := m.reviewDiff
		if r.loading {
			return m, nil
		}
		m.reviewDiff = reviewDiffState{}
		return m, serviceCmd("Review approved for commit "+r.commit+"; merge scheduled.", func(s Service) error { return s.ApproveReview(r.target, r.commit, "") }, m.service)
	case "n":
		r := m.reviewDiff
		m.reviewDiff = reviewDiffState{}
		m.openComposer(rejectReviewInput, r.target, "Enter a reason to reject; the worktree is kept for f follow-up.")
		m.input.reviewCommit = r.commit
	case "j", "down", "pgdown", "ctrl+d":
		m.historyOffset = max(0, m.historyOffset-max(1, m.detailPage()))
	case "k", "up", "pgup", "ctrl+u":
		m.historyOffset = min(max(0, len(strings.Split(m.reviewDiff.diff, "\n"))-1), m.historyOffset+max(1, m.detailPage()))
	case "g":
		m.historyOffset = m.maxScroll()
	case "G":
		m.historyOffset = 0
	}
	return m, nil
}

func (m model) acceptReviewDiff(msg reviewDiffMsg) model {
	if m.reviewDiff.target != msg.target || m.reviewDiff.commit != msg.commit {
		return m
	}
	m.reviewDiff.loading = false
	if msg.err != nil {
		m.notice.text = "Could not load review diff: " + msg.err.Error()
		m.reviewDiff.diff = ""
	} else {
		m.reviewDiff.diff = msg.diff
		m.notice.text = ""
	}
	return m
}
