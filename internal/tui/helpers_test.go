package tui

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/j-veylop/pilharmonic/internal/orchestrator"
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
		case actionResultMsg, composerResultMsg, routerResultMsg, cleanupResultMsg, isolationResultMsg, selectionCopiedMsg, fetchModelsMsg, skillToggleMsg, lspToggleMsg:
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

// addTestProfile registers a profile through the production config loader,
// the only path that adds profiles now.
func addTestProfile(t *testing.T, s *orchestrator.Service, definition string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(path, []byte(`{"profiles":[`+definition+`]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.LoadProfiles(path); err != nil {
		t.Fatal(err)
	}
}

// assertFits fails when a rendered surface exceeds the terminal's cell box.
func assertFits(t *testing.T, view string, w, h int) {
	t.Helper()
	if lipgloss.Width(view) > w || lipgloss.Height(view) > h {
		t.Fatalf("%dx%d: rendered %dx%d", w, h, lipgloss.Width(view), lipgloss.Height(view))
	}
}
