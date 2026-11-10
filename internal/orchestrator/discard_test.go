package orchestrator

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// mergedIsolatedTask runs an isolated task that changes its worktree, approves
// the prepared commit and waits for the serialized merge. It leaves a retained
// worktree with recorded worktree and merge commits.
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
	await(t, s, task.ID, Completed)
	done := approveReview(t, s, task.ID)
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
	s.RegisterRunner("general", runnerFunc(changeRunner))
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
	s.RegisterRunner("general", runnerFunc(changeRunner))
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
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
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
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
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
	s.RegisterRunner("general", runnerFunc(changeRunner))
	first := mergedIsolatedTask(t, s, repo, "first")
	second := mergedIsolatedTask(t, s, repo, "second")
	dirty := filepath.Join(first.ExecutionWorkspace, "local.txt")
	if err := os.WriteFile(dirty, []byte("not committed\n"), 0600); err != nil {
		t.Fatal(err)
	}

	if count := len(s.WorktreeDiscardCandidates()); count != 2 {
		t.Fatalf("candidate count = %d, want 2", count)
	}
	removed, err := s.DiscardMergedWorktrees(s.WorktreeDiscardCandidates())
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
	removed, err = s.DiscardMergedWorktrees(s.WorktreeDiscardCandidates())
	if removed != 1 || err != nil {
		t.Fatalf("second bulk discard = %d, %v", removed, err)
	}
	if count := len(s.WorktreeDiscardCandidates()); count != 0 {
		t.Fatalf("remaining candidate count = %d", count)
	}
}

func TestDeleteTaskRemovesRetainedWorktree(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	s.RegisterRunner("general", runnerFunc(changeRunner))
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

func TestRecoverInterruptedDiscard(t *testing.T) {
	for _, phase := range []string{"before git", "after git", "after cleanup"} {
		t.Run(phase, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			s := openTestService(t, path)
			repo := committedRepo(t)
			s.RegisterRunner("general", runnerFunc(changeRunner))
			task := mergedIsolatedTask(t, s, repo, "recover")
			s.mu.Lock()
			s.data.Tasks[0].DiscardPending = true
			err := s.save()
			s.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if phase != "before git" {
				testGit(t, repo, "worktree", "remove", "--", task.ExecutionWorkspace)
			}
			reservation := filepath.Dir(task.ExecutionWorkspace)
			if phase == "after cleanup" {
				if err := os.RemoveAll(reservation); err != nil {
					t.Fatal(err)
				}
			}
			restored := openTestService(t, path)
			got, _ := restored.GetTask(task.ID)
			if got.DiscardPending || got.ReviewCommit != task.ReviewCommit {
				t.Fatalf("intent or review not reconciled: %+v", got)
			}
			if phase == "before git" {
				if got.ExecutionWorkspace != task.ExecutionWorkspace {
					t.Fatal("cleared a retained worktree")
				}
				if _, err := os.Stat(got.ExecutionWorkspace); err != nil {
					t.Fatal(err)
				}
			} else {
				if got.ExecutionWorkspace != "" || got.IsolationCommit != "" {
					t.Fatal("retained a removed worktree in task state")
				}
				if _, err := os.Stat(reservation); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("reservation still present: %v", err)
				}
			}
			again := openTestService(t, path)
			persisted, _ := again.GetTask(task.ID)
			if persisted.DiscardPending || len(persisted.Events) != len(got.Events) || persisted.ExecutionWorkspace != got.ExecutionWorkspace {
				t.Fatal("second restart repeated or lost discard recovery")
			}
		})
	}
}

func TestDiscardIntentSaveFailurePreservesWorktree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := openTestService(t, path)
	s.RegisterRunner("general", runnerFunc(changeRunner))
	task := mergedIsolatedTask(t, s, committedRepo(t), "intent")
	// Saving over a directory fails before the destructive Git operation.
	s.mu.Lock()
	s.path = t.TempDir()
	s.mu.Unlock()
	err := s.DiscardWorktree(task.ID)
	s.mu.Lock()
	s.path = path
	s.mu.Unlock()
	if err == nil {
		t.Fatal("discard acknowledged a failed intent save")
	}
	got, _ := s.GetTask(task.ID)
	if got.DiscardPending || got.ExecutionWorkspace != task.ExecutionWorkspace {
		t.Fatalf("intent save changed task: %+v", got)
	}
	if _, err = os.Stat(task.ExecutionWorkspace); err != nil {
		t.Fatal(err)
	}
}

func TestDiscardFinalSaveCanRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := openTestService(t, path)
	s.RegisterRunner("general", runnerFunc(changeRunner))
	task := mergedIsolatedTask(t, s, committedRepo(t), "final save")
	s.mergeMu.Lock()
	unlock := sync.OnceFunc(s.mergeMu.Unlock)
	defer unlock()
	result := make(chan error, 1)
	go func() { result <- s.DiscardWorktree(task.ID) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if got, _ := s.GetTask(task.ID); got.DiscardPending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("discard did not persist intent")
		}
		time.Sleep(time.Millisecond)
	}
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	unlock()
	if err := <-result; err == nil {
		t.Fatal("discard acknowledged a failed final save")
	}
	if _, err := os.Stat(task.ExecutionWorkspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Git removal did not finish: %v", err)
	}
	if _, err := s.FollowUp(task.ID, "again"); err == nil {
		t.Fatal("requeued a task with an unsettled discard")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	if err := s.DiscardWorktree(task.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetTask(task.ID)
	if got.DiscardPending || got.ExecutionWorkspace != "" {
		t.Fatal("retry failed to settle a completed removal")
	}
}
