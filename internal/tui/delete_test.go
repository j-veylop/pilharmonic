package tui

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/j-veylop/pilharmonic/internal/orchestrator"
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

// Single and bulk deletes ask first, cancel cleanly, and a bulk delete covers
// only the tasks that were complete when its confirmation opened.
func TestDeleteConfirmations(t *testing.T) {
	dir := t.TempDir()
	s := newTestService(t)
	s.RegisterRunner("general", finishedRunner{})
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
	single, err := s.CreateTask("single", "prompt", dir)
	if err != nil {
		t.Fatal(err)
	}

	m := model{service: s, tasks: s.ListTasks(), width: 120, height: 35}
	selectTask(t, &m, single.ID)
	m = key(m, runeKey("d"))
	if !strings.Contains(m.View(), "DELETE TASK + HISTORY") {
		t.Fatal("missing confirmation")
	}
	m = key(m, runeKey("n"))
	if _, ok := s.GetTask(single.ID); !ok {
		t.Fatal("cancel deleted task")
	}
	m = key(m, runeKey("d"))
	m = runCleanup(t, m)
	if _, ok := s.GetTask(single.ID); ok {
		t.Fatal("confirmed task still exists")
	}

	m = key(m, runeKey("D"))
	if !strings.Contains(m.View(), "DELETE 1 COMPLETED TASK(S)") {
		t.Fatalf("missing bulk-delete confirmation: %s", m.View())
	}
	m = key(m, runeKey("n"))
	if _, ok := s.GetTask(done.ID); !ok {
		t.Fatal("canceled bulk delete removed task")
	}
	m = key(m, runeKey("D"))
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
	if !strings.Contains(m.notice.text, "Deleted 1 completed task") {
		t.Fatalf("deletion not reported: %q", m.notice.text)
	}
}

func TestFollowUpComposerRecordsContext(t *testing.T) {
	dir := t.TempDir()
	s, err := orchestrator.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()
	task, err := s.CreateTask("work", "original mandate", dir)
	if err != nil {
		t.Fatal(err)
	}
	m := model{service: s, workspace: dir, tasks: s.ListTasks(), width: 120, height: 35}
	m = key(m, runeKey("f"))
	if m.input.kind != followUpInput {
		t.Fatalf("follow-up composer not shown: %s", m.View())
	}
	m = key(m, runeKey("also update docs"))
	key(m, tea.KeyMsg{Type: tea.KeyEnter})
	got, _ := s.GetTask(task.ID)
	if got.Prompt != "original mandate" || len(got.Context) != 1 || got.Context[0].Text != "also update docs" {
		t.Fatalf("follow-up not recorded correctly: %+v", got)
	}
}

func runCleanup(t *testing.T, m model) model {
	t.Helper()
	next, cmd := m.Update(runeKey("y"))
	if cmd == nil || !next.(model).cleanupBusy {
		t.Fatal("confirmation did not queue cleanup")
	}
	next, _ = next.Update(cmd())
	return next.(model)
}

// Hold the returned command so the test can navigate while cleanup is pending.
func TestPendingCleanupKeepsTargetAndAllowsNavigation(t *testing.T) {
	s := newTestService(t)
	for _, title := range []string{"one", "two"} {
		if _, err := s.CreateTask(title, title, t.TempDir()); err != nil {
			t.Fatal(err)
		}
	}
	m := model{service: s, tasks: s.ListTasks(), width: 80, height: 24}
	id := m.selectedID()
	m = key(m, runeKey("d"))
	next, cmd := m.Update(runeKey("y"))
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
	m = key(m, runeKey("D"))
	if m.cleanup.kind != "" {
		t.Fatal("opened a second cleanup during the pending command")
	}
	next, _ = m.Update(cmd())
	m = next.(model)
	if m.cleanupBusy || m.selectedID() != other || len(m.tasks) != 1 {
		t.Fatalf("unexpected selection or completion: %+v", m)
	}
}

func TestDiscardWorktreeConfirmation(t *testing.T) {
	repo := reviewRepo(t)
	s := newTestService(t)
	s.RegisterRunner("general", reviewChangeRunner{})
	done := startIsolatedReviewTask(t, s, repo, "discard")
	// Merge is blocked while the review is pending, so discard requires the
	// operator to approve and wait for integration first.
	done = approveReviewAndAwaitMerge(t, s, done)

	m := model{service: s, tasks: s.ListTasks(), width: 120, height: 35}
	selectTask(t, &m, done.ID)
	m = key(m, runeKey("x"))
	if !strings.Contains(m.View(), "DISCARD RETAINED WORKTREE") {
		t.Fatalf("missing discard confirmation: %s", m.View())
	}
	m = key(m, runeKey("n"))
	if _, err := os.Stat(done.ExecutionWorkspace); err != nil {
		t.Fatalf("canceled discard removed the worktree: %v", err)
	}

	m = key(m, runeKey("x"))
	m = runCleanup(t, m)
	if _, err := os.Stat(done.ExecutionWorkspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("confirmed discard left the worktree: %v", err)
	}
	if !strings.Contains(m.notice.text, "Worktree discarded") {
		t.Fatalf("discard not acknowledged: %q", m.notice.text)
	}
	if _, ok := s.GetTask(done.ID); !ok {
		t.Fatal("discard removed the task")
	}
}

func TestDiscardAllMergedWorktreesConfirmation(t *testing.T) {
	repo := reviewRepo(t)
	s := newTestService(t)
	s.RegisterRunner("general", reviewChangeRunner{})
	first := startIsolatedReviewTask(t, s, repo, "first")
	first = approveReviewAndAwaitMerge(t, s, first)
	second := startIsolatedReviewTask(t, s, repo, "second")
	second = approveReviewAndAwaitMerge(t, s, second)

	m := model{service: s, tasks: s.ListTasks(), width: 120, height: 35}
	m = key(m, runeKey("X"))
	if !strings.Contains(m.View(), "DISCARD 2 MERGED WORKTREE(S)") {
		t.Fatalf("missing bulk-discard confirmation: %s", m.View())
	}
	later := startIsolatedReviewTask(t, s, repo, "later")
	m = runCleanup(t, m)
	for _, task := range []orchestrator.Task{first, second} {
		if _, err := os.Stat(task.ExecutionWorkspace); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("worktree %s still exists: %v", task.ID, err)
		}
	}
	if _, err := os.Stat(later.ExecutionWorkspace); err != nil {
		t.Fatalf("bulk discard included a later worktree: %v", err)
	}
	if !strings.Contains(m.notice.text, "Discarded 2 merged worktree(s)") {
		t.Fatalf("bulk discard not acknowledged: %q", m.notice.text)
	}
}
