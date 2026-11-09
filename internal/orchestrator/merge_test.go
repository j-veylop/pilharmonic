package orchestrator

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestIsolatedCompletionAutoMergesChanges(t *testing.T) {
	repo := committedRepo(t)
	head := testGit(t, repo, "rev-parse", "HEAD")
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	s.RegisterRunner("pi", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		if err := os.WriteFile(filepath.Join(task.Workspace, "artifact.txt"), []byte("from worktree\n"), 0600); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(task.Workspace, "tracked.txt"), []byte("changed in worktree\n"), 0600)
	}))
	task, err := s.CreateTask("merge", "make changes", repo)
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
	if done.MergeStatus != MergeMerged || done.MergeCommit == "" || done.WorktreeCommit == "" || done.MergedAt.IsZero() {
		t.Fatalf("merge result not recorded: %+v", done)
	}
	b, err := os.ReadFile(filepath.Join(repo, "artifact.txt"))
	if err != nil || string(b) != "from worktree\n" {
		t.Fatalf("artifact not merged: %q, %v", b, err)
	}
	b, err = os.ReadFile(filepath.Join(repo, "tracked.txt"))
	if err != nil || string(b) != "changed in worktree\n" {
		t.Fatalf("tracked change not merged: %q, %v", b, err)
	}
	if got := testGit(t, repo, "rev-parse", "HEAD"); got != done.MergeCommit || got == head {
		t.Fatalf("source HEAD %q, merge commit %q, initial %q", got, done.MergeCommit, head)
	}
	if status := testGit(t, repo, "status", "--porcelain"); status != "" {
		t.Fatalf("source checkout is dirty after merge: %q", status)
	}
	if parents := strings.Fields(testGit(t, repo, "rev-list", "--parents", "-n", "1", "HEAD")); len(parents) != 3 {
		t.Fatalf("expected a merge commit, parents: %v", parents)
	}
	if _, err = os.Stat(done.ExecutionWorkspace); err != nil {
		t.Fatalf("worktree was removed after merge: %v", err)
	}
}

func TestFailedIsolatedAttemptIsNotMerged(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	s.RegisterRunner("pi", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		if err := os.WriteFile(filepath.Join(task.Workspace, "failed.txt"), []byte("do not merge"), 0600); err != nil {
			return err
		}
		return errors.New("runner failed")
	}))
	task, err := s.CreateTask("failed", "do work", repo)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetTaskIsolation(task.ID, true); err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	done := await(t, s, task.ID, Failed)
	if done.MergeStatus != "" {
		t.Fatalf("failed attempt recorded a merge: %+v", done)
	}
	if _, err = os.Stat(filepath.Join(repo, "failed.txt")); !os.IsNotExist(err) {
		t.Fatalf("failed attempt was merged: %v", err)
	}
}

func TestIsolatedCompletionWithoutChangesIsUpToDate(t *testing.T) {
	repo := committedRepo(t)
	head := testGit(t, repo, "rev-parse", "HEAD")
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	s.RegisterRunner("pi", runnerFunc(func(context.Context, Task, io.Writer) error { return nil }))
	task, err := s.CreateTask("noop", "do nothing", repo)
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
	if done.MergeStatus != MergeUpToDate || done.MergeCommit != head || done.WorktreeCommit != head {
		t.Fatalf("unexpected merge result: %+v", done)
	}
	if got := testGit(t, repo, "rev-parse", "HEAD"); got != head {
		t.Fatalf("source HEAD changed for an empty worktree: %q", got)
	}
}

func TestIsolatedMergeConflictIsRecordedAndAborted(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	s.RegisterRunner("pi", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		if err := os.WriteFile(filepath.Join(task.Workspace, "tracked.txt"), []byte("worktree change\n"), 0600); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("source change\n"), 0600); err != nil {
			return err
		}
		testGit(t, repo, "add", "tracked.txt")
		testGit(t, repo, "commit", "-q", "-m", "advance source")
		return nil
	}))
	task, err := s.CreateTask("conflict", "conflicting change", repo)
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
	if done.MergeStatus != MergeFailed || !strings.Contains(done.MergeError, "merge isolated worktree") || done.WorktreeCommit == "" {
		t.Fatalf("merge failure not recorded: %+v", done)
	}
	if !done.MergeConflict() {
		t.Fatalf("conflict was not classified: %q", done.MergeError)
	}
	if done.Stage() != "merge failed" {
		t.Fatalf("merge failure does not need attention: %q", done.Stage())
	}
	b, err := os.ReadFile(filepath.Join(repo, "tracked.txt"))
	if err != nil || string(b) != "source change\n" {
		t.Fatalf("source was not restored after conflict: %q, %v", b, err)
	}
	cmd := exec.Command("git", "rev-parse", "-q", "--verify", "MERGE_HEAD")
	cmd.Dir = repo
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("source left in a merge: %s", strings.TrimSpace(string(output)))
	}
	b, err = os.ReadFile(filepath.Join(done.ExecutionWorkspace, "tracked.txt"))
	if err != nil || string(b) != "worktree change\n" {
		t.Fatalf("worktree was not retained: %q, %v", b, err)
	}
	if err = s.Retry(task.ID); err != nil {
		t.Fatalf("merge failure cannot be retried: %v", err)
	}
	retried, _ := s.GetTask(task.ID)
	if retried.Status != Queued || retried.MergeStatus != "" {
		t.Fatalf("merge failure retry did not clear the result: %+v", retried)
	}
}

func TestConcurrentIsolatedCompletionsMergeSerially(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	var mu sync.Mutex
	seen := map[string]bool{}
	s.RegisterRunner("pi", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		mu.Lock()
		seen[task.ID] = true
		mu.Unlock()
		return os.WriteFile(filepath.Join(task.Workspace, "artifact-"+task.ID+".txt"), []byte(task.ID), 0600)
	}))
	var tasks []Task
	for _, title := range []string{"first", "second"} {
		task, err := s.CreateTask(title, "work", repo)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.SetTaskIsolation(task.ID, true); err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
	}
	for _, task := range tasks {
		if err := s.Start(context.Background(), task.ID); err != nil {
			t.Fatal(err)
		}
	}
	for _, task := range tasks {
		done := await(t, s, task.ID, Completed)
		if done.MergeStatus != MergeMerged && done.MergeStatus != MergeUpToDate {
			t.Fatalf("merge did not succeed for %s: %+v", task.ID, done)
		}
		if _, err := os.Stat(filepath.Join(repo, "artifact-"+task.ID+".txt")); err != nil {
			t.Fatalf("artifact for %s missing from source: %v", task.ID, err)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("expected both runners to execute, saw %v", seen)
	}
}

func TestCanceledIsolatedAttemptIsNotMerged(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	started := make(chan struct{})
	s.RegisterRunner("pi", runnerFunc(func(ctx context.Context, task Task, _ io.Writer) error {
		if err := os.WriteFile(filepath.Join(task.Workspace, "canceled.txt"), []byte("do not merge"), 0600); err != nil {
			return err
		}
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}))
	task, err := s.CreateTask("canceled", "do work", repo)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetTaskIsolation(task.ID, true); err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	<-started
	if err = s.Cancel(task.ID); err != nil {
		t.Fatal(err)
	}
	done := await(t, s, task.ID, Canceled)
	if done.MergeStatus != "" {
		t.Fatalf("canceled attempt recorded a merge: %+v", done)
	}
	if _, err = os.Stat(filepath.Join(repo, "canceled.txt")); !os.IsNotExist(err) {
		t.Fatalf("canceled attempt was merged: %v", err)
	}
}
