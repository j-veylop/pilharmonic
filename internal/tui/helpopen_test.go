package tui

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"pilharmonic/internal/orchestrator"
)

// f1Key is the key press the terminal reports for the help shortcut.
func f1Key() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyF1} }

// helpSurfaces returns one model per screen help must be reachable from. Each
// model is built independently because help never changes the state underneath.
func helpSurfaces() map[string]model {
	base := boardWithTask()
	surfaces := map[string]model{}

	s := base
	surfaces["board"] = s

	s = base
	s.detailFocus = true
	surfaces["details"] = s

	s = base
	s.fullscreen = true
	surfaces["fullscreen details"] = s

	s = base
	s.activity.open = true
	s.detailFocus = true
	surfaces["activity list"] = s

	s = base
	s.activity.open = true
	s.detailFocus = true
	s.activity.expanded = true
	surfaces["expanded tool call"] = s

	s = base
	s.router.open = true
	s.router.text = "do work"
	s.router.cursor = len(s.router.text)
	surfaces["request form"] = s

	s = base
	s.router.open = true
	s.router.focus = routerFocusRecent
	surfaces["request form recent list"] = s

	s = base
	s.input = composer{kind: newTaskInput, editor: editor{text: "draft"}}
	surfaces["new task composer"] = s

	s = base
	s.input = composer{kind: reportInput, target: "task", editor: editor{text: "blocked need access"}}
	surfaces["report composer"] = s

	s = base
	s.agentPicker.target = "task"
	s.agentPicker.choices = []orchestrator.AgentInfo{{Name: "general"}}
	surfaces["agent picker"] = s

	s = base
	s.profilePicker.target = "task"
	s.profilePicker.choices = []orchestrator.Profile{{Name: "review"}}
	surfaces["profile picker"] = s

	s = base
	s.cleanup = cleanupRequest{kind: deleteTask, ids: []string{"task"}, title: "work"}
	surfaces["delete confirmation"] = s

	s = base
	s.login.menu = true
	surfaces["login menu"] = s

	s = base
	s.login.provider = "openai"
	surfaces["api key entry"] = s

	s = base
	s.models.menu = true
	s.models.step = modelStepClass
	surfaces["model menu"] = s

	s = base
	s.models.menu = true
	s.models.step = modelStepCustom
	s.models.customInput = "custom-id"
	surfaces["custom model entry"] = s

	s = base
	s.agentClass.menu = true
	surfaces["agent class menu"] = s

	return surfaces
}

// The question mark is a normal character in a draft, so it only opens help
// where the operator is not typing text.
func TestQuestionMarkOpensHelpOnlyOutsideTextEntry(t *testing.T) {
	opening := map[string]model{}
	typing := map[string]model{}

	base := boardWithTask()
	opening["board"] = base

	s := base
	s.router.open = true
	s.router.focus = routerFocusRecent
	opening["request form list"] = s

	s = base
	s.agentPicker.target = "task"
	opening["agent picker"] = s

	s = base
	s.cleanup = cleanupRequest{kind: discardTask, ids: []string{"task"}, title: "work"}
	opening["discard confirmation"] = s

	s = base
	s.models.menu = true
	s.models.step = modelStepProvider
	opening["model menu"] = s

	s = base
	s.router.open = true
	s.router.text = "keep"
	s.router.cursor = len("keep")
	typing["request form"] = s

	s = base
	s.input = composer{kind: threadInput, target: "task", editor: editor{text: "keep", cursor: len("keep")}}
	typing["composer"] = s

	s = base
	s.models.menu = true
	s.models.step = modelStepCustom
	typing["custom model entry"] = s

	s = base
	s.login.provider = "openai"
	typing["api key entry"] = s

	for name, m := range opening {
		t.Run(name, func(t *testing.T) {
			next := key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
			if !next.help.open {
				t.Fatalf("? did not open help on the %s", name)
			}
		})
	}
	for name, m := range typing {
		t.Run(name, func(t *testing.T) {
			next := key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
			if next.help.open {
				t.Fatalf("? was stolen from the %s", name)
			}
			switch {
			case next.input.text == "keep?":
			case next.router.text == "keep?":
			case next.models.customInput == "?":
			case next.login.key == "?":
			default:
				t.Fatalf("%s did not receive the character: %+v", name, next)
			}
		})
	}
}

func TestPastedQuestionMarkIsNotAShortcut(t *testing.T) {
	m := boardWithTask()
	m.input = composer{kind: newTaskInput, editor: editor{text: "keep", cursor: len("keep")}}
	paste := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?"), Paste: true}
	m = key(m, paste)
	if m.help.open {
		t.Fatal("a pasted ? opened help")
	}
	if m.input.text != "keep?" {
		t.Fatalf("pasted text was dropped: %q", m.input.text)
	}
}

// A click belongs to the help overlay, not to the text it covers.
func TestClicksBehindHelpDoNotStartASelection(t *testing.T) {
	m := boardWithTask()
	m.help.open = true
	next, _ := m.Update(tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      70, Y: 12,
	})
	m = next.(model)
	if m.selection.dragging || m.selection.hasRange() {
		t.Fatalf("help let a click reach the pane underneath: %+v", m.selection)
	}
}

// F1 opens help over every surface at every size, help owns the keys while it
// is open, and closing it returns the surface exactly as it was.
func TestF1HelpCoversEverySurfaceAndRestoresIt(t *testing.T) {
	for name, base := range helpSurfaces() {
		if footer := ansi.Strip(base.footer(base.width)); !strings.Contains(footer, "help") {
			t.Fatalf("%s footer does not advertise help: %q", name, footer)
		}
		for _, size := range [][2]int{{30, 10}, {50, 20}, {80, 24}, {120, 40}} {
			m := base
			m.width, m.height = size[0], size[1]
			opened := key(m, f1Key())
			view := opened.View()
			assertFits(t, view, size[0], size[1])
			if !opened.help.open || !strings.Contains(ansi.Strip(view), "HELP") {
				t.Fatalf("%s at %v: F1 did not draw help", name, size)
			}
			next, cmd := opened.Update(runeKey("y"))
			if cmd != nil {
				t.Fatalf("%s: a key under help ran a command", name)
			}
			if closed := key(next.(model), f1Key()); !reflect.DeepEqual(closed, m) {
				t.Fatalf("%s at %v: closing help did not restore the surface:\n%+v\n%+v", name, size, closed, m)
			}
		}
	}
}
