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

func TestDeleteTaskPreservesOtherTasksAndWorkspace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	file := filepath.Join(workspace, "keep.txt")
	if err := os.WriteFile(file, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateTask("a", "first", workspace)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateTask("b", "second", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddContext(a.ID, "context-one", "information"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTask(a.ID); err != nil {
		t.Fatal(err)
	}
	if contents, err := os.ReadFile(file); err != nil || string(contents) != "keep" {
		t.Fatalf("workspace changed: %q, %v", contents, err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.GetTask(a.ID); ok {
		t.Fatal("deleted task restored")
	}
	if _, ok := s2.GetTask(b.ID); !ok {
		t.Fatal("other task missing")
	}
	if len(s2.data.Threads) != 1 || s2.data.Threads[0].ID != b.ThreadID {
		t.Fatalf("wrong threads: %+v", s2.data.Threads)
	}
	if err := s.DeleteTask(a.ID); err == nil {
		t.Fatal("missing task deletion succeeded")
	}
}

func TestDeleteRefusesRunningAndRollsBackFailedSave(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	s.RegisterRunner("pi", runnerFunc(func(ctx context.Context, _ Task, _ io.Writer) error { close(started); <-ctx.Done(); return ctx.Err() }))
	task, err := s.CreateTask("a", "prompt", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	<-started
	if err = s.DeleteTask(task.ID); err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("delete running: %v", err)
	}
	if err = s.Cancel(task.ID); err != nil {
		t.Fatal(err)
	}
	await(t, s, task.ID, Canceled)
	savedPath := s.path
	s.path = t.TempDir()
	if err = s.DeleteTask(task.ID); err == nil {
		t.Fatal("failed save succeeded")
	}
	if _, ok := s.GetTask(task.ID); !ok || len(s.data.Threads) != 1 {
		t.Fatal("failed save did not restore state")
	}
	s.path = savedPath
	if err = s.DeleteTask(task.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Shutdown(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestDeleteRefusesParentWithSubagents(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	started := make(chan struct{}, 2)
	s.RegisterRunner("pi", runnerFunc(func(ctx context.Context, task Task, _ io.Writer) error {
		started <- struct{}{}
		if task.ParentID == "" {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}))
	parent, err := s.CreateTask("parent", "parent mandate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), parent.ID); err != nil {
		t.Fatal(err)
	}
	<-started
	child, err := s.SpawnSubagent(parent.ID, "child", "child mandate", "")
	if err != nil {
		t.Fatal(err)
	}
	await(t, s, child.ID, Completed)
	if err = s.Cancel(parent.ID); err != nil {
		t.Fatal(err)
	}
	await(t, s, parent.ID, Canceled)
	if err = s.DeleteTask(parent.ID); err == nil || !strings.Contains(err.Error(), "subagents") {
		t.Fatalf("parent with subagent deleted: %v", err)
	}
	if err = s.DeleteTask(child.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteTask(parent.ID); err != nil {
		t.Fatal(err)
	}
}
