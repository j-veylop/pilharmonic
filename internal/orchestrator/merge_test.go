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
	"time"
)

func TestIsolatedCompletionPreparesReviewThenApprovalMerges(t *testing.T) {
	repo := committedRepo(t)
	head := testGit(t, repo, "rev-parse", "HEAD")
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
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
	if done.ReviewStatus != ReviewPending || done.ReviewCommit == "" || done.ReviewCommit != done.WorktreeCommit {
		t.Fatalf("prepared commit did not open review: %+v", done)
	}
	if done.MergeStatus != "" || done.MergeCommit != "" || !done.MergedAt.IsZero() {
		t.Fatalf("completion merged before approval: %+v", done)
	}
	if got := testGit(t, repo, "rev-parse", "HEAD"); got != head {
		t.Fatalf("source changed before approval: %s, want %s", got, head)
	}
	if _, err = os.Stat(filepath.Join(repo, "artifact.txt")); !os.IsNotExist(err) {
		t.Fatalf("artifact merged before approval: %v", err)
	}

	merged := approveReview(t, s, task.ID)
	if merged.MergeStatus != MergeMerged || merged.MergeCommit == "" || merged.WorktreeCommit != done.ReviewCommit || merged.MergedAt.IsZero() {
		t.Fatalf("merge result not recorded: %+v", merged)
	}
	if merged.ReviewStatus != ReviewApproved {
		t.Fatalf("approval was not retained with the merge: %+v", merged)
	}
	b, err := os.ReadFile(filepath.Join(repo, "artifact.txt"))
	if err != nil || string(b) != "from worktree\n" {
		t.Fatalf("artifact not merged: %q, %v", b, err)
	}
	b, err = os.ReadFile(filepath.Join(repo, "tracked.txt"))
	if err != nil || string(b) != "changed in worktree\n" {
		t.Fatalf("tracked change not merged: %q, %v", b, err)
	}
	if got := testGit(t, repo, "rev-parse", "HEAD"); got != merged.MergeCommit || got == head {
		t.Fatalf("source HEAD %q, merge commit %q, initial %q", got, merged.MergeCommit, head)
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

// Approval merges the commit the operator reviewed. Worktree content that
// appeared after review is neither committed nor merged.
func TestApprovalMergesReviewedCommitWithoutLaterDirtyContent(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		return os.WriteFile(filepath.Join(task.Workspace, "reviewed.txt"), []byte("reviewed\n"), 0600)
	}))
	task, err := s.CreateTask("reviewed", "prepare changes", repo)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetTaskIsolation(task.ID, true); err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	prepared := await(t, s, task.ID, Completed)
	if !prepared.AwaitingReview() || prepared.ReviewCommit == "" {
		t.Fatalf("completion did not prepare reviewed work: %+v", prepared)
	}
	if err = s.ApproveReview(task.ID, "not-the-reviewed-commit", ""); err == nil {
		t.Fatal("approval accepted an unreviewed commit")
	}
	still, _ := s.GetTask(task.ID)
	if !still.AwaitingReview() || still.ReviewCommit != prepared.ReviewCommit {
		t.Fatalf("stale approval changed the review: %+v", still)
	}

	late := filepath.Join(prepared.ExecutionWorkspace, "unreviewed.txt")
	if err = os.WriteFile(late, []byte("not reviewed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	merged := approveReview(t, s, task.ID)
	if merged.MergeStatus != MergeMerged || merged.WorktreeCommit != prepared.ReviewCommit {
		t.Fatalf("wrong worktree commit merged: %+v", merged)
	}
	testGit(t, repo, "merge-base", "--is-ancestor", merged.ReviewCommit, "HEAD")
	if _, err = os.Stat(filepath.Join(repo, "reviewed.txt")); err != nil {
		t.Fatalf("reviewed content was not merged: %v", err)
	}
	if _, err = os.Stat(filepath.Join(repo, "unreviewed.txt")); !os.IsNotExist(err) {
		t.Fatalf("unreviewed content was merged: %v", err)
	}
	head := testGit(t, prepared.ExecutionWorkspace, "rev-parse", "HEAD")
	if head != prepared.ReviewCommit {
		t.Fatalf("approval committed later worktree content: HEAD %s, reviewed %s", head, prepared.ReviewCommit)
	}
	if status := testGit(t, prepared.ExecutionWorkspace, "status", "--porcelain"); !strings.Contains(status, "unreviewed.txt") {
		t.Fatalf("unreviewed worktree content was staged or committed: %q", status)
	}
}

// Approval persists a durable merge intent and starts the merge asynchronously.
// Follow-up, discard and delete must not race that merge.
func TestApprovedMergeGuardsFollowUpDiscardAndDelete(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		return os.WriteFile(filepath.Join(task.Workspace, "guarded.txt"), []byte("work\n"), 0600)
	}))
	task, err := s.CreateTask("guarded", "prepare changes", repo)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetTaskIsolation(task.ID, true); err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	prepared := await(t, s, task.ID, Completed)
	if !prepared.AwaitingReview() {
		t.Fatalf("completion did not open review: %+v", prepared)
	}

	s.mergeMu.Lock()
	if err = s.ApproveReview(task.ID, prepared.ReviewCommit, ""); err != nil {
		s.mergeMu.Unlock()
		t.Fatal(err)
	}
	if _, err = s.FollowUp(task.ID, "late context"); err == nil || !strings.Contains(err.Error(), "integration") {
		t.Fatalf("follow-up during approved merge error = %v", err)
	}
	if err = s.DiscardWorktree(task.ID); err == nil || !strings.Contains(err.Error(), "integration") {
		t.Fatalf("discard during approved merge error = %v", err)
	}
	if err = s.DeleteTask(task.ID); err == nil || !strings.Contains(err.Error(), "integrat") {
		t.Fatalf("delete during approved merge error = %v", err)
	}
	s.mergeMu.Unlock()

	merged := awaitIntegration(t, s, task.ID)
	if merged.MergeStatus != MergeMerged {
		t.Fatalf("approved merge did not complete: %+v", merged)
	}
	if _, err = os.Stat(filepath.Join(repo, "guarded.txt")); err != nil {
		t.Fatalf("approved work was not merged: %v", err)
	}
}

// A persisted approval whose merge did not start before shutdown resumes after
// restart without changing the reviewed commit.
func TestApprovedMergeIntentResumesAfterRestart(t *testing.T) {
	repo := committedRepo(t)
	path := filepath.Join(t.TempDir(), "state.json")
	s := openTestService(t, path)
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		return os.WriteFile(filepath.Join(task.Workspace, "restart.txt"), []byte("work\n"), 0600)
	}))
	task, err := s.CreateTask("restart", "prepare changes", repo)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetTaskIsolation(task.ID, true); err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	prepared := await(t, s, task.ID, Completed)
	if !prepared.AwaitingReview() {
		t.Fatalf("completion did not open review: %+v", prepared)
	}
	s.mu.Lock()
	for i := range s.data.Tasks {
		if s.data.Tasks[i].ID != task.ID {
			continue
		}
		if err = s.data.Tasks[i].settleReview(ReviewApproved, prepared.ReviewCommit, LocalActor, "durable approval"); err != nil {
			s.mu.Unlock()
			t.Fatal(err)
		}
	}
	err = s.save()
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Shutdown(); err != nil {
		t.Fatal(err)
	}

	restored, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Shutdown()
	stored, _ := restored.GetTask(task.ID)
	if stored.ReviewStatus != ReviewApproved || stored.MergeStatus != "" {
		t.Fatalf("approval intent was not durable: %+v", stored)
	}
	if err = restored.ResumeScheduled(); err != nil {
		t.Fatal(err)
	}
	merged := awaitIntegration(t, restored, task.ID)
	if merged.MergeStatus != MergeMerged || merged.WorktreeCommit != prepared.ReviewCommit {
		t.Fatalf("restart did not merge the reviewed commit: %+v", merged)
	}
	if _, err = os.Stat(filepath.Join(repo, "restart.txt")); err != nil {
		t.Fatalf("reviewed work was not merged after restart: %v", err)
	}
}

func TestFailedIsolatedAttemptIsNotMerged(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
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
	s.RegisterRunner("general", runnerFunc(func(context.Context, Task, io.Writer) error { return nil }))
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
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
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
	prepared := await(t, s, task.ID, Completed)
	if !prepared.AwaitingReview() || prepared.MergeStatus != "" {
		t.Fatalf("completion did not stop at review: %+v", prepared)
	}
	done := approveReview(t, s, task.ID)
	if done.MergeStatus != MergeFailed || !strings.Contains(done.MergeError, "merge isolated worktree") || done.WorktreeCommit == "" {
		t.Fatalf("merge failure not recorded: %+v", done)
	}
	if done.ReviewStatus != ReviewApproved {
		t.Fatalf("merge failure lost the approved review: %+v", done)
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
	if retried.Status != Queued || retried.MergeStatus != "" || retried.ReviewStatus != "" {
		t.Fatalf("merge failure retry did not clear the result: %+v", retried)
	}
}

func TestConcurrentIsolatedCompletionsMergeSerially(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	var mu sync.Mutex
	seen := map[string]bool{}
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
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
		prepared := await(t, s, task.ID, Completed)
		if !prepared.AwaitingReview() {
			t.Fatalf("completion did not open review for %s: %+v", task.ID, prepared)
		}
	}
	for _, task := range tasks {
		done := approveReview(t, s, task.ID)
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
	s.RegisterRunner("general", runnerFunc(func(ctx context.Context, task Task, _ io.Writer) error {
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

// A dirty source checkout blocks a merge that overlaps those files. Git aborts
// without touching the operator's changes, the worktree is retained, and the
// failure explains what to do; after resolving the source, the task merges on
// retry.
func TestMergeBlockedByDirtySourceExplainsAndRetries(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		started <- struct{}{}
		<-release
		return os.WriteFile(filepath.Join(task.Workspace, "tracked.txt"), []byte("worktree change\n"), 0600)
	}))
	task, err := s.CreateTask("blocked", "change the tracked file", repo)
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
	if err = os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("local edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	close(release)
	prepared := await(t, s, task.ID, Completed)
	if !prepared.AwaitingReview() {
		t.Fatalf("completion did not open review: %+v", prepared)
	}
	done := approveReview(t, s, task.ID)
	if done.MergeStatus != MergeFailed {
		t.Fatalf("merge status = %s, want failed: %+v", done.MergeStatus, done)
	}
	for _, want := range []string{"uncommitted changes", "commit or stash", "retry the task"} {
		if !strings.Contains(done.MergeError, want) {
			t.Fatalf("merge error %q does not mention %q", done.MergeError, want)
		}
	}
	if b, readErr := os.ReadFile(filepath.Join(repo, "tracked.txt")); readErr != nil || string(b) != "local edit\n" {
		t.Fatalf("source edit was not preserved: %q, %v", b, readErr)
	}
	if _, statErr := os.Stat(done.ExecutionWorkspace); statErr != nil {
		t.Fatalf("blocked merge removed the worktree: %v", statErr)
	}

	testGit(t, repo, "checkout", "--", "tracked.txt")
	if err = s.Retry(task.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	await(t, s, task.ID, Completed)
	merged := approveReview(t, s, task.ID)
	if merged.MergeStatus != MergeMerged {
		t.Fatalf("retry merge status = %s (%s), want merged", merged.MergeStatus, merged.MergeError)
	}
	if b, readErr := os.ReadFile(filepath.Join(repo, "tracked.txt")); readErr != nil || string(b) != "worktree change\n" {
		t.Fatalf("worktree change was not merged on retry: %q, %v", b, readErr)
	}
}

func TestUntrackedMergeFailureGuidance(t *testing.T) {
	cause := errors.New("The following untracked working tree files would be overwritten by merge:\n\tlocal.txt")
	err := describeMergeFailure(cause)
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "move or commit") {
		t.Fatalf("missing untracked file guidance or Git diagnostic: %v", err)
	}
}
