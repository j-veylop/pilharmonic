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

func TestReviewTransitionValidation(t *testing.T) {
	var task Task
	if err := task.beginReview("   "); err == nil {
		t.Fatal("beginReview accepted an empty commit")
	}
	if err := task.settleReview(ReviewApproved, "abc123", LocalActor, ""); err == nil {
		t.Fatal("settleReview accepted a task with no prepared work")
	}
	if err := task.beginReview("abc123"); err != nil {
		t.Fatal(err)
	}
	if !task.AwaitingReview() || task.ReviewSettled() || task.ReviewCommit != "abc123" {
		t.Fatalf("unexpected pending review: %+v", task)
	}
	if err := task.beginReview("def456"); err == nil {
		t.Fatal("beginReview replaced an outstanding review")
	}
	if err := task.settleReview("bogus", "abc123", LocalActor, ""); err == nil {
		t.Fatal("settleReview accepted an unknown decision")
	}
	if err := task.settleReview(ReviewApproved, "def456", LocalActor, ""); err == nil {
		t.Fatal("settleReview accepted a commit that was not reviewed")
	}
	if err := task.settleReview(ReviewApproved, "abc123", "  ", ""); err == nil {
		t.Fatal("settleReview accepted an empty actor")
	}
	if err := task.settleReview(ReviewApproved, "abc123", LocalActor, "  looks good  "); err != nil {
		t.Fatal(err)
	}
	if task.ReviewStatus != ReviewApproved || task.ReviewedBy != LocalActor || task.ReviewNote != "looks good" || task.ReviewedAt.IsZero() {
		t.Fatalf("approval not recorded: %+v", task)
	}
	if task.AwaitingReview() || !task.ReviewSettled() {
		t.Fatalf("unexpected settled review: %+v", task)
	}
	if err := task.settleReview(ReviewRejected, "abc123", LocalActor, ""); err == nil {
		t.Fatal("settleReview replaced a settled review")
	}

	var rejected Task
	if err := rejected.beginReview("fedcba"); err != nil {
		t.Fatal(err)
	}
	if err := rejected.settleReview(ReviewRejected, "fedcba", LocalActor, "not needed"); err != nil {
		t.Fatal(err)
	}
	if rejected.ReviewStatus != ReviewRejected || rejected.ReviewNote != "not needed" || rejected.ReviewedAt.IsZero() {
		t.Fatalf("rejection not recorded: %+v", rejected)
	}
}

func TestResetReviewClearsDecisionOnly(t *testing.T) {
	task := Task{Isolated: true, ExecutionWorkspace: "/tmp/tree", ReviewStatus: ReviewApproved, ReviewCommit: "abc123", ReviewedBy: LocalActor, ReviewedAt: time.Now().UTC(), ReviewNote: "ok"}
	task.resetReview()
	if task.ReviewStatus != "" || task.ReviewCommit != "" || task.ReviewedBy != "" || !task.ReviewedAt.IsZero() || task.ReviewNote != "" {
		t.Fatalf("review decision not cleared: %+v", task)
	}
	if !task.Isolated || task.ExecutionWorkspace != "/tmp/tree" {
		t.Fatalf("resetReview touched workspace state: %+v", task)
	}
}

func TestReviewStatePersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := openTestService(t, path)
	task, err := s.CreateTask("review", "prepare work", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	for i := range s.data.Tasks {
		if s.data.Tasks[i].ID != task.ID {
			continue
		}
		if err := s.data.Tasks[i].beginReview("abc123"); err != nil {
			s.mu.Unlock()
			t.Fatal(err)
		}
		if err := s.data.Tasks[i].settleReview(ReviewApproved, "abc123", LocalActor, "reviewed locally"); err != nil {
			s.mu.Unlock()
			t.Fatal(err)
		}
	}
	err = s.save()
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}

	restored, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Shutdown() }()
	got, ok := restored.GetTask(task.ID)
	if !ok {
		t.Fatal("task missing after reopen")
	}
	if got.ReviewStatus != ReviewApproved || got.ReviewCommit != "abc123" || got.ReviewedBy != LocalActor || got.ReviewNote != "reviewed locally" || got.ReviewedAt.IsZero() {
		t.Fatalf("review state not durable: %+v", got)
	}
}

func TestStartClearsReviewDecision(t *testing.T) {
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	s.RegisterRunner("general", runnerFunc(func(context.Context, Task, io.Writer) error { return nil }))
	task, err := s.CreateTask("start", "do work", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	beginReviewForTest(t, s, task.ID)
	if err := s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	await(t, s, task.ID, Completed)
	assertReviewCleared(t, s, task.ID)
}

func TestRetryClearsReviewDecision(t *testing.T) {
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	s.RegisterRunner("general", runnerFunc(func(context.Context, Task, io.Writer) error {
		return errors.New("attempt failed")
	}))
	task, err := s.CreateTask("retry", "do work", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	await(t, s, task.ID, Failed)
	beginReviewForTest(t, s, task.ID)
	if err := s.Retry(task.ID); err != nil {
		t.Fatal(err)
	}
	await(t, s, task.ID, Queued)
	assertReviewCleared(t, s, task.ID)
}

func beginReviewForTest(t *testing.T, s *Service, id string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Tasks {
		if s.data.Tasks[i].ID == id {
			if err := s.data.Tasks[i].beginReview("abc123"); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("task not found")
}

func assertReviewCleared(t *testing.T, s *Service, id string) {
	t.Helper()
	got, _ := s.GetTask(id)
	if got.ReviewStatus != "" || got.ReviewCommit != "" || got.ReviewedBy != "" || !got.ReviewedAt.IsZero() || got.ReviewNote != "" {
		t.Fatalf("review decision survived a new attempt: %+v", got)
	}
}

func TestIsolatedCompletionOpensReview(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		return os.WriteFile(filepath.Join(task.Workspace, "reviewed.txt"), []byte("work\n"), 0600)
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
	if done.ReviewStatus != ReviewPending || done.ReviewCommit == "" || done.ReviewCommit != done.WorktreeCommit {
		t.Fatalf("prepared commit was not opened for review: %+v", done)
	}
	if done.Stage() != "awaiting review" {
		t.Fatalf("pending review does not need attention: %q", done.Stage())
	}
	opened := false
	for _, event := range done.Events {
		if event.Kind == "review_pending" && strings.Contains(event.Message, done.ReviewCommit) {
			opened = true
		}
	}
	if !opened {
		t.Fatalf("review pending event missing: %+v", done.Events)
	}
}

func TestCompletionWithoutNewWorkSkipsReview(t *testing.T) {
	repo := committedRepo(t)
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
	if done.MergeStatus != MergeUpToDate {
		t.Fatalf("unexpected merge result: %+v", done)
	}
	if done.ReviewStatus != "" || done.ReviewCommit != "" {
		t.Fatalf("up-to-date completion opened a review: %+v", done)
	}
}

func TestSharedCompletionSkipsReview(t *testing.T) {
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	s.RegisterRunner("general", runnerFunc(func(context.Context, Task, io.Writer) error { return nil }))
	task, err := s.CreateTask("shared", "do work", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if task.Isolated {
		t.Fatal("test workspace unexpectedly isolated")
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	done := await(t, s, task.ID, Completed)
	if done.ReviewStatus != "" {
		t.Fatalf("shared completion opened a review: %+v", done)
	}
}

func TestServiceReviewDecisionIsPersistedAndAudited(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := openTestService(t, path)
	task, err := s.CreateTask("review", "prepare work", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ApproveReview(task.ID, "abc123", ""); err == nil {
		t.Fatal("approved a task with no prepared work")
	}
	beginReviewForTest(t, s, task.ID)
	if err = s.ApproveReview(task.ID, "abc123", "  looks good  "); err != nil {
		t.Fatal(err)
	}
	approved, _ := s.GetTask(task.ID)
	if approved.ReviewStatus != ReviewApproved || approved.ReviewNote != "looks good" || approved.ReviewedBy != LocalActor || approved.ReviewedAt.IsZero() {
		t.Fatalf("approval not recorded: %+v", approved)
	}
	audited := false
	for _, event := range approved.Events {
		if event.Kind == "review_approved" && strings.Contains(event.Message, "looks good") {
			audited = true
		}
	}
	if !audited {
		t.Fatalf("approval event missing: %+v", approved.Events)
	}
	if err = s.RejectReview(task.ID, "abc123", "changed my mind"); err == nil {
		t.Fatal("replaced a settled review")
	}
	if err = s.Shutdown(); err != nil {
		t.Fatal(err)
	}

	restored, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Shutdown() }()
	got, _ := restored.GetTask(task.ID)
	if got.ReviewStatus != ReviewApproved || got.ReviewCommit != "abc123" || got.ReviewNote != "looks good" {
		t.Fatalf("review decision not durable: %+v", got)
	}
	if err = restored.RejectReview(task.ID, "abc123", "late rejection"); err == nil {
		t.Fatal("reopened service replaced a settled review")
	}
}

func TestRejectReviewRecordsReason(t *testing.T) {
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	task, err := s.CreateTask("reject", "prepare work", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	beginReviewForTest(t, s, task.ID)
	if err = s.RejectReview(task.ID, "abc123", "  not needed  "); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetTask(task.ID)
	if got.ReviewStatus != ReviewRejected || got.ReviewNote != "not needed" || got.ReviewedBy != LocalActor || got.ReviewedAt.IsZero() {
		t.Fatalf("rejection not recorded: %+v", got)
	}
	if got.AwaitingReview() || !got.ReviewSettled() {
		t.Fatalf("unexpected review state: %+v", got)
	}
	if err = s.ApproveReview("missing", "abc123", ""); err == nil {
		t.Fatal("reviewed a missing task")
	}
}

func TestRejectedReviewRetainsWorkAndFollowUpOpensNewReview(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		name := "attempt-1.txt"
		if len(task.Context) > 0 {
			name = "attempt-2.txt"
		}
		return os.WriteFile(filepath.Join(task.Workspace, name), []byte(name), 0600)
	}))
	task, err := s.CreateTask("reject", "prepare changes", repo)
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
	if err = s.RejectReview(task.ID, prepared.ReviewCommit, "needs more context"); err != nil {
		t.Fatal(err)
	}
	rejected, _ := s.GetTask(task.ID)
	if rejected.ReviewStatus != ReviewRejected || rejected.ReviewCommit != prepared.ReviewCommit || rejected.MergeStatus != "" {
		t.Fatalf("rejection did not retain the commit without merging: %+v", rejected)
	}
	if _, err = os.Stat(filepath.Join(repo, "attempt-1.txt")); !os.IsNotExist(err) {
		t.Fatalf("rejected work was merged: %v", err)
	}
	if head := testGit(t, prepared.ExecutionWorkspace, "rev-parse", "HEAD"); head != prepared.ReviewCommit {
		t.Fatalf("rejection changed the worktree commit: %s", head)
	}

	requeued, err := s.FollowUp(task.ID, "add the missing detail")
	if err != nil {
		t.Fatal(err)
	}
	if !requeued {
		t.Fatal("rejected task was not requeued for follow-up")
	}
	following, _ := s.GetTask(task.ID)
	if following.Status != Queued || following.ReviewStatus != "" || following.MergeStatus != "" {
		t.Fatalf("follow-up did not clear the rejected review: %+v", following)
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	await(t, s, task.ID, Completed)
	next, _ := s.GetTask(task.ID)
	if !next.AwaitingReview() || next.ReviewCommit == "" || next.ReviewCommit == prepared.ReviewCommit {
		t.Fatalf("follow-up did not prepare a new review: %+v", next)
	}
	if _, err = os.Stat(filepath.Join(repo, "attempt-2.txt")); !os.IsNotExist(err) {
		t.Fatalf("follow-up work merged before approval: %v", err)
	}
	merged := approveReview(t, s, task.ID)
	if merged.MergeStatus != MergeMerged || merged.WorktreeCommit != next.ReviewCommit {
		t.Fatalf("follow-up review did not merge exactly: %+v", merged)
	}
	for _, name := range []string{"attempt-1.txt", "attempt-2.txt"} {
		if _, err = os.Stat(filepath.Join(repo, name)); err != nil {
			t.Fatalf("%s was not merged after approval: %v", name, err)
		}
	}
}
