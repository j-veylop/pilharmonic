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

// mergedIsolatedTask runs an isolated task that changes its worktree, approves
// the prepared commit and waits for the serialized merge. It leaves a retained
// worktree with recorded worktree and merge commits.
func mergedIsolatedTask(t *testing.T, s *Service, repo, title string) Task {
	t.Helper()
	task := startIsolated(t, s, title, "do work", repo)
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
	s := newTestService(t)
	s.RegisterRunner("general", runnerFunc(changeRunner))
	done := mergedIsolatedTask(t, s, repo, "discard")
	tree := done.ExecutionWorkspace
	reservation := filepath.Dir(tree)
	sourceHead := testGit(t, repo, "rev-parse", "HEAD")

	must(t, s.DiscardWorktree(done.ID))
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
	s := newTestService(t)
	s.RegisterRunner("general", runnerFunc(changeRunner))
	done := mergedIsolatedTask(t, s, repo, "dirty")
	file := filepath.Join(done.ExecutionWorkspace, "local.txt")
	must(t, os.WriteFile(file, []byte("not committed\n"), 0600))

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

	must(t, os.Remove(file))
	if err = s.DiscardWorktree(done.ID); err != nil {
		t.Fatalf("clean discard: %v", err)
	}
	if _, statErr := os.Stat(done.ExecutionWorkspace); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("clean worktree still exists: %v", statErr)
	}
}

func TestDiscardWorktreeRefusesUnmergedWorktree(t *testing.T) {
	var err error
	repo := committedRepo(t)
	s := newTestService(t)
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		if err := changeRunner(context.Background(), task, io.Discard); err != nil {
			return err
		}
		return errors.New("runner failed")
	}))
	task := startIsolated(t, s, "failed", "do work", repo)
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
	var err error
	repo := committedRepo(t)
	s := newTestService(t)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		started <- struct{}{}
		<-release
		return changeRunner(context.Background(), task, io.Discard)
	}))
	task := startIsolated(t, s, "running", "do work", repo)
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
	s := newTestService(t)
	s.RegisterRunner("general", runnerFunc(changeRunner))
	first := mergedIsolatedTask(t, s, repo, "first")
	second := mergedIsolatedTask(t, s, repo, "second")
	dirty := filepath.Join(first.ExecutionWorkspace, "local.txt")
	must(t, os.WriteFile(dirty, []byte("not committed\n"), 0600))

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

	must(t, os.Remove(dirty))
	removed, err = s.DiscardMergedWorktrees(s.WorktreeDiscardCandidates())
	if removed != 1 || err != nil {
		t.Fatalf("second bulk discard = %d, %v", removed, err)
	}
	if count := len(s.WorktreeDiscardCandidates()); count != 0 {
		t.Fatalf("remaining candidate count = %d", count)
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
				must(t, os.RemoveAll(reservation))
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
	must(t, os.Rename(path, path+".saved"))
	must(t, os.Mkdir(path, 0700))
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
	must(t, os.Remove(path))
	must(t, os.Rename(path+".saved", path))
	must(t, s.DiscardWorktree(task.ID))
	got, _ := s.GetTask(task.ID)
	if got.DiscardPending || got.ExecutionWorkspace != "" {
		t.Fatal("retry failed to settle a completed removal")
	}
}
