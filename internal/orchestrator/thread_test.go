package orchestrator

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"
)

func TestThreadFanoutAndIdempotency(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	first, err := s.CreateTask("one", "first mandate", workspace)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateRelatedTask(first.ID, "two", "second mandate", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if first.ThreadID != second.ThreadID {
		t.Fatal("related task has different thread")
	}
	if err := s.SendThreadContext(first.ID, "id-1", "shared context"); err != nil {
		t.Fatal(err)
	}
	third, err := s.CreateRelatedTask(first.ID, "three", "third mandate", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SendThreadContext(first.ID, "id-1", "shared context"); err != nil {
		t.Fatal(err)
	}
	if err := s.SendThreadContext(first.ID, "id-1", "conflict"); err == nil {
		t.Fatal("accepted conflicting trigger")
	}
	if err := s.AddContext(first.ID, "id-1", "shared context"); err == nil {
		t.Fatal("accepted thread trigger as a task-only retry")
	}
	if err := s.AddContext(first.ID, first.Trigger.ID, first.Prompt); err == nil {
		t.Fatal("accepted the original mandate as a context retry")
	}
	a, _ := s.GetTask(first.ID)
	b, _ := s.GetTask(second.ID)
	c, _ := s.GetTask(third.ID)
	if len(a.Context) != 1 || len(b.Context) != 1 || len(c.Context) != 0 || a.Prompt != "first mandate" || b.Prompt != "second mandate" {
		t.Fatalf("wrong recipients or mandates: %+v %+v %+v", a, b, c)
	}
	reloaded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = reloaded.GetTask(second.ID)
	if len(b.Context) != 1 || b.Context[0].Delivery != "at_start" {
		t.Fatalf("not persisted: %+v", b.Context)
	}
	if err := reloaded.AddContext(first.ID, "direct", "for first task"); err != nil {
		t.Fatal(err)
	}
	if err := reloaded.AddContext(second.ID, "direct", "for first task"); err == nil {
		t.Fatal("accepted another task's trigger as a retry")
	}
}

func TestThreadFanoutUnavailableRunnerAndRollback(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	first, _ := s.CreateTask("one", "first", workspace)
	second, _ := s.CreateRelatedTask(first.ID, "two", "second", workspace)
	started := make(chan struct{})
	s.RegisterRunner("general", runnerFunc(func(ctx context.Context, _ Task, _ io.Writer) error { close(started); <-ctx.Done(); return ctx.Err() }))
	if err := s.Start(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := s.SendThreadContext(first.ID, "id-2", "ctx"); err != nil {
		t.Fatal(err)
	}
	a, _ := s.GetTask(first.ID)
	b, _ := s.GetTask(second.ID)
	if a.Context[0].Delivery != "rejected" || b.Context[0].Delivery != "at_start" {
		t.Fatalf("wrong outcomes: %+v %+v", a.Context, b.Context)
	}
	// A failed durable write must leave no partial records or sends.
	s.mu.Lock()
	oldPath := s.path
	s.path = t.TempDir()
	s.mu.Unlock()
	if err := s.SendThreadContext(first.ID, "id-3", "not persisted"); err == nil {
		t.Fatal("expected persistence failure")
	}
	s.mu.Lock()
	s.path = oldPath
	s.mu.Unlock()
	a, _ = s.GetTask(first.ID)
	b, _ = s.GetTask(second.ID)
	if len(a.Context) != 1 || len(b.Context) != 1 {
		t.Fatal("failed write changed context")
	}
	if err := s.Cancel(first.ID); err != nil {
		t.Fatal(err)
	}
	await(t, s, first.ID, Canceled)
	if err := s.Cancel(second.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SendThreadContext(first.ID, "id-4", "no subscribers"); err == nil {
		t.Fatal("accepted without recipients")
	}
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
}

func TestThreadFanoutRunningDelivery(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	first, _ := s.CreateTask("one", "first", t.TempDir())
	started := make(chan struct{})
	received := make(chan Trigger, 1)
	s.RegisterRunner("general", testThreadRunner{started: started, received: received})
	if err := s.Start(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := s.SendThreadContext(first.ID, "live-1", "ctx"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-received:
		if got.ID != "live-1" {
			t.Fatal(got)
		}
	case <-time.After(time.Second):
		t.Fatal("delivery timed out")
	}
	await(t, s, first.ID, Completed)
	got, _ := s.GetTask(first.ID)
	if got.Context[0].Delivery != "accepted" {
		t.Fatalf("delivery: %+v", got.Context)
	}
}

type testThreadRunner struct {
	started  chan struct{}
	received chan Trigger
}

func (r testThreadRunner) Run(context.Context, Task, io.Writer) error { return nil }
func (r testThreadRunner) RunLive(ctx context.Context, _ Task, _ io.Writer, follow <-chan Trigger, ack func(string, bool, string)) error {
	close(r.started)
	select {
	case trigger := <-follow:
		ack(trigger.ID, true, "queued")
		r.received <- trigger
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestExplicitContextSubscription(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.CreateTask("first", "first mandate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateRelatedTask(first.ID, "second", "second mandate", first.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetContextSubscription(second.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := s.SendThreadContext(first.ID, "while-paused", "context"); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	second, _ = restored.GetTask(second.ID)
	if !second.ContextPaused || len(second.Context) != 0 {
		t.Fatalf("paused subscriber received input: %+v", second)
	}
	if err := restored.SetContextSubscription(second.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := restored.SendThreadContext(first.ID, "while-paused", "context"); err != nil {
		t.Fatal(err)
	}
	second, _ = restored.GetTask(second.ID)
	if len(second.Context) != 0 {
		t.Fatal("duplicate replayed to resumed subscriber")
	}
	if err := restored.SendThreadContext(first.ID, "after-resume", "new context"); err != nil {
		t.Fatal(err)
	}
	second, _ = restored.GetTask(second.ID)
	if len(second.Context) != 1 || second.Prompt != "second mandate" {
		t.Fatalf("invalid resumed context: %+v", second)
	}
	restored.path = t.TempDir()
	if err := restored.SetContextSubscription(second.ID, false); err == nil {
		t.Fatal("expected persistence error")
	}
	second, _ = restored.GetTask(second.ID)
	if second.ContextPaused {
		t.Fatal("failed change was not rolled back")
	}
}

func TestFollowUpRunningTaskUsesLiveContext(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()
	task, err := s.CreateTask("work", "original mandate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	received := make(chan Trigger, 1)
	s.RegisterRunner("general", testThreadRunner{started: started, received: received})
	if err := s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	<-started
	requeued, err := s.FollowUp(task.ID, "adjust the implementation")
	if err != nil || requeued {
		t.Fatalf("follow-up: requeued=%v err=%v", requeued, err)
	}
	select {
	case got := <-received:
		if got.Text != "adjust the implementation" {
			t.Fatalf("wrong live context: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("live follow-up timed out")
	}
	await(t, s, task.ID, Completed)
	got, _ := s.GetTask(task.ID)
	if got.Prompt != "original mandate" || len(got.Context) != 1 || got.Context[0].Delivery != DeliveryAccepted {
		t.Fatalf("unexpected task after follow-up: %+v", got)
	}
}

func TestFollowUpRequeuesTerminalTaskWithoutChangingMandate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()
	s.RegisterRunner("general", runnerFunc(func(context.Context, Task, io.Writer) error { return nil }))
	task, err := s.CreateTask("work", "original mandate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	await(t, s, task.ID, Completed)

	requeued, err := s.FollowUp(task.ID, "please also update the documentation")
	if err != nil || !requeued {
		t.Fatalf("follow-up: requeued=%v err=%v", requeued, err)
	}
	got, _ := s.GetTask(task.ID)
	if got.Status != Queued || got.ScheduleRequested {
		t.Fatalf("task not queued for an explicit start: %+v", got)
	}
	if got.Prompt != "original mandate" || len(got.Context) != 1 || got.Context[0].Delivery != DeliveryAtStart {
		t.Fatalf("follow-up changed mandate or context: %+v", got)
	}
	if len(got.Attempts) != 1 {
		t.Fatalf("follow-up rewrote attempts: %+v", got.Attempts)
	}

	if err = s.Start(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	await(t, s, task.ID, Completed)
	got, _ = s.GetTask(task.ID)
	if len(got.Attempts) != 2 || got.Context[0].Text != "please also update the documentation" {
		t.Fatalf("new attempt did not receive follow-up: %+v", got)
	}

	reloaded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reloaded.Shutdown() }()
	got, _ = reloaded.GetTask(task.ID)
	if len(got.Context) != 1 || got.Context[0].Text != "please also update the documentation" {
		t.Fatalf("follow-up did not persist: %+v", got)
	}
}

func TestAddManualThreadContextReachesSubscribers(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()
	task, err := s.CreateTask("thread", "original mandate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AddManualThreadContext(task.ID, "shared context"); err != nil {
		t.Fatal(err)
	}
	updated, ok := s.GetTask(task.ID)
	if !ok || len(updated.Context) != 1 || updated.Context[0].Text != "shared context" {
		t.Fatalf("thread context not recorded: %+v", updated.Context)
	}
}
