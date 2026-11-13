package tui

import (
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"pilharmonic/internal/orchestrator"
)

// runeKey builds the key message for a typed character.
func runeKey(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// key applies a key press and drains the commands that carry service results so
// tests observe state after an asynchronous action completes. It deliberately
// ignores ticks and quit commands.
func key(m model, msg tea.KeyMsg) model {
	next, cmd := m.Update(msg)
	m = next.(model)
	for i := 0; cmd != nil && i < 8; i++ {
		switch result := cmd().(type) {
		case actionResultMsg, composerResultMsg, routerResultMsg, cleanupResultMsg, isolationResultMsg, selectionCopiedMsg, fetchModelsMsg:
			next, cmd = m.Update(result)
			m = next.(model)
		default:
			return m
		}
	}
	return m
}

// newTestService opens a state file in a temp dir and closes it when the test
// ends, so a test never leaves a service running.
func newTestService(t *testing.T) *orchestrator.Service {
	t.Helper()
	s, err := orchestrator.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Shutdown() })
	return s
}

// assertFits fails when a rendered surface exceeds the terminal's cell box.
func assertFits(t *testing.T, view string, w, h int) {
	t.Helper()
	if lipgloss.Width(view) > w || lipgloss.Height(view) > h {
		t.Fatalf("%dx%d: rendered %dx%d", w, h, lipgloss.Width(view), lipgloss.Height(view))
	}
}
