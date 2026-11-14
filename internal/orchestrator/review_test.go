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

func TestReviewTransitionValidation(t *testing.T) {
	var task Task
	if err := task.beginReview("   "); err == nil {
		t.Fatal("beginReview accepted an empty commit")
	}
	if err := task.settleReview(ReviewApproved, "abc123", LocalActor, ""); err == nil {
		t.Fatal("settleReview accepted a task with no prepared work")
	}
	must(t, task.beginReview("abc123"))
	if !task.AwaitingReview() || reviewSettledForTest(task) || task.ReviewCommit != "abc123" {
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
	must(t, task.settleReview(ReviewApproved, "abc123", LocalActor, "  looks good  "))
	if task.ReviewStatus != ReviewApproved || task.ReviewedBy != LocalActor || task.ReviewNote != "looks good" || task.ReviewedAt.IsZero() {
		t.Fatalf("approval not recorded: %+v", task)
	}
	if task.AwaitingReview() || !reviewSettledForTest(task) {
		t.Fatalf("unexpected settled review: %+v", task)
	}
	if err := task.settleReview(ReviewRejected, "abc123", LocalActor, ""); err == nil {
		t.Fatal("settleReview replaced a settled review")
	}

	var rejected Task
	must(t, rejected.beginReview("fedcba"))
	must(t, rejected.settleReview(ReviewRejected, "fedcba", LocalActor, "not needed"))
	if rejected.ReviewStatus != ReviewRejected || rejected.ReviewNote != "not needed" || rejected.ReviewedAt.IsZero() {
		t.Fatalf("rejection not recorded: %+v", rejected)
	}
}

// reviewSettledForTest is the settled-review predicate the tests assert; the
// production code only needs AwaitingReview plus the recorded status.
func reviewSettledForTest(t Task) bool {
	return t.ReviewStatus == ReviewApproved || t.ReviewStatus == ReviewRejected
}

func TestReviewStatePersistsAcrossReopen(t *testing.T) {
	var err error
	path := filepath.Join(t.TempDir(), "state.json")
	s := openTestService(t, path)
	task := createTask(t, s, "review", "prepare work", t.TempDir())
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
	must(t, s.Shutdown())

	restored := mustGet(Open(path))(t)
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
	s := newTestService(t)
	s.RegisterRunner("general", runnerFunc(func(context.Context, Task, io.Writer) error { return nil }))
	task := createTask(t, s, "start", "do work", t.TempDir())
	beginReviewForTest(t, s, task.ID)
	must(t, s.Start(context.Background(), task.ID))
	await(t, s, task.ID, Completed)
	assertReviewCleared(t, s, task.ID)
}

func TestRetryClearsReviewDecision(t *testing.T) {
	s := newTestService(t)
	s.RegisterRunner("general", runnerFunc(func(context.Context, Task, io.Writer) error {
		return errors.New("attempt failed")
	}))
	task := createTask(t, s, "retry", "do work", t.TempDir())
	must(t, s.Start(context.Background(), task.ID))
	await(t, s, task.ID, Failed)
	beginReviewForTest(t, s, task.ID)
	must(t, s.Retry(task.ID))
	await(t, s, task.ID, Queued)
	assertReviewCleared(t, s, task.ID)
}

func beginReviewForTest(t *testing.T, s *Service, id string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Tasks {
		if s.data.Tasks[i].ID == id {
			must(t, s.data.Tasks[i].beginReview("abc123"))
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

func TestCompletionWithoutReviewableWorkSkipsReview(t *testing.T) {
	for _, isolated := range []bool{true, false} {
		s := newTestService(t)
		s.RegisterRunner("general", runnerFunc(func(context.Context, Task, io.Writer) error { return nil }))
		var task Task
		if isolated {
			task = startIsolated(t, s, "noop", "do nothing", committedRepo(t))
		} else {
			task = createTask(t, s, "shared", "do work", t.TempDir())
			if task.Isolated {
				t.Fatal("test workspace unexpectedly isolated")
			}
			must(t, s.Start(context.Background(), task.ID))
		}
		done := await(t, s, task.ID, Completed)
		if isolated && done.MergeStatus != MergeUpToDate {
			t.Fatalf("unexpected merge result: %+v", done)
		}
		if done.ReviewStatus != "" || done.ReviewCommit != "" {
			t.Fatalf("isolated=%v completion opened a review: %+v", isolated, done)
		}
	}
}

func TestServiceReviewDecisionIsPersistedAndAudited(t *testing.T) {
	var err error
	path := filepath.Join(t.TempDir(), "state.json")
	s := openTestService(t, path)
	task := createTask(t, s, "review", "prepare work", t.TempDir())
	if err = s.ApproveReview(task.ID, "abc123", ""); err == nil {
		t.Fatal("approved a task with no prepared work")
	}
	beginReviewForTest(t, s, task.ID)
	must(t, s.ApproveReview(task.ID, "abc123", "  looks good  "))
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
	must(t, s.Shutdown())

	restored := mustGet(Open(path))(t)
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
	var err error
	s := newTestService(t)
	task := createTask(t, s, "reject", "prepare work", t.TempDir())
	beginReviewForTest(t, s, task.ID)
	must(t, s.RejectReview(task.ID, "abc123", "  not needed  "))
	got, _ := s.GetTask(task.ID)
	if got.ReviewStatus != ReviewRejected || got.ReviewNote != "not needed" || got.ReviewedBy != LocalActor || got.ReviewedAt.IsZero() {
		t.Fatalf("rejection not recorded: %+v", got)
	}
	if got.AwaitingReview() || !reviewSettledForTest(got) {
		t.Fatalf("unexpected review state: %+v", got)
	}
	if err = s.ApproveReview("missing", "abc123", ""); err == nil {
		t.Fatal("reviewed a missing task")
	}
}

func TestRejectedReviewRetainsWorkAndFollowUpOpensNewReview(t *testing.T) {
	var err error
	repo := committedRepo(t)
	s := newTestService(t)
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		name := "attempt-1.txt"
		if len(task.Context) > 0 {
			name = "attempt-2.txt"
		}
		return os.WriteFile(filepath.Join(task.Workspace, name), []byte(name), 0600)
	}))
	task := startIsolated(t, s, "reject", "prepare changes", repo)
	prepared := await(t, s, task.ID, Completed)
	if !prepared.AwaitingReview() {
		t.Fatalf("completion did not open review: %+v", prepared)
	}
	must(t, s.RejectReview(task.ID, prepared.ReviewCommit, "needs more context"))
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

	requeued := mustGet(s.FollowUp(task.ID, "add the missing detail"))(t)
	if !requeued {
		t.Fatal("rejected task was not requeued for follow-up")
	}
	following, _ := s.GetTask(task.ID)
	if following.Status != Queued || following.ReviewStatus != "" || following.MergeStatus != "" {
		t.Fatalf("follow-up did not clear the rejected review: %+v", following)
	}
	must(t, s.Start(context.Background(), task.ID))
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
