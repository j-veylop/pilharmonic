package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/j-veylop/pilharmonic/internal/orchestrator"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The dialog's buttons, in focus order. Quitting comes first so Enter keeps
// its old meaning and staying is one key away in either direction.
const (
	quitButtonQuit = iota
	quitButtonStay
)

var quitButtonLabels = [2]string{"quit", "cancel"}

// requestQuit opens the confirmation. Every exit key on every surface asks
// first, so a stray q or Ctrl-c never ends a run with work in flight. The card
// is drawn last and takes the keyboard and mouse before any other surface;
// backing out leaves that surface exactly as it was.
func (m model) requestQuit() model {
	m.quitConfirm, m.quitChoice = true, quitButtonQuit
	m.notice.text = ""
	m.vimG, m.vimCount = false, 0
	return m
}

// quitKey answers the dialog; a paste never does. y, n and Esc answer
// directly; Enter presses the focused button, and h/l, the arrows and Tab move
// the focus.
func (m model) quitKey(msg tea.KeyMsg) (model, tea.Cmd) {
	if msg.Paste {
		return m, nil
	}
	switch msg.String() {
	case "y", "Y", "ctrl+c":
		return m, tea.Quit
	case "n", "N", "esc":
		return m.cancelQuit(), nil
	case "enter":
		if m.quitChoice == quitButtonStay {
			return m.cancelQuit(), nil
		}
		return m, tea.Quit
	case "left", "h":
		m.quitChoice = quitButtonQuit
	case "right", "l":
		m.quitChoice = quitButtonStay
	case "tab", "shift+tab":
		// With two buttons, either direction cycles to the other one.
		m.quitChoice = 1 - m.quitChoice
	case "q":
		// q opened the dialog, so it must not answer it by accident.
	}
	return m, nil
}

// cancelQuit closes the dialog and reports that nothing stopped.
func (m model) cancelQuit() model {
	m.quitConfirm, m.quitChoice = false, quitButtonQuit
	m.notice.text = "Quit canceled."
	return m
}

// quitLiveWork counts the tasks a quit stops. It counts process status, not
// the displayed stage: a task waiting for subagents is still live.
func (m model) quitLiveWork() int {
	running := 0
	for _, task := range m.tasks {
		if task.Status == orchestrator.Running {
			running++
		}
	}
	return running
}

// quitCountLine states how many attempts quitting cancels.
func (m model) quitCountLine() string {
	switch running := m.quitLiveWork(); running {
	case 0:
		return "Nothing is running."
	case 1:
		return "1 running task stops when you quit."
	default:
		return fmt.Sprintf("%d running tasks stop when you quit.", running)
	}
}

// quitView draws the confirmation card over background.
func (m model) quitView(background string, w, h int) string {
	mw := max(24, min(54, w-2))
	mh, content := m.quitModal(mw, h)
	if mh < 5 {
		return fit(background, w, h)
	}
	return modalView(background, w, h, mw, mh, content)
}

// quitModal assembles the card's rows for a terminal h rows tall. Rows are
// grouped by importance and the least important groups drop out first, so a
// short terminal still shows the question, the count and both buttons.
func (m model) quitModal(mw, h int) (int, []string) {
	inner := mw - 4
	countStyle := mutedStyle
	if m.quitLiveWork() > 0 {
		countStyle = warningStyle
	}
	type group struct {
		order, priority int
		rows            []string
	}
	groups := []group{
		{order: 0, priority: 3, rows: []string{accentStyle.Render("QUIT")}},
		{order: 1, priority: 4, rows: []string{faintStyle.Render(strings.Repeat("─", inner))}},
		{order: 2, priority: 1, rows: wrapStyled("Leave pilharmonic?", titleStyle, inner)},
		{order: 3, priority: 2, rows: wrapStyled(m.quitCountLine(), countStyle, inner)},
		{order: 4, priority: 6, rows: wrapStyled("History and worktrees are kept", faintStyle, inner)},
		{order: 5, priority: 5, rows: []string{""}},
		{order: 6, priority: 0, rows: quitButtonRows(m, inner)},
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].priority < groups[j].priority })
	budget, used, chosen := h-2, 0, make([]group, 0, len(groups))
	for _, candidate := range groups {
		if used+len(candidate.rows) > budget {
			continue
		}
		used += len(candidate.rows)
		chosen = append(chosen, candidate)
	}
	sort.SliceStable(chosen, func(i, j int) bool { return chosen[i].order < chosen[j].order })
	var content []string
	for _, g := range chosen {
		for _, row := range g.rows {
			content = append(content, lipgloss.PlaceHorizontal(inner, lipgloss.Center, row))
		}
	}
	return len(content) + 2, content
}

// quitButtonRows lays the buttons side by side, or one per row when the card
// is too narrow to hold both.
func quitButtonRows(m model, inner int) []string {
	quit := quitButton(quitButtonQuit, m.quitChoice == quitButtonQuit)
	stay := quitButton(quitButtonStay, m.quitChoice == quitButtonStay)
	if row := quit + "  " + stay; lipgloss.Width(row) <= inner {
		return []string{row}
	}
	return []string{quit, stay}
}

// selectionButton uses the same filled pills for dialog buttons and tabs.
func selectionButton(label string, focused bool) string {
	if focused {
		return pillStyle.Render(label)
	}
	return pillMutedStyle.Render(label)
}

func quitButton(index int, focused bool) string {
	return selectionButton(quitButtonLabels[index], focused)
}

// wrapStyled wraps plain text to width and styles each line in place.
func wrapStyled(text string, style lipgloss.Style, width int) []string {
	lines := wrapHelp(text, max(1, width))
	styled := make([]string, len(lines))
	for i, line := range lines {
		styled[i] = style.Render(line)
	}
	return styled
}
