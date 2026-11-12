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
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	file := filepath.Join(workspace, "keep.txt")
	if err := os.WriteFile(file, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateTask("a", "first", workspace)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateTask("b", "second", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddContext(a.ID, "context-one", "information"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTask(a.ID); err != nil {
		t.Fatal(err)
	}
	if contents, err := os.ReadFile(file); err != nil || string(contents) != "keep" {
		t.Fatalf("workspace changed: %q, %v", contents, err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
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
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	s.RegisterRunner("general", runnerFunc(func(ctx context.Context, _ Task, _ io.Writer) error { close(started); <-ctx.Done(); return ctx.Err() }))
	task, err := s.CreateTask("a", "prompt", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	<-started
	if err = s.DeleteTask(task.ID); err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("delete running: %v", err)
	}
	if err = s.Cancel(task.ID); err != nil {
		t.Fatal(err)
	}
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
	if err = s.DeleteTask(task.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Shutdown(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestDeleteRefusesParentWithSubagents(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()
	started := make(chan struct{}, 2)
	s.RegisterRunner("general", runnerFunc(func(ctx context.Context, task Task, _ io.Writer) error {
		started <- struct{}{}
		if task.ParentID == "" {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}))
	parent, err := s.CreateTask("parent", "parent mandate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), parent.ID); err != nil {
		t.Fatal(err)
	}
	<-started
	child, err := s.SpawnSubagent(parent.ID, "child", "child mandate", "")
	if err != nil {
		t.Fatal(err)
	}
	await(t, s, child.ID, Completed)
	if err = s.Cancel(parent.ID); err != nil {
		t.Fatal(err)
	}
	await(t, s, parent.ID, Canceled)
	if err = s.DeleteTask(parent.ID); err == nil || !strings.Contains(err.Error(), "subagents") {
		t.Fatalf("parent with subagent deleted: %v", err)
	}
	if err = s.DeleteTask(child.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteTask(parent.ID); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteCompletedTasksKeepsIncompleteWorkAndWorkspace(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()
	workspace := t.TempDir()
	file := filepath.Join(workspace, "keep.txt")
	if err := os.WriteFile(file, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	done, err := s.CreateTask("done", "completed work", workspace)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := s.CreateTask("parent", "completed parent", workspace)
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.CreateTask("child", "completed child", workspace)
	if err != nil {
		t.Fatal(err)
	}
	blockedParent, err := s.CreateTask("blocked parent", "completed parent with failed child", workspace)
	if err != nil {
		t.Fatal(err)
	}
	blockedChild, err := s.CreateTask("failed child", "failed child", workspace)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := s.CreateTask("queued", "still queued", workspace)
	if err != nil {
		t.Fatal(err)
	}

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
	deleted, err := s.DeleteCompletedTasks(s.CompletedDeletionCandidates())
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

	reloaded, err := Open(s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reloaded.Shutdown() }()
	if count := len(reloaded.CompletedDeletionCandidates()); count != 0 {
		t.Fatalf("deleted tasks restored: %d deletable", count)
	}
	if _, ok := reloaded.GetTask(blockedParent.ID); !ok {
		t.Fatal("incomplete parent missing after reload")
	}
}

func TestDeleteCompletedTasksRollsBackFailedSave(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()
	task, err := s.CreateTask("done", "completed work", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.data.Tasks[0].Status = Completed
	s.mu.Unlock()
	savedPath := s.path
	s.path = t.TempDir()
	if _, err = s.DeleteCompletedTasks(s.CompletedDeletionCandidates()); err == nil {
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
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		return os.WriteFile(filepath.Join(task.Workspace, "prepared.txt"), []byte("prepared"), 0600)
	}))
	task, err := s.CreateTask("review", "prepare work", repo)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetTaskIsolation(task.ID, true); err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
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
