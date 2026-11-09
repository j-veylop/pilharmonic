package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"orchestrator/internal/orchestrator"
)

func TestDiscardWorktreeConfirmation(t *testing.T) {
	repo := reviewRepo(t)
	s, err := orchestrator.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	s.RegisterRunner("pi", reviewChangeRunner{})
	done := startIsolatedReviewTask(t, s, repo, "discard")

	m := model{service: s, tasks: s.ListTasks(), width: 120, height: 35}
	selectTask(t, &m, done.ID)
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if !strings.Contains(m.View(), "DISCARD RETAINED WORKTREE") {
		t.Fatalf("missing discard confirmation: %s", m.View())
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if _, err = os.Stat(done.ExecutionWorkspace); err != nil {
		t.Fatalf("canceled discard removed the worktree: %v", err)
	}

	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if _, err = os.Stat(done.ExecutionWorkspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("confirmed discard left the worktree: %v", err)
	}
	if !strings.Contains(m.notice, "Worktree discarded") {
		t.Fatalf("discard not acknowledged: %q", m.notice)
	}
	if _, ok := s.GetTask(done.ID); !ok {
		t.Fatal("discard removed the task")
	}
}

func TestDiscardAllMergedWorktreesConfirmation(t *testing.T) {
	repo := reviewRepo(t)
	s, err := orchestrator.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	s.RegisterRunner("pi", reviewChangeRunner{})
	first := startIsolatedReviewTask(t, s, repo, "first")
	second := startIsolatedReviewTask(t, s, repo, "second")

	m := model{service: s, tasks: s.ListTasks(), width: 120, height: 35}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("X")})
	if !strings.Contains(m.View(), "DISCARD 2 MERGED WORKTREE(S)") {
		t.Fatalf("missing bulk-discard confirmation: %s", m.View())
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	for _, task := range []orchestrator.Task{first, second} {
		if _, err = os.Stat(task.ExecutionWorkspace); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("worktree %s still exists: %v", task.ID, err)
		}
	}
	if !strings.Contains(m.notice, "Discarded 2 merged worktree(s)") {
		t.Fatalf("bulk discard not acknowledged: %q", m.notice)
	}
}
