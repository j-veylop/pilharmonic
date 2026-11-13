package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"pilharmonic/internal/orchestrator"
)

// Quitting ends the process, so every exit key on every surface asks first.
// The dialog is a modal drawn over the current screen: the request is answered
// only by an explicit yes, and backing out leaves the screen underneath, its
// draft and its selection, exactly as it was.
//
// The keys are the same everywhere, so the confirmation needs no per-surface
// variant: q and Ctrl-c open it, y, Enter or a second Ctrl-c quits, and n, Esc
// or q returns.

const (
	// quitModalWidth bounds the frame on a generous terminal.
	quitModalWidth = 52
	// quitModalHeight bounds the frame. It leaves room for the title, the
	// question, the count and the reassurance once those wrap on a narrow
	// terminal, plus a gap and the answer row.
	quitModalHeight = 12
)

// requestQuit arms the confirmation above whatever is on screen. Routing every
// exit request through one modal keeps a stray q or Ctrl-c from ending a run
// with work in flight; the operator sees what they are about to stop first.
func (m model) requestQuit() model {
	m.quitConfirm = true
	m.notice = ""
	m.vimG, m.vimCount = false, 0
	return m
}

// quitKey answers the dialog. Only an explicit yes returns a quit command;
// every other key, including a paste, leaves the app running.
func (m model) quitKey(msg tea.KeyMsg) (model, tea.Cmd) {
	if msg.Paste {
		return m, nil
	}
	switch msg.String() {
	case "y", "Y", "enter", "ctrl+c":
		return m, tea.Quit
	case "n", "N", "esc", "q":
		m.quitConfirm = false
		m.notice = "Quit canceled."
	}
	return m, nil
}

// quitAnswerRow names the two ways out of the dialog, y first, so the answer is
// readable without the surrounding prose.
func quitAnswerRow() string {
	return keyStyle.Render("y") + mutedStyle.Render(" quit") +
		faintStyle.Render("  ·  ") +
		keyStyle.Render("n/esc") + mutedStyle.Render(" stay")
}

// liveTaskCount counts the attempts quitting will cancel. It reads process
// status rather than the header's displayed stage: a running task that is
// blocked, waiting or waiting for subagents is still a live process.
func (m model) liveTaskCount() int {
	count := 0
	for _, task := range m.tasks {
		if task.Status == orchestrator.Running {
			count++
		}
	}
	return count
}

// quitCountLine states how much work the confirmation is about to stop.
func quitCountLine(running int) string {
	if running == 0 {
		return "Nothing is running."
	}
	task, verb := "tasks", "stop"
	if running == 1 {
		task, verb = "task", "stops"
	}
	return fmt.Sprintf("%d running %s %s when you quit.", running, task, verb)
}

// quitWrap is one muted row group, word-wrapped to the modal's content width so
// a narrow frame reads the whole line instead of an ellipsized tail.
func quitWrap(text string, inner int) []string {
	var rows []string
	for _, line := range strings.Split(ansi.Wordwrap(text, inner, ""), "\n") {
		rows = append(rows, mutedStyle.Render(line))
	}
	return rows
}

// quitRows joins row groups into one body, copying so the caller's groups stay
// independent.
func quitRows(groups ...[]string) []string {
	var rows []string
	for _, group := range groups {
		rows = append(rows, group...)
	}
	return rows
}

// quitModal projects the dialog for a terminal of w by h cells. The frame keeps
// the question, then the running count, then the reassurance that history
// survives, dropping the least important line before the answer row when the
// frame is short. It returns no content when the terminal cannot hold a usable
// frame; the board then prints the same answer row on its resize prompt.
func (m model) quitModal(w, h int) (mw, mh int, content []string) {
	inner := min(quitModalWidth-4, w-8)
	ceiling := min(quitModalHeight, h-2)
	if inner < 12 || ceiling < 4 {
		return 0, 0, nil
	}
	mw = inner + 4
	title := accentStyle.Render("QUIT")
	hint := fit(quitAnswerRow(), inner, 1)
	question := quitWrap("Leave pilharmonic?", inner)
	count := quitWrap(quitCountLine(m.liveTaskCount()), inner)
	history := quitWrap("History and worktrees are kept.", inner)
	// Richest body first; each step gives up the least useful line still there,
	// and the empty body is the last resort that always keeps the question.
	bodies := [][]string{
		quitRows(question, count, history),
		quitRows(question, count),
		quitRows(question, history),
		quitRows(count),
		nil,
	}
	for _, body := range bodies {
		candidate := append(append([]string{title}, body...), "", hint)
		if len(candidate) <= ceiling-2 {
			return mw, len(candidate) + 2, candidate
		}
	}
	return 0, 0, nil
}

// quitView draws the confirmation as a centered modal, dimming the screen it
// covers with the same overlay the menus and help use, so the family reads as
// one surface.
func (m model) quitView(background string, w, h int) string {
	mw, mh, content := m.quitModal(w, h)
	if content == nil {
		return fit(background, w, h)
	}
	modal := focusedPanel(strings.Join(content, "\n"), mw, mh, true)
	bg := strings.Split(background, "\n")
	box := strings.Split(modal, "\n")
	x, y := (w-mw)/2, (h-mh)/2
	result := make([]string, max(0, h))
	for row := range result {
		line := ""
		if row < len(bg) {
			line = ansi.Strip(bg[row])
		}
		line = lipgloss.PlaceHorizontal(w, lipgloss.Left, line)
		if row >= y && row < y+len(box) {
			result[row] = mutedStyle.Faint(true).Render(ansi.Cut(line, 0, x)) + box[row-y] + mutedStyle.Faint(true).Render(ansi.Cut(line, x+mw, w))
		} else {
			result[row] = mutedStyle.Faint(true).Render(line)
		}
	}
	return fit(strings.Join(result, "\n"), w, h)
}
