package orchestrator

import (
	"context"
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
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, _ = s.GetTask(owner.ID)
		if got.MergeStatus == MergeMerged {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pending merge did not complete: %+v", got)
		}
		time.Sleep(time.Millisecond)
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
