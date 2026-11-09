package orchestrator

import (
	"context"
	"io"
	"path/filepath"
	"sync"
	"testing"
)

func TestConcurrencyLimit(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	if err := s.SetConcurrencyLimit(1); err != nil {
		t.Fatal(err)
	}
	if err := s.SetConcurrencyLimit(0); err == nil {
		t.Fatal("zero concurrency accepted")
	}
	entered := make(chan struct{}, 2)
	unblock := make(chan struct{})
	s.RegisterRunner("pi", runnerFunc(func(ctx context.Context, _ Task, _ io.Writer) error {
		entered <- struct{}{}
		select {
		case <-unblock:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}))
	a, err := s.CreateTask("first", "one", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateTask("second", "two", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	<-entered
	var wg sync.WaitGroup
	var successes int
	var mu sync.Mutex
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.Start(context.Background(), b.ID) == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if successes != 1 {
		t.Fatalf("scheduled %d tasks at capacity", successes)
	}
	task, _ := s.GetTask(b.ID)
	if task.Status != Queued || !task.ScheduleRequested || len(task.Attempts) != 0 {
		t.Fatalf("rejected task changed: %+v", task)
	}
	close(unblock)
	await(t, s, a.ID, Completed)
	await(t, s, b.ID, Completed)
}

func TestScheduledRequestSurvivesShutdown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetConcurrencyLimit(1); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	s.RegisterRunner("pi", runnerFunc(func(ctx context.Context, _ Task, _ io.Writer) error { close(entered); <-ctx.Done(); return ctx.Err() }))
	first, _ := s.CreateTask("first", "one", t.TempDir())
	second, _ := s.CreateTask("second", "two", t.TempDir())
	if err := s.Start(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := s.Start(context.Background(), second.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	waiting, _ := reopened.GetTask(second.ID)
	if waiting.Status != Queued || !waiting.ScheduleRequested {
		t.Fatalf("lost request: %+v", waiting)
	}
	reopened.RegisterRunner("pi", runnerFunc(func(_ context.Context, _ Task, _ io.Writer) error { return nil }))
	if err := reopened.SetConcurrencyLimit(1); err != nil {
		t.Fatal(err)
	}
	await(t, reopened, second.ID, Completed)
	if err := reopened.Shutdown(); err != nil {
		t.Fatal(err)
	}
}

func TestCancelScheduledContext(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "state.json"))
	if err := s.SetConcurrencyLimit(1); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	s.RegisterRunner("pi", runnerFunc(func(ctx context.Context, _ Task, _ io.Writer) error { close(entered); <-ctx.Done(); return ctx.Err() }))
	first, _ := s.CreateTask("first", "one", t.TempDir())
	second, _ := s.CreateTask("second", "two", t.TempDir())
	if err := s.Start(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	if err := s.Start(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	cancel()
	canceled := await(t, s, second.ID, Canceled)
	if canceled.ScheduleRequested || len(canceled.Attempts) != 0 {
		t.Fatalf("bad cancellation: %+v", canceled)
	}
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
}
