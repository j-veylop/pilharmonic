package orchestrator

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type runnerFunc func(context.Context, Task, io.Writer) error

func (f runnerFunc) Run(ctx context.Context, t Task, w io.Writer) error { return f(ctx, t, w) }
func await(t *testing.T, s *Service, id string, want Status) Task {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		task, _ := s.GetTask(id)
		if task.Status == want {
			return task
		}
		time.Sleep(time.Millisecond * 5)
	}
	task, _ := s.GetTask(id)
	t.Fatalf("got status %s, want %s", task.Status, want)
	return Task{}
}
func TestRouteRunAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.RegisterRunner("pi", runnerFunc(func(ctx context.Context, task Task, out io.Writer) error {
		_, err := io.WriteString(out, "work done")
		return err
	}))
	task, err := s.CreateTask("build", "make a thing", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if task.Decision.Outcome != "accept" || task.Trigger.Text != task.Prompt {
		t.Fatalf("bad route: %+v", task)
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	done := await(t, s, task.ID, Completed)
	if done.Output != "work done" {
		t.Fatalf("output %q", done.Output)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	restored, ok := again.GetTask(task.ID)
	if !ok || restored.Status != Completed || len(restored.Events) != 3 {
		t.Fatalf("bad restored task: %+v", restored)
	}
	if err = again.Start(context.Background(), task.ID); err == nil {
		t.Fatal("started completed task")
	}
}
func TestCancelAndRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, _ := Open(path)
	started := make(chan struct{})
	s.RegisterRunner("pi", runnerFunc(func(ctx context.Context, _ Task, _ io.Writer) error { close(started); <-ctx.Done(); return ctx.Err() }))
	task, err := s.CreateTask("x", "y", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	<-started
	// A fresh service models restarting after a crash; in normal operation only one process owns the state file.
	restored, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := restored.GetTask(task.ID); got.Status != Interrupted {
		t.Fatalf("got %s", got.Status)
	}
	if err = s.Cancel(task.ID); err != nil {
		t.Fatal(err)
	}
	await(t, s, task.ID, Canceled)
}
func TestValidationAndFailure(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "state.json"))
	if _, err := s.CreateTask("", "x", t.TempDir()); err == nil {
		t.Fatal("empty title accepted")
	}
	if _, err := s.CreateTask("x", "y", filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing workspace accepted")
	}
	s.RegisterRunner("pi", runnerFunc(func(context.Context, Task, io.Writer) error { return errors.New("runner failed") }))
	task, _ := s.CreateTask("x", "y", t.TempDir())
	if err := s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	done := await(t, s, task.ID, Failed)
	if !strings.Contains(done.Error, "runner failed") {
		t.Fatal(done.Error)
	}
}

func TestShutdownCancelsAndWaits(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "state.json"))
	started := make(chan struct{})
	s.RegisterRunner("pi", runnerFunc(func(ctx context.Context, _ Task, _ io.Writer) error { close(started); <-ctx.Done(); return ctx.Err() }))
	task, _ := s.CreateTask("x", "y", t.TempDir())
	if err := s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetTask(task.ID)
	if got.Status != Canceled {
		t.Fatalf("got %s", got.Status)
	}
	next, _ := s.CreateTask("next", "y", t.TempDir())
	if err := s.Start(context.Background(), next.ID); err == nil {
		t.Fatal("started after shutdown")
	}
}

func TestContextRoutingAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTask("build", "original mandate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddContext(task.ID, "followup-1", "use the existing style"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddContext(task.ID, "followup-1", "use the existing style"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddContext(task.ID, "followup-1", "different"); err == nil {
		t.Fatal("accepted conflicting trigger")
	}
	restored, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := restored.GetTask(task.ID)
	if got.Prompt != "original mandate" || len(got.Context) != 1 || got.Context[0].Text != "use the existing style" {
		t.Fatalf("bad restored task: %+v", got)
	}
	if len(restored.data.Threads) != 1 || len(restored.data.Threads[0].Decisions) != 2 {
		t.Fatalf("bad thread: %+v", restored.data.Threads)
	}
	received := make(chan Task, 1)
	restored.RegisterRunner("pi", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error { received <- task; return nil }))
	if err := restored.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	sent := <-received
	if sent.Prompt != "original mandate" || len(sent.Context) != 1 {
		t.Fatalf("bad runner input: %+v", sent)
	}
	await(t, restored, task.ID, Completed)
	if err := restored.AddContext(task.ID, "followup-2", "late"); err == nil {
		t.Fatal("accepted context after start")
	}
}

func TestRetryPreservesAttempts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	runs := 0
	s.RegisterRunner("pi", runnerFunc(func(_ context.Context, _ Task, out io.Writer) error {
		runs++
		if runs == 1 {
			io.WriteString(out, "first attempt")
			return errors.New("failed first")
		}
		io.WriteString(out, "second attempt")
		return nil
	}))
	task, err := s.CreateTask("test", "original mandate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	await(t, s, task.ID, Failed)
	if err := s.Retry(task.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	done := await(t, s, task.ID, Completed)
	if len(done.Attempts) != 2 || done.Attempts[0].Status != Failed || done.Attempts[0].Output != "first attempt" || done.Attempts[1].Output != "second attempt" || done.Prompt != "original mandate" {
		t.Fatalf("unexpected attempts: %+v", done)
	}
	if err := s.Retry(task.ID); err == nil {
		t.Fatal("retried completed task")
	}
	reloaded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := reloaded.GetTask(task.ID)
	if len(got.Attempts) != 2 || got.Attempts[0].Error != "failed first" {
		t.Fatalf("attempt history lost: %+v", got.Attempts)
	}
}
