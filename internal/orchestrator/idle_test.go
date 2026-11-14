package orchestrator

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIdleTimeoutCancelsSilentAgent(t *testing.T) {
	s := mustGet(Open(filepath.Join(t.TempDir(), "state.json")))(t)
	must(t, s.SetIdleTimeout(40*time.Millisecond))
	started := make(chan struct{})
	s.RegisterRunner("general", runnerFunc(func(ctx context.Context, _ Task, _ io.Writer) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}))
	task := createTask(t, s, "quiet", "run quietly", t.TempDir())
	must(t, s.Start(context.Background(), task.ID))
	<-started
	done := await(t, s, task.ID, Canceled)
	if !strings.Contains(done.Error, "idle timeout") {
		t.Fatalf("unexpected cancellation reason: %q", done.Error)
	}
}

func TestIdleTimeoutAllowsStreamingAgent(t *testing.T) {
	s := mustGet(Open(filepath.Join(t.TempDir(), "state.json")))(t)
	must(t, s.SetIdleTimeout(200*time.Millisecond))
	s.RegisterRunner("general", runnerFunc(func(ctx context.Context, _ Task, out io.Writer) error {
		for i := 0; i < 30; i++ {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(20 * time.Millisecond):
			}
			if _, err := io.WriteString(out, "."); err != nil {
				return err
			}
		}
		return nil
	}))
	task := createTask(t, s, "chatty", "stream output", t.TempDir())
	must(t, s.Start(context.Background(), task.ID))
	done := await(t, s, task.ID, Completed)
	if done.Output != strings.Repeat(".", 30) {
		t.Fatalf("output %q", done.Output)
	}
}

func TestIdleTimeoutRejectsNegative(t *testing.T) {
	s := mustGet(Open(filepath.Join(t.TempDir(), "state.json")))(t)
	if s.SetIdleTimeout(-time.Second) == nil {
		t.Fatal("expected error for negative timeout")
	}
}
