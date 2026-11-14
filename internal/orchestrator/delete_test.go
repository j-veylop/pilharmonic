package orchestrator

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeleteTaskPreservesOtherTasksAndWorkspace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := mustGet(Open(path))(t)
	workspace := t.TempDir()
	file := filepath.Join(workspace, "keep.txt")
	must(t, os.WriteFile(file, []byte("keep"), 0600))
	a := createTask(t, s, "a", "first", workspace)
	b := createTask(t, s, "b", "second", workspace)
	must(t, s.AddContext(a.ID, "context-one", "information"))
	must(t, s.DeleteTask(a.ID))
	if contents, err := os.ReadFile(file); err != nil || string(contents) != "keep" {
		t.Fatalf("workspace changed: %q, %v", contents, err)
	}
	s2 := mustGet(Open(path))(t)
	if _, ok := s2.GetTask(a.ID); ok {
		t.Fatal("deleted task restored")
	}
	if _, ok := s2.GetTask(b.ID); !ok {
		t.Fatal("other task missing")
	}
	if len(s2.data.Threads) != 1 || s2.data.Threads[0].ID != b.ThreadID {
		t.Fatalf("wrong threads: %+v", s2.data.Threads)
	}
	if err := s.DeleteTask(a.ID); err == nil {
		t.Fatal("missing task deletion succeeded")
	}
}

func TestDeleteRefusesRunningAndRollsBackFailedSave(t *testing.T) {
	var err error
	s := mustGet(Open(filepath.Join(t.TempDir(), "state.json")))(t)
	started := make(chan struct{})
	s.RegisterRunner("general", runnerFunc(func(ctx context.Context, _ Task, _ io.Writer) error { close(started); <-ctx.Done(); return ctx.Err() }))
	task := createTask(t, s, "a", "prompt", t.TempDir())
	must(t, s.Start(context.Background(), task.ID))
	<-started
	if err = s.DeleteTask(task.ID); err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("delete running: %v", err)
	}
	must(t, s.Cancel(task.ID))
	await(t, s, task.ID, Canceled)
	savedPath := s.path
	s.path = t.TempDir()
	if err = s.DeleteTask(task.ID); err == nil {
		t.Fatal("failed save succeeded")
	}
	if _, ok := s.GetTask(task.ID); !ok || len(s.data.Threads) != 1 {
		t.Fatal("failed save did not restore state")
	}
	s.path = savedPath
	must(t, s.DeleteTask(task.ID))
	if err = s.Shutdown(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestDeleteRefusesParentWithSubagents(t *testing.T) {
	var err error
	s := newTestService(t)
	started := make(chan struct{}, 2)
	s.RegisterRunner("general", runnerFunc(func(ctx context.Context, task Task, _ io.Writer) error {
		started <- struct{}{}
		if task.ParentID == "" {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}))
	parent := createTask(t, s, "parent", "parent mandate", t.TempDir())
	must(t, s.Start(context.Background(), parent.ID))
	<-started
	child := mustGet(s.SpawnSubagent(parent.ID, "child", "child mandate", ""))(t)
	await(t, s, child.ID, Completed)
	must(t, s.Cancel(parent.ID))
	await(t, s, parent.ID, Canceled)
	if err = s.DeleteTask(parent.ID); err == nil || !strings.Contains(err.Error(), "subagents") {
		t.Fatalf("parent with subagent deleted: %v", err)
	}
	must(t, s.DeleteTask(child.ID))
	must(t, s.DeleteTask(parent.ID))
}

func TestDeleteCompletedTasksKeepsIncompleteWorkAndWorkspace(t *testing.T) {
	s := newTestService(t)
	workspace := t.TempDir()
	file := filepath.Join(workspace, "keep.txt")
	must(t, os.WriteFile(file, []byte("keep"), 0600))
	done := createTask(t, s, "done", "completed work", workspace)
	parent := createTask(t, s, "parent", "completed parent", workspace)
	child := createTask(t, s, "child", "completed child", workspace)
	blockedParent := createTask(t, s, "blocked parent", "completed parent with failed child", workspace)
	blockedChild := createTask(t, s, "failed child", "failed child", workspace)
	queued := createTask(t, s, "queued", "still queued", workspace)

	s.mu.Lock()
	for i := range s.data.Tasks {
		task := &s.data.Tasks[i]
		switch task.ID {
		case done.ID, parent.ID, child.ID, blockedParent.ID:
			task.Status = Completed
		case blockedChild.ID:
			task.Status = Failed
			task.ParentID = blockedParent.ID
		}
		if task.ID == child.ID {
			task.ParentID = parent.ID
		}
	}
	s.mu.Unlock()

	if count := len(s.CompletedDeletionCandidates()); count != 3 {
		t.Fatalf("deletable count = %d, want 3", count)
	}
	deleted, _, err := s.DeleteCompletedTasksDetailed(s.CompletedDeletionCandidates())
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 3 {
		t.Fatalf("deleted %d tasks, want 3", deleted)
	}
	for _, id := range []string{done.ID, parent.ID, child.ID} {
		if _, ok := s.GetTask(id); ok {
			t.Fatalf("completed task %s was not deleted", id)
		}
	}
	for _, id := range []string{blockedParent.ID, blockedChild.ID, queued.ID} {
		if _, ok := s.GetTask(id); !ok {
			t.Fatalf("incomplete task %s was deleted", id)
		}
	}
	if contents, err := os.ReadFile(file); err != nil || string(contents) != "keep" {
		t.Fatalf("workspace changed: %q, %v", contents, err)
	}
	s.mu.Lock()
	remainingThreads := len(s.data.Threads)
	s.mu.Unlock()
	if remainingThreads != 3 {
		t.Fatalf("threads = %d, want 3 for remaining tasks", remainingThreads)
	}

	reloaded := mustGet(Open(s.path))(t)
	defer func() { _ = reloaded.Shutdown() }()
	if count := len(reloaded.CompletedDeletionCandidates()); count != 0 {
		t.Fatalf("deleted tasks restored: %d deletable", count)
	}
	if _, ok := reloaded.GetTask(blockedParent.ID); !ok {
		t.Fatal("incomplete parent missing after reload")
	}
}

func TestDeleteCompletedTasksRollsBackFailedSave(t *testing.T) {
	var err error
	s := newTestService(t)
	task := createTask(t, s, "done", "completed work", t.TempDir())
	s.mu.Lock()
	s.data.Tasks[0].Status = Completed
	s.mu.Unlock()
	savedPath := s.path
	s.path = t.TempDir()
	if _, _, err = s.DeleteCompletedTasksDetailed(s.CompletedDeletionCandidates()); err == nil {
		t.Fatal("failed save succeeded")
	}
	s.path = savedPath
	if _, ok := s.GetTask(task.ID); !ok {
		t.Fatal("failed save did not restore tasks")
	}
	if count := len(s.CompletedDeletionCandidates()); count != 1 {
		t.Fatalf("failed save changed deletion eligibility: %d", count)
	}
}

// A prepared commit that nobody has reviewed must survive bulk deletion: the
// worktree holds the only copy of that work.
func TestDeleteCompletedTasksKeepsUnreviewedPreparedWork(t *testing.T) {
	repo := committedRepo(t)
	s := newTestService(t)
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		return os.WriteFile(filepath.Join(task.Workspace, "prepared.txt"), []byte("prepared"), 0600)
	}))
	task := startIsolated(t, s, "review", "prepare work", repo)
	done := await(t, s, task.ID, Completed)
	if !done.AwaitingReview() || done.WorktreeCommit == "" {
		t.Fatalf("task did not stop at review: %+v", done)
	}
	for _, id := range s.CompletedDeletionCandidates() {
		if id == task.ID {
			t.Fatal("unreviewed prepared work was offered for bulk deletion")
		}
	}
	deleted, skipped, err := s.DeleteCompletedTasksDetailed([]string{task.ID})
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 0 {
		t.Fatalf("deleted %d task(s) with unreviewed work", deleted)
	}
	if len(skipped) != 1 || skipped[0].ID != task.ID || !strings.Contains(skipped[0].Reason, "review") {
		t.Fatalf("skipped deletion not explained: %+v", skipped)
	}
	if _, err = os.Stat(done.ExecutionWorkspace); err != nil {
		t.Fatalf("prepared worktree was removed: %v", err)
	}
	if _, ok := s.GetTask(task.ID); !ok {
		t.Fatal("task with unreviewed work was deleted")
	}
}
