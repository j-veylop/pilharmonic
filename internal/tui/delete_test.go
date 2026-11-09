package tui

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"orchestrator/internal/orchestrator"
)

type finishedRunner struct{}

func (finishedRunner) Run(context.Context, orchestrator.Task, io.Writer) error { return nil }

func awaitCompleted(t *testing.T, s *orchestrator.Service, id string) orchestrator.Task {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		task, ok := s.GetTask(id)
		if ok && task.Status == orchestrator.Completed {
			return task
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("task did not complete")
	return orchestrator.Task{}
}

func TestDeleteConfirmation(t *testing.T) {
	s, err := orchestrator.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTask("test task", "prompt", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := model{service: s, tasks: s.ListTasks(), width: 80, height: 24}
	update := func(key string) {
		result, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		m = result.(model)
		if cmd != nil {
			result, _ := m.Update(cmd())
			m = result.(model)
		}
	}
	update("d")
	if !strings.Contains(m.View(), "DELETE TASK + HISTORY") {
		t.Fatal("missing confirmation")
	}
	update("n")
	if _, ok := s.GetTask(task.ID); !ok {
		t.Fatal("cancel deleted task")
	}
	update("d")
	update("y")
	if _, ok := s.GetTask(task.ID); ok {
		t.Fatal("confirmed task still exists")
	}
}

func TestDeleteCompletedConfirmationAndAction(t *testing.T) {
	dir := t.TempDir()
	s, err := orchestrator.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	s.RegisterRunner("pi", finishedRunner{})
	done, err := s.CreateTask("done", "completed work", dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), done.ID); err != nil {
		t.Fatal(err)
	}
	awaitCompleted(t, s, done.ID)
	queued, err := s.CreateTask("queued", "not started", dir)
	if err != nil {
		t.Fatal(err)
	}

	m := model{service: s, tasks: s.ListTasks(), width: 120, height: 35}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("D")})
	if !strings.Contains(m.View(), "DELETE 1 COMPLETED TASK(S)") {
		t.Fatalf("missing bulk-delete confirmation: %s", m.View())
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if _, ok := s.GetTask(done.ID); !ok {
		t.Fatal("canceled bulk delete removed task")
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("D")})
	// A new completion after opening the confirmation is outside its scope.
	if err := s.Start(context.Background(), queued.ID); err != nil {
		t.Fatal(err)
	}
	awaitCompleted(t, s, queued.ID)
	m = runCleanup(t, m)
	if _, ok := s.GetTask(done.ID); ok {
		t.Fatal("completed task still exists")
	}
	if _, ok := s.GetTask(queued.ID); !ok {
		t.Fatal("task completed after confirmation opened was deleted")
	}
	if !strings.Contains(m.notice, "Deleted 1 completed task") {
		t.Fatalf("deletion not reported: %q", m.notice)
	}
}

func TestFollowUpComposerRecordsContext(t *testing.T) {
	dir := t.TempDir()
	s, err := orchestrator.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	task, err := s.CreateTask("work", "original mandate", dir)
	if err != nil {
		t.Fatal(err)
	}
	m := model{service: s, workspace: dir, tasks: s.ListTasks(), width: 120, height: 35}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("F")})
	if !strings.Contains(m.View(), "FOLLOW UP") {
		t.Fatalf("follow-up composer not shown: %s", m.View())
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("also update docs")})
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	got, _ := s.GetTask(task.ID)
	if got.Prompt != "original mandate" || len(got.Context) != 1 || got.Context[0].Text != "also update docs" {
		t.Fatalf("follow-up not recorded correctly: %+v", got)
	}
}

func runCleanup(t *testing.T, m model) model {
	t.Helper()
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if cmd == nil || !next.(model).cleanupBusy {
		t.Fatal("confirmation did not queue cleanup")
	}
	next, _ = next.Update(cmd())
	return next.(model)
}

// Hold the returned command so the test can navigate while cleanup is pending.
func TestPendingCleanupKeepsTargetAndAllowsNavigation(t *testing.T) {
	s, err := orchestrator.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	for _, title := range []string{"one", "two"} {
		if _, err := s.CreateTask(title, title, t.TempDir()); err != nil {
			t.Fatal(err)
		}
	}
	m := model{service: s, tasks: s.ListTasks(), width: 80, height: 24}
	id := m.selectedID()
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if cmd == nil {
		t.Fatal("missing cleanup command")
	}
	m = key(next.(model), tea.KeyMsg{Type: tea.KeyDown})
	other := m.selectedID()
	if id == other {
		t.Fatal("navigation blocked")
	}
	if _, ok := s.GetTask(id); !ok {
		t.Fatal("task was deleted in Update")
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("D")})
	if m.cleanup.kind != "" {
		t.Fatal("opened a second cleanup during the pending command")
	}
	next, _ = m.Update(cmd())
	m = next.(model)
	if m.cleanupBusy || m.selectedID() != other || len(m.tasks) != 1 {
		t.Fatalf("unexpected selection or completion: %+v", m)
	}
}
