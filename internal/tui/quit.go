package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"pilharmonic/internal/orchestrator"
)

// Every exit key on every surface opens one confirmation modal, so a stray q
// or Ctrl-c never ends a run with work in flight. Only an explicit yes quits;
// backing out leaves the screen underneath exactly as it was.

const (
	quitModalWidth  = 52
	quitModalHeight = 12
)

func (m model) requestQuit() model {
	m.quitConfirm = true
	m.notice.text = ""
	m.vimG, m.vimCount = false, 0
	return m
}

// quitKey answers the dialog; a paste never does.
func (m model) quitKey(msg tea.KeyMsg) (model, tea.Cmd) {
	if msg.Paste {
		return m, nil
	}
	switch msg.String() {
	case "y", "Y", "enter", "ctrl+c":
		return m, tea.Quit
	case "n", "N", "esc", "q":
		m.quitConfirm = false
		m.notice.text = "Quit canceled."
	}
	return m, nil
}

// quitCountLine states how many attempts quitting cancels. It counts process
// status, not the displayed stage: a task waiting for subagents is still live.
func (m model) quitCountLine() string {
	running := 0
	for _, task := range m.tasks {
		if task.Status == orchestrator.Running {
			running++
		}
	}
	switch running {
	case 0:
		return "Nothing is running."
	case 1:
		return "1 running task stops when you quit."
	}
	return fmt.Sprintf("%d running tasks stop when you quit.", running)
}

// quitModal lays out the dialog for a w by h terminal, dropping the least
// important line before the answer row when the frame is short. It returns no
// content when no usable frame fits; the resize prompt then names the answers.
func (m model) quitModal(w, h int) (mw, mh int, content []string) {
	inner := min(quitModalWidth-4, w-8)
	ceiling := min(quitModalHeight, h-2)
	if inner < 12 || ceiling < 4 {
		return 0, 0, nil
	}
	wrap := func(text string) []string {
		rows := strings.Split(ansi.Wordwrap(text, inner, ""), "\n")
		for i, row := range rows {
			rows[i] = mutedStyle.Render(row)
		}
		return rows
	}
	question, count, history := wrap("Leave pilharmonic?"), wrap(m.quitCountLine()), wrap("History and worktrees are kept.")
	bodies := [][][]string{{question, count, history}, {question, count}, {question, history}, {count}, nil}
	for _, groups := range bodies {
		candidate := []string{accentStyle.Render("QUIT")}
		for _, group := range groups {
			candidate = append(candidate, group...)
		}
		candidate = append(candidate, "", fitHints(inner, "y quit", "n/esc stay"))
		if len(candidate) <= ceiling-2 {
			return inner + 4, len(candidate) + 2, candidate
		}
	}
	return 0, 0, nil
}

func (m model) quitView(background string, w, h int) string {
	mw, mh, content := m.quitModal(w, h)
	return modalView(background, w, h, mw, mh, content)
}
