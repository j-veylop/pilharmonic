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
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

func testGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output))
}

// committedRepo creates a repository with one commit, which isolated worktrees
// are created from.
func committedRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	dir := t.TempDir()
	testGit(t, dir, "init", "-q")
	testGit(t, dir, "config", "user.email", "test@example.com")
	testGit(t, dir, "config", "user.name", "Test")
	testGit(t, dir, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("committed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	testGit(t, dir, "add", "tracked.txt")
	testGit(t, dir, "commit", "-q", "-m", "initial")
	return dir
}

func openTestService(t *testing.T, path string) *Service {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Shutdown() })
	return s
}

func TestIsolatedExecutionRunsInCommittedWorktree(t *testing.T) {
	repo := committedRepo(t)
	head := testGit(t, repo, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("local only"), 0600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(t.TempDir(), "state.json")
	s := openTestService(t, statePath)
	seen := make(chan Task, 1)
	s.RegisterRunner("pi", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		seen <- task
		return nil
	}))
	task, err := s.CreateTask("isolated", "do work", repo)
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
	run := <-seen
	wantCwd := filepath.Join(filepath.Dir(statePath), "workspaces", task.ID, "tree")
	if run.Workspace != wantCwd {
		t.Fatalf("runner cwd %q, want %q", run.Workspace, wantCwd)
	}
	if _, err := os.Stat(filepath.Join(run.Workspace, "tracked.txt")); err != nil {
		t.Fatalf("committed file missing from worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(run.Workspace, "untracked.txt")); !os.IsNotExist(err) {
		t.Fatalf("uncommitted file was copied into the worktree: %v", err)
	}
	if got := testGit(t, run.Workspace, "rev-parse", "HEAD"); got != head {
		t.Fatalf("worktree HEAD %q, want %q", got, head)
	}
	if !done.Isolated || done.IsolationCommit != head || done.ExecutionWorkspace != run.Workspace {
		t.Fatalf("isolation not recorded: %+v", done)
	}
	if done.Workspace != repo {
		t.Fatalf("source workspace changed: %q", done.Workspace)
	}
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Shutdown()
	got, ok := restored.GetTask(task.ID)
	if !ok || !got.Isolated || got.ExecutionWorkspace != run.Workspace || got.IsolationCommit != head {
		t.Fatalf("isolation not durable: %+v", got)
	}
}

func TestIsolatedRetryReusesPreparedWorktree(t *testing.T) {
	repo := committedRepo(t)
	statePath := filepath.Join(t.TempDir(), "state.json")
	s := openTestService(t, statePath)
	var mu sync.Mutex
	var calls int
	var workspaces []string
	s.RegisterRunner("pi", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		mu.Lock()
		calls++
		n := calls
		workspaces = append(workspaces, task.Workspace)
		mu.Unlock()
		if n == 1 {
			if err := os.WriteFile(filepath.Join(task.Workspace, "artifact.txt"), []byte("kept"), 0600); err != nil {
				return err
			}
			return errors.New("first attempt failed")
		}
		if _, err := os.Stat(filepath.Join(task.Workspace, "artifact.txt")); err != nil {
			return err
		}
		return nil
	}))
	task, err := s.CreateTask("retry", "do work", repo)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetTaskIsolation(task.ID, true); err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	await(t, s, task.ID, Failed)
	if err = s.Retry(task.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	done := await(t, s, task.ID, Completed)
	mu.Lock()
	defer mu.Unlock()
	if len(workspaces) != 2 || workspaces[0] != workspaces[1] {
		t.Fatalf("retry prepared a new workspace: %v", workspaces)
	}
	if done.ExecutionWorkspace != workspaces[0] || len(done.Attempts) != 2 || done.Attempts[1].Status != Completed {
		t.Fatalf("unexpected retry state: %+v", done)
	}
}

func TestIsolatedExecutionRequiresRepository(t *testing.T) {
	requireGit(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	called := make(chan struct{}, 1)
	s.RegisterRunner("pi", runnerFunc(func(context.Context, Task, io.Writer) error {
		called <- struct{}{}
		return nil
	}))
	dir := t.TempDir()
	task, err := s.CreateTask("plain", "do work", dir)
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
	select {
	case <-called:
		t.Fatal("runner ran without a prepared worktree")
	default:
	}
	if !strings.Contains(done.Error, "prepare isolated workspace") {
		t.Fatalf("unexpected error: %q", done.Error)
	}
	if done.ExecutionWorkspace != "" {
		t.Fatalf("recorded workspace after failed preparation: %q", done.ExecutionWorkspace)
	}
}

func TestIsolatedSubdirectoryWorkspace(t *testing.T) {
	repo := committedRepo(t)
	sub := filepath.Join(repo, "sub")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "inside.txt"), []byte("inside\n"), 0600); err != nil {
		t.Fatal(err)
	}
	testGit(t, repo, "add", "sub/inside.txt")
	testGit(t, repo, "commit", "-q", "-m", "subdirectory")
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	seen := make(chan Task, 1)
	s.RegisterRunner("pi", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		seen <- task
		return nil
	}))
	task, err := s.CreateTask("sub", "work in subdirectory", sub)
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
	run := <-seen
	if filepath.Base(run.Workspace) != "sub" || !strings.Contains(run.Workspace, "tree") {
		t.Fatalf("runner cwd %q is not the worktree subdirectory", run.Workspace)
	}
	if _, err := os.Stat(filepath.Join(run.Workspace, "inside.txt")); err != nil {
		t.Fatalf("subdirectory content missing: %v", err)
	}
}

func TestIsolatedWorkspaceOwnershipMismatch(t *testing.T) {
	repo := committedRepo(t)
	statePath := filepath.Join(t.TempDir(), "state.json")
	s := openTestService(t, statePath)
	s.RegisterRunner("pi", runnerFunc(func(context.Context, Task, io.Writer) error {
		return errors.New("attempt failed")
	}))
	task, err := s.CreateTask("owner", "do work", repo)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetTaskIsolation(task.ID, true); err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	await(t, s, task.ID, Failed)
	ownerPath := filepath.Join(filepath.Dir(statePath), "workspaces", task.ID, "owner.json")
	if err := os.WriteFile(ownerPath, []byte(`{"task_id":"someone-else"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.Retry(task.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	done := await(t, s, task.ID, Failed)
	if !strings.Contains(done.Error, "ownership") {
		t.Fatalf("tampered reservation was accepted: %q", done.Error)
	}
}

func TestIsolatedWorkspaceRejectsSymlinkReservation(t *testing.T) {
	repo := committedRepo(t)
	statePath := filepath.Join(t.TempDir(), "state.json")
	s := openTestService(t, statePath)
	s.RegisterRunner("pi", runnerFunc(func(context.Context, Task, io.Writer) error {
		return errors.New("attempt failed")
	}))
	task, err := s.CreateTask("symlink", "do work", repo)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetTaskIsolation(task.ID, true); err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	await(t, s, task.ID, Failed)
	reservation := filepath.Join(filepath.Dir(statePath), "workspaces", task.ID)
	if err := os.RemoveAll(reservation); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), reservation); err != nil {
		t.Fatal(err)
	}
	if err = s.Retry(task.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	done := await(t, s, task.ID, Failed)
	if !strings.Contains(done.Error, "regular directory") {
		t.Fatalf("symlink reservation was accepted: %q", done.Error)
	}
}

func TestIsolatedWorkspaceSurvivesTaskDeletion(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	s.RegisterRunner("pi", runnerFunc(func(context.Context, Task, io.Writer) error { return nil }))
	task, err := s.CreateTask("delete", "do work", repo)
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
	if done.ExecutionWorkspace == "" {
		t.Fatal("no execution workspace recorded")
	}
	if err = s.DeleteTask(task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(done.ExecutionWorkspace); err != nil {
		t.Fatalf("worktree was removed with the task: %v", err)
	}
}

func TestIsolationToggleRules(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	if err := s.SetConcurrencyLimit(1); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	s.RegisterRunner("pi", runnerFunc(func(ctx context.Context, _ Task, _ io.Writer) error {
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}))
	first, err := s.CreateTask("first", "one", repo)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetTaskIsolation(first.ID, true); err != nil {
		t.Fatal(err)
	}
	if err = s.SetTaskIsolation(first.ID, true); err != nil {
		t.Fatalf("repeated toggle failed: %v", err)
	}
	if err = s.SetTaskIsolation("missing", true); err == nil {
		t.Fatal("unknown task accepted")
	}
	if err = s.Start(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	<-started
	if err = s.SetTaskIsolation(first.ID, false); err == nil || !strings.Contains(err.Error(), "unscheduled queued") {
		t.Fatalf("running task toggle error: %v", err)
	}
	second, err := s.CreateTask("second", "two", repo)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), second.ID); err != nil {
		t.Fatal(err)
	}
	scheduled, _ := s.GetTask(second.ID)
	if !scheduled.ScheduleRequested {
		t.Fatal("second task was not scheduled")
	}
	if err = s.SetTaskIsolation(second.ID, true); err == nil || !strings.Contains(err.Error(), "unscheduled queued") {
		t.Fatalf("scheduled task toggle error: %v", err)
	}
	if err = s.Cancel(first.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Cancel(second.ID); err != nil {
		t.Fatal(err)
	}
	await(t, s, first.ID, Canceled)
	await(t, s, second.ID, Canceled)
}

func TestPreparedIsolatedWorkspaceCannotBeToggled(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	s.RegisterRunner("pi", runnerFunc(func(context.Context, Task, io.Writer) error {
		return errors.New("attempt failed")
	}))
	task, err := s.CreateTask("prepared", "do work", repo)
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
	if done.ExecutionWorkspace == "" {
		t.Fatal("workspace was not prepared")
	}
	if err = s.Retry(task.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.SetTaskIsolation(task.ID, false); err == nil || !strings.Contains(err.Error(), "retained for retries") {
		t.Fatalf("prepared workspace toggle error: %v", err)
	}
}
