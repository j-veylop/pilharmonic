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
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
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
	defer func() { _ = restored.Shutdown() }()
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
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
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
	dir := t.TempDir()
	task, err := s.CreateTask("plain", "do work", dir)
	if err != nil {
		t.Fatal(err)
	}
	// Isolation is validated when it is requested, not when the attempt starts.
	err = s.SetTaskIsolation(task.ID, true)
	if err == nil || !strings.Contains(err.Error(), "cannot be isolated") {
		t.Fatalf("isolation accepted for a non-repository: %v", err)
	}
	got, _ := s.GetTask(task.ID)
	if got.Isolated || got.IsolationAuto {
		t.Fatalf("refused isolation changed the task: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(s.path), "workspaces", task.ID)); !os.IsNotExist(err) {
		t.Fatalf("refused isolation created a reservation: %v", err)
	}
	found := false
	for _, event := range got.Events {
		if event.Kind == "workspace" && strings.Contains(event.Message, "Isolation unavailable") {
			found = true
		}
	}
	if !found {
		t.Fatalf("isolation refusal was not recorded: %+v", got.Events)
	}
}

func TestIsolatedExecutionRequiresCommit(t *testing.T) {
	requireGit(t)
	repo := t.TempDir()
	testGit(t, repo, "init", "-q")
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	task, err := s.CreateTask("unborn", "do work", repo)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetTaskIsolation(task.ID, true); err == nil || !strings.Contains(err.Error(), "committed HEAD") {
		t.Fatalf("isolation accepted for an unborn repository: %v", err)
	}
	if got, _ := s.GetTask(task.ID); got.Isolated {
		t.Fatalf("unborn repository task was isolated: %+v", got)
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
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
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
	s.RegisterRunner("general", runnerFunc(func(context.Context, Task, io.Writer) error {
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
	s.RegisterRunner("general", runnerFunc(func(context.Context, Task, io.Writer) error {
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

func TestIsolatedWorkspaceIsCleanedUpWithTaskDeletion(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	s.RegisterRunner("general", runnerFunc(func(context.Context, Task, io.Writer) error { return nil }))
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
	reservation := filepath.Join(filepath.Dir(s.path), "workspaces", task.ID)
	if _, err := os.Stat(reservation); err != nil {
		t.Fatalf("worktree reservation missing before deletion: %v", err)
	}
	if err = s.DeleteTask(task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(done.ExecutionWorkspace); !os.IsNotExist(err) {
		t.Fatalf("worktree was retained after deletion: %v", err)
	}
	if _, err := os.Stat(reservation); !os.IsNotExist(err) {
		t.Fatalf("workspace reservation was retained after deletion: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "tracked.txt")); err != nil {
		t.Fatalf("source workspace was changed: %v", err)
	}
	if list := testGit(t, repo, "worktree", "list", "--porcelain"); strings.Contains(list, done.ExecutionWorkspace) {
		t.Fatalf("source repository still registers the removed worktree: %s", list)
	}
}

func TestCanceledIsolatedWorkspaceIsRetainedUntilDeletion(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	started := make(chan struct{})
	s.RegisterRunner("general", runnerFunc(func(ctx context.Context, task Task, _ io.Writer) error {
		if err := os.WriteFile(filepath.Join(task.Workspace, "canceled.txt"), []byte("partial work"), 0600); err != nil {
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
	if _, err := os.Stat(filepath.Join(done.ExecutionWorkspace, "canceled.txt")); err != nil {
		t.Fatalf("canceled worktree was cleaned up before deletion: %v", err)
	}
	if err = s.DeleteTask(task.ID); err != nil {
		t.Fatal(err)
	}
	reservation := filepath.Join(filepath.Dir(s.path), "workspaces", task.ID)
	if _, err := os.Stat(reservation); !os.IsNotExist(err) {
		t.Fatalf("canceled worktree reservation was retained after deletion: %v", err)
	}
}

func TestDeleteCompletedTasksCleansUpWorktrees(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	s.RegisterRunner("general", runnerFunc(func(context.Context, Task, io.Writer) error { return nil }))
	var tasks []Task
	for _, title := range []string{"first", "second"} {
		task, err := s.CreateTask(title, "fix the parser", repo)
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
	}
	for _, task := range tasks {
		if err := s.Start(context.Background(), task.ID); err != nil {
			t.Fatal(err)
		}
	}
	var reservations []string
	for _, task := range tasks {
		done := await(t, s, task.ID, Completed)
		if done.ExecutionWorkspace == "" {
			t.Fatalf("task %s has no prepared worktree", task.ID)
		}
		reservations = append(reservations, filepath.Join(filepath.Dir(s.path), "workspaces", task.ID))
	}
	deleted, err := s.DeleteCompletedTasks(s.CompletedDeletionCandidates())
	if err != nil {
		t.Fatal(err)
	}
	if deleted != len(tasks) {
		t.Fatalf("deleted %d completed tasks, want %d", deleted, len(tasks))
	}
	for _, reservation := range reservations {
		if _, err := os.Stat(reservation); !os.IsNotExist(err) {
			t.Fatalf("worktree reservation was retained after bulk deletion: %v", err)
		}
	}
}

func TestIsolationToggleRules(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	if err := s.SetConcurrencyLimit(1); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	s.RegisterRunner("general", runnerFunc(func(ctx context.Context, _ Task, _ io.Writer) error {
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
	s.RegisterRunner("general", runnerFunc(func(context.Context, Task, io.Writer) error {
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

func TestNewChangeTasksDefaultToIsolationInCommittedRepository(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	seen := make(chan Task, 1)
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		seen <- task
		return nil
	}))
	task, err := s.CreateTask("default isolated", "fix the parser", repo)
	if err != nil {
		t.Fatal(err)
	}
	if !task.Isolated {
		t.Fatal("new change task did not default to isolation")
	}
	foundEvent := false
	for _, event := range task.Events {
		if event.Kind == "workspace" && strings.Contains(event.Message, "by default") {
			foundEvent = true
		}
	}
	if !foundEvent {
		t.Fatalf("default isolation was not recorded: %+v", task.Events)
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	done := await(t, s, task.ID, Completed)
	run := <-seen
	wantCwd := filepath.Join(filepath.Dir(s.path), "workspaces", task.ID, "tree")
	if run.Workspace != wantCwd || done.ExecutionWorkspace != wantCwd {
		t.Fatalf("default isolation did not prepare the worktree: run=%q task=%q", run.Workspace, done.ExecutionWorkspace)
	}
}

func TestNewTasksUseSharedWorkspaceWhenIsolationIsUnavailable(t *testing.T) {
	dir := t.TempDir()
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	seen := make(chan Task, 1)
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		seen <- task
		return nil
	}))
	task, err := s.CreateTask("plain", "do work", dir)
	if err != nil {
		t.Fatal(err)
	}
	if task.Isolated {
		t.Fatal("task without an isolatable repository requested isolation")
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	done := await(t, s, task.ID, Completed)
	run := <-seen
	if run.Workspace != dir || done.ExecutionWorkspace != "" {
		t.Fatalf("task did not use the shared workspace: run=%q execution=%q", run.Workspace, done.ExecutionWorkspace)
	}
}

func TestDefaultIsolationCanBeDisabled(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	if err := s.SetDefaultIsolation(false); err != nil {
		t.Fatal(err)
	}
	seen := make(chan Task, 1)
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error {
		seen <- task
		return nil
	}))
	task, err := s.CreateTask("shared", "fix the parser", repo)
	if err != nil {
		t.Fatal(err)
	}
	if task.Isolated {
		t.Fatal("default isolation override was ignored")
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	await(t, s, task.ID, Completed)
	if run := <-seen; run.Workspace != repo {
		t.Fatalf("runner cwd %q, want shared source %q", run.Workspace, repo)
	}
}

// A crash between creating the reservation directory and writing its ownership
// record must not block every later attempt.
func TestEmptyReservationIsRecreated(t *testing.T) {
	repo := committedRepo(t)
	statePath := filepath.Join(t.TempDir(), "state.json")
	s := openTestService(t, statePath)
	s.RegisterRunner("general", runnerFunc(func(context.Context, Task, io.Writer) error { return nil }))
	task, err := s.CreateTask("empty", "do work", repo)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetTaskIsolation(task.ID, true); err != nil {
		t.Fatal(err)
	}
	reservation := filepath.Join(filepath.Dir(statePath), "workspaces", task.ID)
	if err = os.MkdirAll(reservation, 0700); err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	done := await(t, s, task.ID, Completed)
	if done.ExecutionWorkspace == "" {
		t.Fatalf("empty reservation was not recreated: %+v", done)
	}
	if _, err := os.Stat(filepath.Join(done.ExecutionWorkspace, "tracked.txt")); err != nil {
		t.Fatalf("worktree was not prepared: %v", err)
	}
}

// An interrupted worktree add leaves a directory Git does not recognise and a
// stale registration; a retry must repair both instead of failing forever.
func TestBrokenWorktreeIsRepaired(t *testing.T) {
	repo := committedRepo(t)
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	var mu sync.Mutex
	attempts := 0
	s.RegisterRunner("general", runnerFunc(func(context.Context, Task, io.Writer) error {
		mu.Lock()
		attempts++
		first := attempts == 1
		mu.Unlock()
		if first {
			return errors.New("first attempt failed")
		}
		return nil
	}))
	task, err := s.CreateTask("broken", "do work", repo)
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
	tree := failed.ExecutionWorkspace
	if tree == "" {
		t.Fatal("worktree was not prepared")
	}
	if err = os.RemoveAll(tree); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(tree, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(tree, "partial"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.Retry(task.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	await(t, s, task.ID, Completed)
	if _, err = os.Stat(filepath.Join(tree, "tracked.txt")); err != nil {
		t.Fatalf("broken worktree was not repaired: %v", err)
	}
	if _, err = os.Stat(filepath.Join(tree, "partial")); !os.IsNotExist(err) {
		t.Fatalf("partial worktree content survived repair: %v", err)
	}
}
