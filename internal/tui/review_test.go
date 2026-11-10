package tui

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"pilharmonic/internal/orchestrator"
)

type reviewChangeRunner struct{}

func (reviewChangeRunner) Run(_ context.Context, task orchestrator.Task, _ io.Writer) error {
	return os.WriteFile(filepath.Join(task.Workspace, "change.txt"), []byte(task.ID+"\n"), 0600)
}

func reviewRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, strings.TrimSpace(string(output)))
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	run("config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("committed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", "tracked.txt")
	run("commit", "-q", "-m", "initial")
	return dir
}

// startIsolatedReviewTask runs a task whose worktree change is prepared and
// opened for review without merging it, which is the state the review keys act
// on. Approval schedules the merge; rejection leaves the worktree retained.
func startIsolatedReviewTask(t *testing.T, s *orchestrator.Service, repo, title string) orchestrator.Task {
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
	awaitCompleted(t, s, task.ID)
	done, ok := s.GetTask(task.ID)
	if !ok {
		t.Fatal("task missing after completion")
	}
	deadline := time.Now().Add(2 * time.Second)
	for !done.AwaitingReview() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		done, ok = s.GetTask(task.ID)
		if !ok {
			t.Fatal("task missing while waiting for review")
		}
	}
	if !done.AwaitingReview() {
		t.Fatalf("completion did not open a review: %+v", done)
	}
	return done
}

// approveReviewAndAwaitMerge records approval and waits for the asynchronous
// integration it schedules, so later discard or follow-up actions see a merged
// worktree.
func approveReviewAndAwaitMerge(t *testing.T, s *orchestrator.Service, task orchestrator.Task) orchestrator.Task {
	t.Helper()
	if task.AwaitingReview() {
		if err := s.ApproveReview(task.ID, task.ReviewCommit, ""); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		got, ok := s.GetTask(task.ID)
		if !ok {
			t.Fatal("task missing while awaiting merge")
		}
		switch got.MergeStatus {
		case orchestrator.MergeMerged, orchestrator.MergeUpToDate:
			return got
		case orchestrator.MergeFailed:
			t.Fatalf("merge failed: %s", got.MergeError)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("merge did not complete")
	return orchestrator.Task{}
}

func selectTask(t *testing.T, m *model, id string) {
	t.Helper()
	for i, task := range m.tasks {
		if task.ID == id {
			m.selected = i
			return
		}
	}
	t.Fatal("task not found in model")
}

func TestReviewKeysApproveAndReject(t *testing.T) {
	repo := reviewRepo(t)
	s, err := orchestrator.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	s.RegisterRunner("general", reviewChangeRunner{})

	approved := startIsolatedReviewTask(t, s, repo, "approve")
	m := model{service: s, tasks: s.ListTasks(), width: 120, height: 35}
	selectTask(t, &m, approved.ID)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	m = next.(model)
	got, _ := s.GetTask(approved.ID)
	if got.ReviewStatus != orchestrator.ReviewApproved || got.ReviewedBy != orchestrator.LocalActor {
		t.Fatalf("approval key did not record a decision: %+v", got)
	}
	if !strings.Contains(m.notice, "Review approved") || !strings.Contains(m.notice, "merge") {
		t.Fatalf("approval did not state the merge consequence: %q", m.notice)
	}
	got = approveReviewAndAwaitMerge(t, s, got)
	if got.MergeStatus != orchestrator.MergeMerged && got.MergeStatus != orchestrator.MergeUpToDate {
		t.Fatalf("approval did not merge the prepared commit: %+v", got)
	}

	rejected := startIsolatedReviewTask(t, s, repo, "reject")
	m.tasks = s.ListTasks()
	selectTask(t, &m, rejected.ID)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("V")})
	m = next.(model)
	if m.input.kind != rejectReviewInput || m.input.target != rejected.ID {
		t.Fatalf("rejection key did not open a reason input: %+v", m)
	}
	if !strings.Contains(m.notice, "worktree") || !strings.Contains(m.notice, "F") {
		t.Fatalf("rejection prompt did not mention retained work: %q", m.notice)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("not needed")})
	m = next.(model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	got, _ = s.GetTask(rejected.ID)
	if got.ReviewStatus != orchestrator.ReviewRejected || got.ReviewNote != "not needed" {
		t.Fatalf("rejection key did not record a decision: %+v", got)
	}
	if m.input.kind != "" || !strings.Contains(m.notice, "Review rejected") || !strings.Contains(m.notice, "F") {
		t.Fatalf("rejection not acknowledged with follow-up: input=%v notice=%q", m.input, m.notice)
	}
	if view := m.View(); !strings.Contains(view, "F adds follow-up") {
		t.Fatalf("rejected detail does not suggest follow-up: %s", view)
	}

	// Rejection keeps the worktree and allows a follow-up that requeues the
	// task without changing its original mandate.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("F")})
	m = next.(model)
	if m.input.kind != followUpInput || m.input.target != rejected.ID {
		t.Fatalf("rejected task did not open a follow-up composer: %+v", m.input)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("add regression coverage")})
	m = next.(model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	got, _ = s.GetTask(rejected.ID)
	if got.Status != orchestrator.Queued || got.Prompt != "do work" || len(got.Context) != 1 || got.Context[0].Text != "add regression coverage" || got.ReviewStatus != "" {
		t.Fatalf("follow-up did not requeue the rejected task cleanly: %+v", got)
	}
	if _, err := os.Stat(rejected.ExecutionWorkspace); err != nil {
		t.Fatalf("rejection did not retain the worktree: %v", err)
	}
}

func TestReviewKeysRefuseStaleCommit(t *testing.T) {
	repo := reviewRepo(t)
	s, err := orchestrator.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	s.RegisterRunner("general", reviewChangeRunner{})
	task := startIsolatedReviewTask(t, s, repo, "updated review")
	for _, key := range []string{"v", "V"} {
		m := model{service: s, tasks: s.ListTasks(), width: 120, height: 35}
		selectTask(t, &m, task.ID)
		// The screen still shows the commit from an earlier attempt.
		m.tasks[m.selected].ReviewCommit = "earlier-commit"
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		m = next.(model)
		if key == "V" {
			// The task list refreshes while the rejection composer is open.
			m.tasks = s.ListTasks()
			m.input.text = "reviewed the earlier commit"
			next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = next.(model)
		}
		got, _ := s.GetTask(task.ID)
		if !got.AwaitingReview() || got.ReviewCommit != task.ReviewCommit || !strings.Contains(m.notice, "does not match") {
			t.Fatalf("key %s: review %s for %s, notice %q", key, got.ReviewStatus, got.ReviewCommit, m.notice)
		}
	}
}

func TestReviewKeysRequirePreparedWork(t *testing.T) {
	s, err := orchestrator.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	task, err := s.CreateTask("queued", "do work", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := model{service: s, tasks: s.ListTasks(), width: 120, height: 35}
	selectTask(t, &m, task.ID)
	for _, key := range []string{"v", "V"} {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		m = next.(model)
		if m.input.kind != "" || !strings.Contains(m.notice, "No worktree commit") {
			t.Fatalf("%s: input=%v notice=%q", key, m.input, m.notice)
		}
	}
}
