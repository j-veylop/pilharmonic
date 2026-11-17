package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
)

func quits(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// Every exit key on every surface asks first, and the dialog then owns the
// keyboard so no key reaches the surface it covers.
func TestQuitKeysAskOnEverySurface(t *testing.T) {
	surfaces := helpSurfaces()
	surfaces["help"] = func() model { m := boardWithTask(); m.help.open = true; return m }()
	for name, m := range surfaces {
		keys := []tea.KeyMsg{{Type: tea.KeyCtrlC}}
		if name == "board" {
			keys = append(keys, runeKey("q"))
		}
		for _, msg := range keys {
			next, cmd := m.Update(msg)
			armed := next.(model)
			if quits(cmd) || !armed.quitConfirm {
				t.Fatalf("%s: %s did not ask before quitting", name, msg)
			}
			plain := ansi.Strip(armed.View())
			if !strings.Contains(plain, "Leave pilharmonic?") || !strings.Contains(plain, "y quit") || !strings.Contains(plain, "n/esc stay") {
				t.Fatalf("%s: confirmation not drawn: %s", name, plain)
			}
			after := key(key(armed, runeKey("j")), runeKey("a"))
			if after.help != armed.help || after.selected != armed.selected || after.router.Editor != armed.router.Editor ||
				after.models.index != armed.models.index || after.models.customInput != armed.models.customInput ||
				after.input.Editor != armed.input.Editor || after.agentPicker.index != armed.agentPicker.index ||
				after.profilePicker.index != armed.profilePicker.index || after.setup.index != armed.setup.index ||
				after.login != armed.login || after.cleanup.kind != armed.cleanup.kind {
				t.Fatalf("%s: a key reached the surface under the confirmation", name)
			}
		}
	}
}

func TestQuitConfirmationAnswers(t *testing.T) {
	cases := []struct {
		msg  tea.KeyMsg
		quit bool
		open bool
	}{
		{runeKey("y"), true, true},
		{runeKey("Y"), true, true},
		{tea.KeyMsg{Type: tea.KeyEnter}, true, true},
		{tea.KeyMsg{Type: tea.KeyCtrlC}, true, true},
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y"), Paste: true}, false, true},
		{runeKey("x"), false, true},
		{runeKey("n"), false, false},
		{runeKey("N"), false, false},
		{runeKey("q"), false, true},
		{tea.KeyMsg{Type: tea.KeyEsc}, false, false},
	}
	for _, tc := range cases {
		m := boardWithTask()
		m.router.open, m.router.Text = true, "keep this draft"
		next, cmd := m.requestQuit().Update(tc.msg)
		m = next.(model)
		if quits(cmd) != tc.quit || m.quitConfirm != tc.open {
			t.Fatalf("%q: quit=%v open=%v", tc.msg.String(), quits(cmd), m.quitConfirm)
		}
		if !m.router.open || m.router.Text != "keep this draft" || len(m.tasks) != 1 {
			t.Fatalf("%q disturbed the surface underneath", tc.msg.String())
		}
		if !tc.open && m.notice.text != "Quit canceled." {
			t.Fatalf("%q did not report the cancellation: %q", tc.msg.String(), m.notice.text)
		}
	}
}

// quitPlain is the rendered screen as flowing text, so wrapped prose still
// matches as one sentence.
func quitPlain(view string) string {
	plain := strings.Map(func(r rune) rune {
		if strings.ContainsRune("│╭╮╰╯─▌", r) {
			return ' '
		}
		return r
	}, ansi.Strip(view))
	return strings.Join(strings.Fields(plain), " ")
}

func TestQuitConfirmationCountsLiveWorkAndFits(t *testing.T) {
	waiting := model{width: 120, height: 35, tasks: []orchestrator.Task{
		{ID: "parent", Title: "parent", Status: orchestrator.Running},
		{ID: "child", ParentID: "parent", Title: "child", Status: orchestrator.Queued},
	}}
	for _, tc := range []struct {
		m    model
		want string
	}{
		{boardWithTask(), "1 running task stops when you quit."},
		{waiting, "1 running task stops when you quit."}, // a parent waiting for subagents is still live
		{model{width: 100, height: 28}, "Nothing is running."},
	} {
		if plain := quitPlain(tc.m.requestQuit().View()); !strings.Contains(plain, tc.want) || !strings.Contains(plain, "History and worktrees are kept") {
			t.Fatalf("dialog lost %q: %s", tc.want, plain)
		}
	}
	for _, size := range [][2]int{{120, 35}, {80, 24}, {40, 12}, {30, 10}, {30, 24}, {20, 5}} {
		m := boardWithTask()
		m.width, m.height = size[0], size[1]
		view := m.requestQuit().View()
		assertFits(t, view, size[0], size[1])
		plain := quitPlain(view)
		if size[0] < 30 {
			if !strings.Contains(plain, "y quit · n/esc stay") {
				t.Fatalf("%v: resize prompt lost the answer keys: %s", size, plain)
			}
			continue
		}
		if !strings.Contains(plain, "y quit") || !strings.Contains(plain, "when you quit") {
			t.Fatalf("%v: dialog lost its count or answers: %s", size, plain)
		}
		if size[1] > 10 && !strings.Contains(plain, "History and worktrees are kept") {
			t.Fatalf("%v: dialog dropped the reassurance: %s", size, plain)
		}
	}
}

func TestQuitConfirmationOwnsTheMouse(t *testing.T) {
	m := boardWithTask()
	m.help.open = true
	m = m.requestQuit()
	next, cmd := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	if cmd != nil || next.(model).help.scroll != 0 {
		t.Fatal("wheel reached the help overlay under the dialog")
	}
	next, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 10, Y: 8})
	if next.(model).selection.dragging {
		t.Fatal("mouse press started a selection under the dialog")
	}
}

// The dialog opens on the quit button, and h/l, the arrows and Tab move the
// focus without answering it. Tab cycles, so focus never gets stuck.
func TestQuitDialogFocusMovesBetweenButtons(t *testing.T) {
	cases := []struct {
		from int
		msg  tea.KeyMsg
		want int
	}{
		{quitButtonQuit, runeKey("l"), quitButtonStay},
		{quitButtonQuit, tea.KeyMsg{Type: tea.KeyRight}, quitButtonStay},
		{quitButtonQuit, tea.KeyMsg{Type: tea.KeyTab}, quitButtonStay},
		{quitButtonQuit, tea.KeyMsg{Type: tea.KeyShiftTab}, quitButtonStay},
		{quitButtonStay, runeKey("h"), quitButtonQuit},
		{quitButtonStay, tea.KeyMsg{Type: tea.KeyLeft}, quitButtonQuit},
		{quitButtonStay, tea.KeyMsg{Type: tea.KeyTab}, quitButtonQuit},
		{quitButtonStay, tea.KeyMsg{Type: tea.KeyShiftTab}, quitButtonQuit},
	}
	for _, tc := range cases {
		m := boardWithTask().requestQuit()
		if m.quitChoice != quitButtonQuit {
			t.Fatal("the dialog did not open on the quit button")
		}
		m.quitChoice = tc.from
		next, cmd := m.Update(tc.msg)
		if got := next.(model); cmd != nil || !got.quitConfirm || got.quitChoice != tc.want {
			t.Fatalf("%q from %d: cmd=%v open=%v choice=%d", tc.msg.String(), tc.from, cmd, got.quitConfirm, got.quitChoice)
		}
	}
	// Two Tabs come back to where they started.
	m := boardWithTask().requestQuit()
	if m = key(key(m, tea.KeyMsg{Type: tea.KeyTab}), tea.KeyMsg{Type: tea.KeyTab}); m.quitChoice != quitButtonQuit {
		t.Fatal("tab did not cycle back to the quit button")
	}
}

// The hint follows focus so Enter's effect is never a surprise, without arrows
// in either button.
func TestQuitDialogMarksTheFocusedButton(t *testing.T) {
	m := boardWithTask().requestQuit()
	if plain := quitPlain(m.View()); strings.Contains(plain, "▸") || !strings.Contains(plain, "y quit") || !strings.Contains(plain, "enter quit") {
		t.Fatalf("quit button not marked as focused: %s", plain)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyTab})
	if plain := quitPlain(m.View()); strings.Contains(plain, "▸") || !strings.Contains(plain, "n/esc stay") || !strings.Contains(plain, "enter stay") {
		t.Fatalf("focus marker or hint did not follow the selection: %s", plain)
	}
}

// Enter presses the focused button, so the safe answer is reachable without
// remembering a key. A paste still never answers.
func TestQuitDialogEnterPressesTheFocusedButton(t *testing.T) {
	quit := boardWithTask().requestQuit()
	if next, cmd := quit.Update(tea.KeyMsg{Type: tea.KeyEnter}); !quits(cmd) || !next.(model).quitConfirm {
		t.Fatal("enter on the focused quit button did not quit")
	}
	stay := boardWithTask().requestQuit()
	stay = key(stay, tea.KeyMsg{Type: tea.KeyTab})
	next, cmd := stay.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if quits(cmd) {
		t.Fatal("enter on the focused stay button quit")
	}
	if got := next.(model); got.quitConfirm || got.notice.text != "Quit canceled." {
		t.Fatalf("stay button did not cancel: open=%v notice=%q", got.quitConfirm, got.notice.text)
	}
	if next, cmd := quit.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y"), Paste: true}); quits(cmd) || !next.(model).quitConfirm {
		t.Fatal("a paste answered the dialog")
	}
}

// Both buttons stay visible even when the card has to stack them in a narrow
// terminal, and the bar keeps the count, the question and the reassurance.
func TestQuitDialogDrawsBothButtons(t *testing.T) {
	for _, size := range [][2]int{{120, 35}, {80, 24}, {40, 12}, {30, 24}, {30, 10}} {
		m := boardWithTask()
		m.width, m.height = size[0], size[1]
		view := m.requestQuit().View()
		assertFits(t, view, size[0], size[1])
		plain := quitPlain(view)
		want := []string{"QUIT", "Leave pilharmonic?", "y quit", "n/esc stay"}
		if size[0] >= 40 {
			// The focus hint is optional; it is the first row a short card drops.
			want = append(want, "choose")
		}
		for _, text := range want {
			if !strings.Contains(plain, text) {
				t.Fatalf("%v: dialog lost %q: %s", size, text, plain)
			}
		}
	}
}
