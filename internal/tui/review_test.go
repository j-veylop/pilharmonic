package tui

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"orchestrator/internal/orchestrator"
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

// startIsolatedReviewTask runs a task whose worktree change is merged and then
// opened for review, which is the state the review keys act on.
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
	if !done.AwaitingReview() {
		t.Fatalf("completion did not open a review: %+v", done)
	}
	return done
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
	s.RegisterRunner("pi", reviewChangeRunner{})

	approved := startIsolatedReviewTask(t, s, repo, "approve")
	m := model{service: s, tasks: s.ListTasks(), width: 120, height: 35}
	selectTask(t, &m, approved.ID)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	m = next.(model)
	got, _ := s.GetTask(approved.ID)
	if got.ReviewStatus != orchestrator.ReviewApproved || got.ReviewedBy != orchestrator.LocalActor {
		t.Fatalf("approval key did not record a decision: %+v", got)
	}
	if !strings.Contains(m.notice, "Review approved") {
		t.Fatalf("approval not acknowledged: %q", m.notice)
	}

	rejected := startIsolatedReviewTask(t, s, repo, "reject")
	m.tasks = s.ListTasks()
	selectTask(t, &m, rejected.ID)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("V")})
	m = next.(model)
	if m.input.kind != rejectReviewInput || m.input.target != rejected.ID {
		t.Fatalf("rejection key did not open a reason input: %+v", m)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("not needed")})
	m = next.(model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	got, _ = s.GetTask(rejected.ID)
	if got.ReviewStatus != orchestrator.ReviewRejected || got.ReviewNote != "not needed" {
		t.Fatalf("rejection key did not record a decision: %+v", got)
	}
	if m.input.kind != "" || !strings.Contains(m.notice, "Review rejected") {
		t.Fatalf("rejection not acknowledged: input=%v notice=%q", m.input, m.notice)
	}
}

func TestReviewKeysRefuseStaleCommit(t *testing.T) {
	repo := reviewRepo(t)
	s, err := orchestrator.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	s.RegisterRunner("pi", reviewChangeRunner{})
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
