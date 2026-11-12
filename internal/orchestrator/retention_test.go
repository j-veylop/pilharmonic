package orchestrator

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
)

// TestAttemptRetentionDropsOlderOutputs proves that retries cannot grow the
// state file without bound while attempt identity and outcome stay intact.
func TestAttemptRetentionDropsOlderOutputs(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()
	if err = s.SetAttemptRetention(1); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, _ Task, out io.Writer) error {
		attempts++
		if attempts == 1 {
			_, _ = io.WriteString(out, "first attempt output")
			return errors.New("failed first")
		}
		_, _ = io.WriteString(out, "second attempt output")
		return nil
	}))
	task, err := s.CreateTask("retry", "original mandate", t.TempDir())
	if err != nil {
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
	if len(done.Attempts) != 2 {
		t.Fatalf("attempts %+v", done.Attempts)
	}
	first := done.Attempts[0]
	if first.Output != "" || !first.OutputTruncated || first.Error != "failed first" {
		t.Fatalf("older attempt not retained correctly: %+v", first)
	}
	if done.Attempts[1].Output != "second attempt output" || done.Attempts[1].OutputTruncated {
		t.Fatalf("newest attempt output missing: %+v", done.Attempts[1])
	}
	found := false
	for _, event := range done.Events {
		if event.Kind == "retention" {
			found = true
		}
	}
	if !found {
		t.Fatal("retention event not recorded")
	}
}

func TestAttemptRetentionKeepsEverythingWhenDisabled(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()
	if err = s.SetAttemptRetention(0); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, _ Task, out io.Writer) error {
		attempts++
		if attempts == 1 {
			_, _ = io.WriteString(out, "first attempt output")
			return errors.New("failed first")
		}
		return nil
	}))
	task, err := s.CreateTask("keep", "original mandate", t.TempDir())
	if err != nil {
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
	if done.Attempts[0].Output != "first attempt output" || done.Attempts[0].OutputTruncated {
		t.Fatalf("disabled retention dropped output: %+v", done.Attempts[0])
	}
}

func TestAttemptRetentionRejectsNegative(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()
	if s.SetAttemptRetention(-1) == nil {
		t.Fatal("expected error for negative retention")
	}
}
