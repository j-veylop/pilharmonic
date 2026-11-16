package orchestrator

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestIsolatedCompletionPreparesReviewThenApprovalMerges(t *testing.T) {
	var err error
	repo := committedRepo(t)
	head := testGit(t, repo, "rev-parse", "HEAD")
	s := newTestService(t)
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		if err := os.WriteFile(filepath.Join(task.Workspace, "artifact.txt"), []byte("from worktree\n"), 0600); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(task.Workspace, "tracked.txt"), []byte("changed in worktree\n"), 0600)
	}))
	task := startIsolated(t, s, "merge", "make changes", repo)
	done := await(t, s, task.ID, Completed)
	if done.ReviewStatus != ReviewPending || done.ReviewCommit == "" || done.ReviewCommit != done.WorktreeCommit || done.Stage() != StageAwaitingReview {
		t.Fatalf("prepared commit did not open review: %+v", done)
	}
	if !slices.ContainsFunc(done.Events, func(e Event) bool {
		return e.Kind == "review_pending" && strings.Contains(e.Message, done.ReviewCommit)
	}) {
		t.Fatalf("review pending event missing: %+v", done.Events)
	}
	if done.MergeStatus != "" || done.MergeCommit != "" || !done.MergedAt.IsZero() {
		t.Fatalf("completion merged before approval: %+v", done)
	}
	if subject := testGit(t, repo, "log", "-1", "--format=%s", done.ReviewCommit); !strings.HasPrefix(subject, "Isolated task ") {
		t.Fatalf("prepared commit subject = %q, want an isolated-task subject", subject)
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
	var err error
	repo := committedRepo(t)
	s := newTestService(t)
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		return os.WriteFile(filepath.Join(task.Workspace, "reviewed.txt"), []byte("reviewed\n"), 0600)
	}))
	task := startIsolated(t, s, "reviewed", "prepare changes", repo)
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
	must(t, os.WriteFile(late, []byte("not reviewed\n"), 0600))
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
	var err error
	repo := committedRepo(t)
	s := newTestService(t)
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		return os.WriteFile(filepath.Join(task.Workspace, "guarded.txt"), []byte("work\n"), 0600)
	}))
	task := startIsolated(t, s, "guarded", "prepare changes", repo)
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
	var err error
	repo := committedRepo(t)
	path := filepath.Join(t.TempDir(), "state.json")
	s := openTestService(t, path)
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		return os.WriteFile(filepath.Join(task.Workspace, "restart.txt"), []byte("work\n"), 0600)
	}))
	task := startIsolated(t, s, "restart", "prepare changes", repo)
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
	must(t, s.Shutdown())

	restored := mustGet(Open(path))(t)
	defer func() { _ = restored.Shutdown() }()
	stored, _ := restored.GetTask(task.ID)
	if stored.ReviewStatus != ReviewApproved || stored.MergeStatus != "" {
		t.Fatalf("approval intent was not durable: %+v", stored)
	}
	must(t, restored.SetConcurrencyLimit(DefaultConcurrency))
	merged := awaitIntegration(t, restored, task.ID)
	if merged.MergeStatus != MergeMerged || merged.WorktreeCommit != prepared.ReviewCommit {
		t.Fatalf("restart did not merge the reviewed commit: %+v", merged)
	}
	if _, err = os.Stat(filepath.Join(repo, "restart.txt")); err != nil {
		t.Fatalf("reviewed work was not merged after restart: %v", err)
	}
}

func TestFailedIsolatedAttemptIsNotMerged(t *testing.T) {
	var err error
	repo := committedRepo(t)
	s := newTestService(t)
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		if err := os.WriteFile(filepath.Join(task.Workspace, "failed.txt"), []byte("do not merge"), 0600); err != nil {
			return err
		}
		return errors.New("runner failed")
	}))
	task := startIsolated(t, s, "failed", "do work", repo)
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
	s := newTestService(t)
	s.RegisterRunner("general", runnerFunc(func(context.Context, Task, io.Writer) error { return nil }))
	task := startIsolated(t, s, "noop", "do nothing", repo)
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
	s := newTestService(t)
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
	task := startIsolated(t, s, "conflict", "conflicting change", repo)
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
	s := newTestService(t)
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
		must(t, s.SetTaskIsolation(task.ID, true))
		tasks = append(tasks, task)
	}
	for _, task := range tasks {
		must(t, s.Start(context.Background(), task.ID))
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
	var err error
	repo := committedRepo(t)
	s := newTestService(t)
	started := make(chan struct{})
	s.RegisterRunner("general", runnerFunc(func(ctx context.Context, task Task, _ io.Writer) error {
		if err := os.WriteFile(filepath.Join(task.Workspace, "canceled.txt"), []byte("do not merge"), 0600); err != nil {
			return err
		}
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}))
	task := startIsolated(t, s, "canceled", "do work", repo)
	<-started
	must(t, s.Cancel(task.ID))
	done := await(t, s, task.ID, Canceled)
	if done.MergeStatus != "" {
		t.Fatalf("canceled attempt recorded a merge: %+v", done)
	}
	if _, err = os.Stat(filepath.Join(repo, "canceled.txt")); !os.IsNotExist(err) {
		t.Fatalf("canceled attempt was merged: %v", err)
	}
}

// An operator who keeps editing the source checkout does not block the merge.
// Git stashes the touched edits, merges, and reapplies them. A clean reapply
// leaves the edit uncommitted; a conflicting one keeps it in an autostash,
// leaves the conflict in the file and records a warning instead of losing work.
func TestMergeAutostashesUncommittedSourceEdits(t *testing.T) {
	for _, tc := range []struct {
		name, base, worktree, local string
		check                       func(t *testing.T, repo string, done Task)
	}{
		{"clean reapply", "one\ntwo\nthree\nfour\nfive\nsix\nseven\n", "one\ntwo\nthree\nfour\nfive\nsix\nSEVEN\n", "LOCAL\ntwo\nthree\nfour\nfive\nsix\nseven\n",
			func(t *testing.T, repo string, done Task) {
				if done.MergeWarning != "" || done.MergeError != "" {
					t.Fatalf("merge over uncommitted source edits = %+v", done)
				}
				want := "LOCAL\ntwo\nthree\nfour\nfive\nsix\nSEVEN\n"
				if b, err := os.ReadFile(filepath.Join(repo, "tracked.txt")); err != nil || string(b) != want {
					t.Fatalf("source after merge = %q, %v; want %q", b, err, want)
				}
				if status := testGit(t, repo, "status", "--porcelain"); status != "M tracked.txt" {
					t.Fatalf("the local edit was committed instead of kept uncommitted: %q", status)
				}
				if stash := testGit(t, repo, "stash", "list"); stash != "" {
					t.Fatalf("a clean reapply left a stash behind: %q", stash)
				}
				if parents := strings.Fields(testGit(t, repo, "rev-list", "--parents", "-n", "1", "HEAD")); len(parents) != 3 {
					t.Fatalf("expected a merge commit, parents: %v", parents)
				}
			}},
		{"conflicting reapply", "alpha\nbeta\ngamma\n", "alpha\nWORKTREE\ngamma\n", "alpha\nLOCAL\ngamma\n",
			func(t *testing.T, repo string, done Task) {
				if done.MergeStatus != MergeFailed || done.MergeCommit != "" {
					t.Fatalf("merge over conflicting source edits = %+v", done)
				}
				if !done.MergeConflict() || !strings.Contains(done.MergeError, "tracked.txt") {
					t.Fatalf("merge error does not name the conflicting file: %q", done.MergeError)
				}
				if done.MergeWarning != "" {
					t.Fatalf("a rolled-back merge recorded a warning: %q", done.MergeWarning)
				}
				if stash := testGit(t, repo, "stash", "list"); stash != "" {
					t.Fatalf("the rolled-back merge left a stash behind: %q", stash)
				}
				if got, err := os.ReadFile(filepath.Join(repo, "tracked.txt")); err != nil || string(got) != "alpha\nLOCAL\ngamma\n" {
					t.Fatalf("the operator's edit was not restored: %q, %v", got, err)
				}
				if parents := strings.Fields(testGit(t, repo, "rev-list", "--parents", "-n", "1", "HEAD")); len(parents) != 2 {
					t.Fatalf("the merge commit was not rolled back, parents: %v", parents)
				}
				if slices.ContainsFunc(done.Events, func(e Event) bool { return e.Kind == "merge_warning" }) {
					t.Fatalf("a rolled-back merge recorded a merge warning: %+v", done.Events)
				}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := committedRepo(t)
			must(t, os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte(tc.base), 0600))
			testGit(t, repo, "commit", "-qam", "expand the tracked file")
			s := newTestService(t)
			started := make(chan struct{}, 1)
			release := make(chan struct{})
			s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
				started <- struct{}{}
				<-release
				return os.WriteFile(filepath.Join(task.Workspace, "tracked.txt"), []byte(tc.worktree), 0600)
			}))
			task := startIsolated(t, s, "dirty", "change the tracked file", repo)
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("runner did not start")
			}
			must(t, os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte(tc.local), 0600))
			close(release)
			if prepared := await(t, s, task.ID, Completed); !prepared.AwaitingReview() {
				t.Fatalf("completion did not open review: %+v", prepared)
			}
			done := approveReview(t, s, task.ID)
			tc.check(t, repo, done)
		})
	}
}

// A merge that genuinely conflicts is still aborted, and the abort restores the
// source checkout including uncommitted edits Git stashed for the attempt.
func TestConflictingMergeRestoresStashedSourceEdits(t *testing.T) {
	repo := committedRepo(t)
	must(t, os.WriteFile(filepath.Join(repo, "other.txt"), []byte("keep me\n"), 0600))
	testGit(t, repo, "add", "other.txt")
	testGit(t, repo, "commit", "-q", "-m", "add other")
	s := newTestService(t)
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		if err := os.WriteFile(filepath.Join(task.Workspace, "tracked.txt"), []byte("worktree\n"), 0600); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("source\n"), 0600); err != nil {
			return err
		}
		testGit(t, repo, "add", "tracked.txt")
		testGit(t, repo, "commit", "-q", "-m", "advance source")
		return nil
	}))
	task := startIsolated(t, s, "conflict", "conflicting change", repo)
	await(t, s, task.ID, Completed)
	// The runner committed its source change; the operator keeps an edit too.
	must(t, os.WriteFile(filepath.Join(repo, "other.txt"), []byte("local edit\n"), 0600))
	done := approveReview(t, s, task.ID)
	if done.MergeStatus != MergeFailed || !done.MergeConflict() {
		t.Fatalf("conflicting merge = %+v", done)
	}
	if b, readErr := os.ReadFile(filepath.Join(repo, "other.txt")); readErr != nil || string(b) != "local edit\n" {
		t.Fatalf("aborted merge lost the operator's edit: %q, %v", b, readErr)
	}
	if stash := testGit(t, repo, "stash", "list"); stash != "" {
		t.Fatalf("aborted merge left a stash behind: %q", stash)
	}
	cmd := exec.Command("git", "rev-parse", "-q", "--verify", "MERGE_HEAD")
	cmd.Dir = repo
	if output, mergeErr := cmd.CombinedOutput(); mergeErr == nil {
		t.Fatalf("source left in a merge: %s", strings.TrimSpace(string(output)))
	}
}
