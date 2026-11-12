package orchestrator

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInheritedWorktreeWaitsForGrandchildAndSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := openTestService(t, path)
	if err := s.SetConcurrencyLimit(1); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	s.RegisterRunner("general", runnerFunc(func(ctx context.Context, task Task, _ io.Writer) error {
		if task.Depth < 2 {
			_, err := s.SpawnSubagent(task.ID, "child", "use inherited tree", "")
			return err
		}
		close(entered)
		select {
		case <-release:
			return os.WriteFile(filepath.Join(task.Workspace, "grandchild"), []byte("done"), 0600)
		case <-ctx.Done():
			return ctx.Err()
		}
	}))
	repo := committedRepo(t)
	owner, err := s.CreateTask("parent", "fix the inherited tree", repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), owner.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("queued descendants never acquired the parent's slot")
	}
	got, _ := s.GetTask(owner.ID)
	if got.Status != Completed || got.MergeStatus != MergeWaiting {
		t.Fatalf("premature integration: %+v", got)
	}
	close(release)
	var review Task
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, _ = s.GetTask(owner.ID)
		if got.AwaitingReview() {
			review = got
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("prepared commit did not open review: %+v", got)
		}
		time.Sleep(time.Millisecond)
	}
	if review.MergeStatus != "" {
		t.Fatalf("review opened before approval: %+v", review)
	}
	merged := approveReview(t, s, owner.ID)
	if merged.MergeStatus != MergeMerged {
		t.Fatalf("approved worktree did not merge: %+v", merged)
	}
	if _, err := os.Stat(filepath.Join(repo, "grandchild")); err != nil {
		t.Fatal(err)
	}
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestService(t, path)
	if err := reopened.DiscardWorktree(owner.ID); err == nil {
		t.Fatal("discarded worktree with retryable descendants after restart")
	}
}

// Approval waits while a follow-up child is using the inherited worktree and
// then merges only the reviewed commit, not the child's later dirty content.
func TestApprovalWaitsForInheritedWorktreeAndMergesReviewedCommit(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		switch task.Depth {
		case 0:
			if err := os.WriteFile(filepath.Join(task.Workspace, "parent.txt"), []byte("parent\n"), 0600); err != nil {
				return err
			}
			_, err := s.SpawnSubagent(task.ID, "child", "child work", "")
			return err
		default:
			name := "child-1.txt"
			if len(task.Context) > 0 {
				name = "child-2.txt"
			}
			return os.WriteFile(filepath.Join(task.Workspace, name), []byte(name), 0600)
		}
	}))
	owner, err := s.CreateTask("parent", "fix the inherited tree", repo)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetTaskIsolation(owner.ID, true); err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), owner.ID); err != nil {
		t.Fatal(err)
	}

	var prepared Task
	deadline := time.Now().Add(5 * time.Second)
	for {
		prepared, _ = s.GetTask(owner.ID)
		if prepared.AwaitingReview() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("parent never prepared review: %+v", prepared)
		}
		time.Sleep(time.Millisecond)
	}
	if _, err = os.Stat(filepath.Join(repo, "parent.txt")); !os.IsNotExist(err) {
		t.Fatalf("parent work merged before review: %v", err)
	}

	var child Task
	for _, task := range s.ListTasks() {
		if task.ParentID == owner.ID {
			child = task
			break
		}
	}
	if child.ID == "" {
		t.Fatal("child task missing")
	}
	requeued, err := s.FollowUp(child.ID, "extend the child work")
	if err != nil {
		t.Fatal(err)
	}
	if !requeued {
		t.Fatal("completed child was not requeued")
	}
	if err = s.ApproveReview(owner.ID, prepared.ReviewCommit, ""); err != nil {
		t.Fatal(err)
	}
	waiting, _ := s.GetTask(owner.ID)
	if waiting.ReviewStatus != ReviewApproved || waiting.MergeStatus != MergeWaiting {
		t.Fatalf("approval did not wait for the inherited worktree: %+v", waiting)
	}
	if _, err = os.Stat(filepath.Join(repo, "parent.txt")); !os.IsNotExist(err) {
		t.Fatalf("approval merged while a descendant was active: %v", err)
	}

	if err = s.Start(context.Background(), child.ID); err != nil {
		t.Fatal(err)
	}
	await(t, s, child.ID, Completed)
	merged := awaitIntegration(t, s, owner.ID)
	if merged.MergeStatus != MergeMerged || merged.WorktreeCommit != prepared.ReviewCommit {
		t.Fatalf("deferred approval did not merge the reviewed commit: %+v", merged)
	}
	for _, name := range []string{"parent.txt", "child-1.txt"} {
		if _, err = os.Stat(filepath.Join(repo, name)); err != nil {
			t.Fatalf("%s was not merged with the reviewed commit: %v", name, err)
		}
	}
	if _, err = os.Stat(filepath.Join(repo, "child-2.txt")); !os.IsNotExist(err) {
		t.Fatalf("later child content was merged by approval: %v", err)
	}
	if status := testGit(t, owner.ExecutionWorkspace, "status", "--porcelain"); status == "" {
		t.Fatal("later child content was committed or removed from the worktree")
	}
}

// Deleting the unsuccessful child that blocked a deferred integration lets the
// operator prepare the retained worktree again without re-running the agent.
func TestDeletingFailedChildReopensIntegration(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	if err := s.SetConcurrencyLimit(1); err != nil {
		t.Fatal(err)
	}
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		if task.Depth > 0 {
			return errors.New("child failed")
		}
		if err := os.WriteFile(filepath.Join(task.Workspace, "parent.txt"), []byte("parent\n"), 0600); err != nil {
			return err
		}
		_, err := s.SpawnSubagent(task.ID, "child", "child work", "")
		return err
	}))
	parent, err := s.CreateTask("parent", "fix the inherited tree", repo)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetTaskIsolation(parent.ID, true); err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), parent.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		blocked, _ := s.GetTask(parent.ID)
		if blocked.MergeStatus == MergeFailed && blocked.ReviewStatus == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("integration never reported the failed child: %+v", blocked)
		}
		time.Sleep(time.Millisecond)
	}
	var child Task
	for _, task := range s.ListTasks() {
		if task.ParentID == parent.ID {
			child = task
			break
		}
	}
	if child.ID == "" {
		t.Fatal("child task missing")
	}
	if err = s.DeleteTask(child.ID); err != nil {
		t.Fatal(err)
	}
	for {
		reopened, _ := s.GetTask(parent.ID)
		if reopened.AwaitingReview() && reopened.WorktreeCommit != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("deleting the failed child did not reopen integration: %+v", reopened)
		}
		time.Sleep(time.Millisecond)
	}
}
