package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	s.activity = true
	s.detailFocus = true
	surfaces["activity list"] = s

	s = base
	s.activity = true
	s.detailFocus = true
	s.activityExpanded = true
	surfaces["expanded tool call"] = s

	s = base
	s.router = true
	s.routerDraft = "do work"
	s.routerCursor = len(s.routerDraft)
	surfaces["request form"] = s

	s = base
	s.router = true
	s.routerFocus = routerFocusRecent
	surfaces["request form recent list"] = s

	s = base
	s.input = composer{kind: newTaskInput, text: "draft"}
	surfaces["new task composer"] = s

	s = base
	s.input = composer{kind: reportInput, target: "task", text: "blocked need access"}
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
	s.loginMenu = true
	surfaces["login menu"] = s

	s = base
	s.loginProvider = "openai"
	surfaces["api key entry"] = s

	s = base
	s.modelMenu = true
	s.modelStep = modelStepClass
	surfaces["model menu"] = s

	s = base
	s.modelMenu = true
	s.modelStep = modelStepCustom
	s.modelCustomInput = "custom-id"
	surfaces["custom model entry"] = s

	s = base
	s.agentClassMenu = true
	surfaces["agent class menu"] = s

	return surfaces
}

func TestF1OpensHelpFromEverySurface(t *testing.T) {
	for name, m := range helpSurfaces() {
		t.Run(name, func(t *testing.T) {
			m = key(m, f1Key())
			if !m.help {
				t.Fatalf("F1 did not open help on the %s", name)
			}
			if view := ansi.Strip(m.View()); !strings.Contains(view, "HELP") {
				t.Fatalf("help is not rendered on the %s: %q", name, view)
			}
			m = key(m, f1Key())
			if m.help {
				t.Fatalf("F1 did not close help on the %s", name)
			}
		})
	}
}

func TestHelpCoversEverySurfaceWithoutBreakingLayout(t *testing.T) {
	for name, base := range helpSurfaces() {
		for _, size := range [][2]int{{30, 10}, {50, 20}, {80, 24}, {120, 40}} {
			m := base
			m.width, m.height = size[0], size[1]
			m = key(m, f1Key())
			if !m.help {
				t.Fatalf("%s at %v: F1 did not open help", name, size)
			}
			view := m.View()
			if lipgloss.Width(view) > size[0] || lipgloss.Height(view) > size[1] {
				t.Fatalf("%s at %v: rendered %dx%d", name, size, lipgloss.Width(view), lipgloss.Height(view))
			}
		}
	}
}

func TestHelpReturnsToTheSurfaceItCovered(t *testing.T) {
	m := boardWithTask()
	m.input = composer{kind: newTaskInput, target: "task", text: "keep this draft"}
	m = key(m, f1Key())
	if m.input.text != "keep this draft" {
		t.Fatalf("help lost the draft: %q", m.input.text)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.help {
		t.Fatal("escape did not close help")
	}
	if m.input.kind != newTaskInput || m.input.text != "keep this draft" {
		t.Fatalf("composer did not survive help: %+v", m.input)
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "keep this draft") || !strings.Contains(view, "NEW TASK") {
		t.Fatalf("composer did not return: %q", view)
	}
}

// Help floats above a menu instead of replacing it, and closing help brings the
// menu back with its selection intact.
func TestHelpStacksAboveMenus(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(m *model)
		marker   string
		restored func(m model) bool
	}{
		{
			name:   "login menu",
			setup:  func(m *model) { m.loginMenu = true },
			marker: "LOGIN",
			restored: func(m model) bool {
				return m.loginMenu
			},
		},
		{
			name:   "model menu",
			setup:  func(m *model) { m.modelMenu = true; m.modelStep = modelStepClass },
			marker: "MODEL",
			restored: func(m model) bool {
				return m.modelMenu && m.modelStep == modelStepClass
			},
		},
		{
			name:   "agent class menu",
			setup:  func(m *model) { m.agentClassMenu = true },
			marker: "AGENT",
			restored: func(m model) bool {
				return m.agentClassMenu
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := boardWithTask()
			tc.setup(&m)
			m = key(m, f1Key())
			view := ansi.Strip(m.View())
			if !strings.Contains(view, "HELP") {
				t.Fatalf("help did not cover the %s: %q", tc.name, view)
			}
			if strings.Contains(view, tc.marker) {
				t.Fatalf("the %s shows through help: %q", tc.name, view)
			}
			m = key(m, tea.KeyMsg{Type: tea.KeyEsc})
			if m.help {
				t.Fatalf("escape did not close help over the %s", tc.name)
			}
			if !tc.restored(m) {
				t.Fatalf("closing help did not restore the %s", tc.name)
			}
		})
	}
}

// The question mark is a normal character in a draft, so it only opens help
// where the operator is not typing text.
func TestQuestionMarkOpensHelpOnlyOutsideTextEntry(t *testing.T) {
	opening := map[string]model{}
	typing := map[string]model{}

	base := boardWithTask()
	opening["board"] = base

	s := base
	s.router = true
	s.routerFocus = routerFocusRecent
	opening["request form list"] = s

	s = base
	s.agentPicker.target = "task"
	opening["agent picker"] = s

	s = base
	s.cleanup = cleanupRequest{kind: discardTask, ids: []string{"task"}, title: "work"}
	opening["discard confirmation"] = s

	s = base
	s.modelMenu = true
	s.modelStep = modelStepProvider
	opening["model menu"] = s

	s = base
	s.router = true
	s.routerDraft = "keep"
	s.routerCursor = len("keep")
	typing["request form"] = s

	s = base
	s.input = composer{kind: threadInput, target: "task", text: "keep", cursor: len("keep")}
	typing["composer"] = s

	s = base
	s.modelMenu = true
	s.modelStep = modelStepCustom
	typing["custom model entry"] = s

	s = base
	s.loginProvider = "openai"
	typing["api key entry"] = s

	for name, m := range opening {
		t.Run(name, func(t *testing.T) {
			next := key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
			if !next.help {
				t.Fatalf("? did not open help on the %s", name)
			}
		})
	}
	for name, m := range typing {
		t.Run(name, func(t *testing.T) {
			next := key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
			if next.help {
				t.Fatalf("? was stolen from the %s", name)
			}
			switch {
			case next.input.text == "keep?":
			case next.routerDraft == "keep?":
			case next.modelCustomInput == "?":
			case next.loginKey == "?":
			default:
				t.Fatalf("%s did not receive the character: %+v", name, next)
			}
		})
	}
}

func TestPastedQuestionMarkIsNotAShortcut(t *testing.T) {
	m := boardWithTask()
	m.input = composer{kind: newTaskInput, text: "keep", cursor: len("keep")}
	paste := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?"), Paste: true}
	m = key(m, paste)
	if m.help {
		t.Fatal("a pasted ? opened help")
	}
	if m.input.text != "keep?" {
		t.Fatalf("pasted text was dropped: %q", m.input.text)
	}
}

func TestHelpHintIsVisibleOnEverySurface(t *testing.T) {
	for name, m := range helpSurfaces() {
		footer := ansi.Strip(m.footer(m.width))
		if !strings.Contains(footer, "help") {
			t.Fatalf("%s footer does not advertise help: %q", name, footer)
		}
	}
}

// A click belongs to the help overlay, not to the text it covers.
func TestClicksBehindHelpDoNotStartASelection(t *testing.T) {
	m := boardWithTask()
	m.help = true
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

// Help must never confirm a destructive prompt: the overlay owns the key, and
// closing it leaves the confirmation exactly as it was.
func TestHelpDoesNotConfirmAPendingCleanup(t *testing.T) {
	m := boardWithTask()
	m.cleanup = cleanupRequest{kind: deleteTask, ids: []string{"task"}, title: "work"}
	m = key(m, f1Key())
	if !m.help {
		t.Fatal("help did not open over the confirmation")
	}
	if next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}); cmd != nil {
		t.Fatalf("help ran a cleanup command: %T", cmd())
	} else if typed := next.(model); typed.cleanup.kind != deleteTask || typed.cleanupBusy {
		t.Fatalf("help confirmed the deletion: %+v", typed.cleanup)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.cleanup.kind != deleteTask {
		t.Fatalf("closing help dropped the confirmation: %+v", m.cleanup)
	}
	if !strings.Contains(ansi.Strip(m.View()), "DELETE TASK") {
		t.Fatal("the confirmation did not come back")
	}
}
