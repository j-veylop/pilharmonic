package orchestrator

import (
	"context"
	"io"
	"path/filepath"
	"sync"
	"testing"
)

func TestConcurrencyLimit(t *testing.T) {
	s := newTestService(t)
	must(t, s.SetConcurrencyLimit(1))
	if err := s.SetConcurrencyLimit(0); err == nil {
		t.Fatal("zero concurrency accepted")
	}
	entered := make(chan struct{}, 2)
	unblock := make(chan struct{})
	s.RegisterRunner("general", runnerFunc(func(ctx context.Context, _ Task, _ io.Writer) error {
		entered <- struct{}{}
		select {
		case <-unblock:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}))
	a := createTask(t, s, "first", "one", t.TempDir())
	b := createTask(t, s, "second", "two", t.TempDir())
	must(t, s.Start(context.Background(), a.ID))
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
	s := mustGet(Open(path))(t)
	must(t, s.SetConcurrencyLimit(1))
	entered := make(chan struct{})
	s.RegisterRunner("general", runnerFunc(func(ctx context.Context, _ Task, _ io.Writer) error { close(entered); <-ctx.Done(); return ctx.Err() }))
	first, _ := s.CreateTask("first", "one", t.TempDir())
	second, _ := s.CreateTask("second", "two", t.TempDir())
	must(t, s.Start(context.Background(), first.ID))
	<-entered
	must(t, s.Start(context.Background(), second.ID))
	must(t, s.Shutdown())
	reopened := mustGet(Open(path))(t)
	waiting, _ := reopened.GetTask(second.ID)
	if waiting.Status != Queued || !waiting.ScheduleRequested {
		t.Fatalf("lost request: %+v", waiting)
	}
	reopened.RegisterRunner("general", runnerFunc(func(_ context.Context, _ Task, _ io.Writer) error { return nil }))
	must(t, reopened.SetConcurrencyLimit(1))
	await(t, reopened, second.ID, Completed)
	must(t, reopened.Shutdown())
}

func TestCancelScheduledRequest(t *testing.T) {
	for _, method := range []string{"operator", "context"} {
		t.Run(method, func(t *testing.T) {
			workspace := t.TempDir()
			path := filepath.Join(workspace, "state.json")
			s := openTestService(t, path)
			s.RegisterRunner("general", runnerFunc(func(ctx context.Context, _ Task, _ io.Writer) error {
				<-ctx.Done()
				return ctx.Err()
			}))
			must(t, s.SetConcurrencyLimit(1))
			first, err := s.CreateTask("first", "one", workspace)
			if err != nil {
				t.Fatal(err)
			}
			must(t, s.Start(context.Background(), first.ID))
			second, err := s.CreateTask("second", "two", workspace)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			must(t, s.Start(ctx, second.ID))
			if method == "operator" {
				must(t, s.Cancel(second.ID))
			} else {
				cancel()
			}
			await(t, s, second.ID, Canceled)
			must(t, s.Shutdown())
			reopened := openTestService(t, path)
			task, ok := reopened.GetTask(second.ID)
			if !ok || task.Status != Canceled || task.ScheduleRequested || !task.ScheduledAt.IsZero() || len(task.Attempts) != 0 {
				t.Fatalf("unexpected persisted cancellation: %+v", task)
			}
		})
	}
}
