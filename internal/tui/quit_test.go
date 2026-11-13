package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
)

// quitPressed is the key press the terminal reports for a rune shortcut.
func quitPressed(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

func TestBoardQuitKeysAskBeforeQuitting(t *testing.T) {
	for _, msg := range []tea.KeyMsg{
		quitPressed('q'),
		{Type: tea.KeyCtrlC},
	} {
		m := boardWithTask()
		next, cmd := m.Update(msg)
		m = next.(model)
		if cmd != nil {
			if _, ok := cmd().(tea.QuitMsg); ok {
				t.Fatalf("%q quit without confirmation", msg.String())
			}
		}
		if !m.quitConfirm {
			t.Fatalf("%q did not arm the confirmation", msg.String())
		}
		plain := ansi.Strip(m.View())
		if !strings.Contains(plain, "Leave pilharmonic?") {
			t.Fatalf("%q did not draw the confirmation: %s", msg.String(), plain)
		}
		if !strings.Contains(plain, "y quit") || !strings.Contains(plain, "n/esc stay") {
			t.Fatalf("%q did not offer both answers: %s", msg.String(), plain)
		}
	}
}

func TestQuitConfirmationCountsRunningTasks(t *testing.T) {
	running := boardWithTask()
	running = running.requestQuit()
	if plain := ansi.Strip(running.View()); !strings.Contains(plain, "1 running task stops when you quit.") {
		t.Fatalf("running count missing from dialog: %s", plain)
	}
	if !strings.Contains(ansi.Strip(running.View()), "History and worktrees are kept.") {
		t.Fatalf("record notice missing from dialog: %s", ansi.Strip(running.View()))
	}

	idle := model{width: 100, height: 28}.requestQuit()
	if plain := ansi.Strip(idle.View()); !strings.Contains(plain, "Nothing is running.") {
		t.Fatalf("idle dialog did not say nothing is running: %s", plain)
	}
}

func TestQuitConfirmationAnswersWithQuit(t *testing.T) {
	for _, msg := range []tea.KeyMsg{
		quitPressed('y'),
		quitPressed('Y'),
		{Type: tea.KeyEnter},
		{Type: tea.KeyCtrlC},
	} {
		m := boardWithTask().requestQuit()
		next, cmd := m.Update(msg)
		if cmd == nil {
			t.Fatalf("%q did not answer the confirmation", msg.String())
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatalf("%q did not return a quit command", msg.String())
		}
		if !next.(model).quitConfirm {
			t.Fatalf("%q closed the dialog before quitting", msg.String())
		}
	}
}

func TestQuitConfirmationCancelKeepsSurface(t *testing.T) {
	for _, msg := range []tea.KeyMsg{
		quitPressed('n'),
		quitPressed('N'),
		quitPressed('q'),
		{Type: tea.KeyEsc},
	} {
		m := boardWithTask()
		m.router = true
		m.routerDraft = "keep this draft"
		m = m.requestQuit()
		next, cmd := m.Update(msg)
		m = next.(model)
		if cmd != nil {
			t.Fatalf("%q returned a command while canceling", msg.String())
		}
		if m.quitConfirm {
			t.Fatalf("%q left the confirmation armed", msg.String())
		}
		if !m.router || m.routerDraft != "keep this draft" {
			t.Fatalf("%q disturbed the surface underneath: router=%v draft=%q", msg.String(), m.router, m.routerDraft)
		}
		if m.notice != "Quit canceled." {
			t.Fatalf("%q did not report the cancellation: %q", msg.String(), m.notice)
		}
	}
}

func TestQuitConfirmationOutranksEverySurface(t *testing.T) {
	base := boardWithTask()
	surfaces := map[string]model{
		"help":        func() model { m := base; m.help = true; return m }(),
		"router":      func() model { m := base; m.router = true; return m }(),
		"composer":    func() model { m := base; m.input = composer{kind: newTaskInput}; return m }(),
		"cleanup":     func() model { m := base; m.cleanup = cleanupRequest{kind: deleteTask, ids: []string{"task"}}; return m }(),
		"login menu":  func() model { m := base; m.loginMenu = true; return m }(),
		"model menu":  func() model { m := base; m.modelMenu = true; return m }(),
		"agent class": func() model { m := base; m.agentClassMenu = true; return m }(),
	}
	for name, m := range surfaces {
		next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		m = next.(model)
		if cmd != nil {
			if _, ok := cmd().(tea.QuitMsg); ok {
				t.Fatalf("%s: ctrl+c quit without confirmation", name)
			}
		}
		if !m.quitConfirm {
			t.Fatalf("%s: ctrl+c did not arm the confirmation", name)
		}
		// The confirmation owns the keyboard, so no key can move the surface
		// it covers, whether that surface scrolls, types or selects.
		before := m
		after := key(m, quitPressed('j'))
		after = key(after, quitPressed('a'))
		if after.helpScroll != before.helpScroll || after.selected != before.selected ||
			after.routerDraft != before.routerDraft || after.modelIndex != before.modelIndex ||
			after.modelCustomInput != before.modelCustomInput ||
			after.input.text != before.input.text ||
			after.agentPicker.index != before.agentPicker.index ||
			after.profilePicker.index != before.profilePicker.index ||
			after.loginProvider != before.loginProvider || after.loginKey != before.loginKey ||
			after.cleanup.kind != before.cleanup.kind {
			t.Fatalf("%s: key reached the surface under the confirmation", name)
		}
	}
}

func TestQuitConfirmationIgnoresPastedText(t *testing.T) {
	m := boardWithTask().requestQuit()
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y"), Paste: true})
	m = next.(model)
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Fatal("pasted text answered the confirmation")
		}
	}
	if !m.quitConfirm {
		t.Fatal("pasted text dismissed the confirmation")
	}
}

func TestQuitConfirmationFitsTerminal(t *testing.T) {
	for _, size := range [][2]int{{120, 35}, {80, 24}, {50, 20}, {40, 12}, {30, 10}, {20, 5}} {
		m := boardWithTask()
		m.width, m.height = size[0], size[1]
		m = m.requestQuit()
		view := m.View()
		if lipgloss.Width(view) > size[0] || lipgloss.Height(view) > size[1] {
			t.Fatalf("%v: rendered %dx%d", size, lipgloss.Width(view), lipgloss.Height(view))
		}
		if !strings.Contains(ansi.Strip(view), "y quit") {
			t.Fatalf("%v: no answer keys on screen: %s", size, ansi.Strip(view))
		}
	}
}

// quitPlain is the rendered screen as flowing text, so a message that wraps on
// a narrow terminal can still be matched as one sentence.
func quitPlain(view string) string {
	plain := strings.Map(func(r rune) rune {
		switch r {
		case '│', '╭', '╮', '╰', '╯', '─', '▌':
			return ' '
		}
		return r
	}, ansi.Strip(view))
	return strings.Join(strings.Fields(plain), " ")
}

func TestQuitConfirmationKeepsProseOnNarrowTerminal(t *testing.T) {
	// Both sizes are narrow enough to wrap the prose, which used to drop the
	// question or the running count from the dialog.
	for _, size := range [][2]int{{30, 10}, {30, 24}, {40, 12}} {
		m := boardWithTask()
		m.width, m.height = size[0], size[1]
		m = m.requestQuit()
		plain := quitPlain(m.View())
		if !strings.Contains(plain, "Leave pilharmonic?") && !strings.Contains(plain, "Nothing is running.") {
			t.Fatalf("%v: dialog lost the question: %s", size, plain)
		}
		if !strings.Contains(plain, "when you quit") {
			t.Fatalf("%v: dialog lost the running count: %s", size, plain)
		}
		if !strings.Contains(plain, "History and worktrees are kept.") && size[1] > 10 {
			t.Fatalf("%v: dialog dropped the reassurance: %s", size, plain)
		}
	}
}

func TestQuitConfirmationCountsLiveAttempts(t *testing.T) {
	// A running parent waiting for a child is displayed as WAITING FOR
	// SUBAGENTS, but its process is still canceled by quitting, so the dialog
	// must not say nothing is running.
	m := model{width: 120, height: 35, tasks: []orchestrator.Task{
		{ID: "parent", Title: "parent", Status: orchestrator.Running},
		{ID: "child", ParentID: "parent", Title: "child", Status: orchestrator.Queued},
	}}
	m = m.requestQuit()
	plain := ansi.Strip(m.View())
	if strings.Contains(plain, "Nothing is running.") {
		t.Fatalf("dialog ignored a live attempt: %s", plain)
	}
	if !strings.Contains(plain, "1 running task stops when you quit.") {
		t.Fatalf("dialog did not count the live attempt: %s", plain)
	}
}

func TestQuitConfirmationOwnsTheMouse(t *testing.T) {
	m := boardWithTask()
	m.help = true
	m = m.requestQuit()
	wheel := tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown}
	next, cmd := m.Update(wheel)
	m = next.(model)
	if cmd != nil {
		t.Fatal("wheel returned a command while the dialog was open")
	}
	if m.helpScroll != 0 {
		t.Fatalf("wheel scrolled the help overlay under the dialog: %d", m.helpScroll)
	}
	press := tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 10, Y: 8}
	next, _ = m.Update(press)
	if next.(model).selection.dragging {
		t.Fatal("mouse press started a selection under the dialog")
	}
}

func TestQuitConfirmationPromptBelowMinimumSize(t *testing.T) {
	m := model{width: 24, height: 8}.requestQuit()
	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "y quit · n/esc stay") {
		t.Fatalf("resize prompt does not carry the answer keys: %s", plain)
	}
}

func TestQuitConfirmationLeavesTaskStateAlone(t *testing.T) {
	m := boardWithTask().requestQuit()
	before := m.tasks[0]
	next, _ := m.Update(quitPressed('n'))
	m = next.(model)
	if len(m.tasks) != 1 || m.tasks[0].ID != before.ID || m.tasks[0].Status != before.Status {
		t.Fatalf("confirmation changed the task list: %+v", m.tasks)
	}
}
