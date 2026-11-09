package orchestrator

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mergedIsolatedTask runs an isolated task that changes its worktree, so the
// automatic merge records a worktree and merge commit and leaves a retained
// worktree.
func mergedIsolatedTask(t *testing.T, s *Service, repo, title string) Task {
	t.Helper()
	task, err := s.CreateTask(title, "do work", repo)
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
	if done.MergeStatus != MergeMerged || done.ExecutionWorkspace == "" {
		t.Fatalf("task did not merge or has no worktree: %+v", done)
	}
	return done
}

func changeRunner(_ context.Context, task Task, _ io.Writer) error {
	return os.WriteFile(filepath.Join(task.Workspace, "artifact-"+task.ID+".txt"), []byte(task.ID+"\n"), 0600)
}

func TestDiscardWorktreeRemovesMergedWorktreeOnly(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	s.RegisterRunner("pi", runnerFunc(changeRunner))
	done := mergedIsolatedTask(t, s, repo, "discard")
	tree := done.ExecutionWorkspace
	reservation := filepath.Dir(tree)
	sourceHead := testGit(t, repo, "rev-parse", "HEAD")

	if err := s.DiscardWorktree(done.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree still exists: %v", err)
	}
	if _, err := os.Stat(reservation); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree reservation still exists: %v", err)
	}
	got, ok := s.GetTask(done.ID)
	if !ok {
		t.Fatal("discarding removed the task")
	}
	if got.ExecutionWorkspace != "" || got.IsolationCommit != "" {
		t.Fatalf("discarded worktree is still recorded: %+v", got)
	}
	discarded := false
	for _, event := range got.Events {
		if event.Kind == "worktree_discarded" && strings.Contains(event.Message, tree) {
			discarded = true
		}
	}
	if !discarded {
		t.Fatalf("discard event missing: %+v", got.Events)
	}
	if got.MergeCommit == "" || got.WorktreeCommit == "" || got.ReviewCommit == "" {
		t.Fatalf("discard removed integration history: %+v", got)
	}
	if head := testGit(t, repo, "rev-parse", "HEAD"); head != sourceHead {
		t.Fatalf("discard changed source HEAD: %s, want %s", head, sourceHead)
	}
	if list := testGit(t, repo, "worktree", "list"); strings.Contains(list, tree) {
		t.Fatalf("git still lists the discarded worktree: %s", list)
	}
	if err := s.DiscardWorktree(done.ID); err == nil || !strings.Contains(err.Error(), "no retained isolated worktree") {
		t.Fatalf("second discard error = %v", err)
	}
}

func TestDiscardWorktreeRefusesDirtyWorktree(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	s.RegisterRunner("pi", runnerFunc(changeRunner))
	done := mergedIsolatedTask(t, s, repo, "dirty")
	file := filepath.Join(done.ExecutionWorkspace, "local.txt")
	if err := os.WriteFile(file, []byte("not committed\n"), 0600); err != nil {
		t.Fatal(err)
	}

	err := s.DiscardWorktree(done.ID)
	if err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("dirty discard error = %v", err)
	}
	if _, statErr := os.Stat(done.ExecutionWorkspace); statErr != nil {
		t.Fatalf("dirty worktree was removed: %v", statErr)
	}
	got, _ := s.GetTask(done.ID)
	if got.ExecutionWorkspace != done.ExecutionWorkspace {
		t.Fatalf("refused discard cleared the workspace: %+v", got)
	}

	if err = os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err = s.DiscardWorktree(done.ID); err != nil {
		t.Fatalf("clean discard: %v", err)
	}
	if _, statErr := os.Stat(done.ExecutionWorkspace); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("clean worktree still exists: %v", statErr)
	}
}

func TestDiscardWorktreeRefusesUnmergedWorktree(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	s.RegisterRunner("pi", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		if err := changeRunner(context.Background(), task, io.Discard); err != nil {
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
	failed := await(t, s, task.ID, Failed)
	if failed.ExecutionWorkspace == "" {
		t.Fatal("failed attempt has no prepared worktree")
	}

	err = s.DiscardWorktree(failed.ID)
	if err == nil || !strings.Contains(err.Error(), "has not been merged") {
		t.Fatalf("unmerged discard error = %v", err)
	}
	if _, statErr := os.Stat(failed.ExecutionWorkspace); statErr != nil {
		t.Fatalf("unmerged worktree was removed: %v", statErr)
	}
}

func TestDiscardWorktreeRefusesRunningTask(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	s.RegisterRunner("pi", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		started <- struct{}{}
		<-release
		return changeRunner(context.Background(), task, io.Discard)
	}))
	task, err := s.CreateTask("running", "do work", repo)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetTaskIsolation(task.ID, true); err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("runner did not start")
	}
	if err = s.DiscardWorktree(task.ID); err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("running discard error = %v", err)
	}
	close(release)
	await(t, s, task.ID, Completed)
}

func TestDiscardMergedWorktreesSkipsRefusedWorktrees(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	s.RegisterRunner("pi", runnerFunc(changeRunner))
	first := mergedIsolatedTask(t, s, repo, "first")
	second := mergedIsolatedTask(t, s, repo, "second")
	dirty := filepath.Join(first.ExecutionWorkspace, "local.txt")
	if err := os.WriteFile(dirty, []byte("not committed\n"), 0600); err != nil {
		t.Fatal(err)
	}

	if count := s.DeletableWorktrees(); count != 2 {
		t.Fatalf("candidate count = %d, want 2", count)
	}
	removed, err := s.DiscardMergedWorktrees()
	if removed != 1 || err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("bulk discard = %d, %v", removed, err)
	}
	if _, statErr := os.Stat(first.ExecutionWorkspace); statErr != nil {
		t.Fatalf("dirty worktree was removed: %v", statErr)
	}
	if _, statErr := os.Stat(second.ExecutionWorkspace); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("clean worktree still exists: %v", statErr)
	}

	if err = os.Remove(dirty); err != nil {
		t.Fatal(err)
	}
	removed, err = s.DiscardMergedWorktrees()
	if removed != 1 || err != nil {
		t.Fatalf("second bulk discard = %d, %v", removed, err)
	}
	if count := s.DeletableWorktrees(); count != 0 {
		t.Fatalf("remaining candidate count = %d", count)
	}
}

func TestDeleteTaskRemovesRetainedWorktree(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	s.RegisterRunner("pi", runnerFunc(changeRunner))
	done := mergedIsolatedTask(t, s, repo, "delete")
	if err := s.DeleteTask(done.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(done.ExecutionWorkspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deletion kept the retained worktree: %v", err)
	}
	if _, ok := s.GetTask(done.ID); ok {
		t.Fatal("task still exists after deletion")
	}
}
