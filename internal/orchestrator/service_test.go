package orchestrator

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type runnerFunc func(context.Context, Task, io.Writer) error

func (f runnerFunc) Run(ctx context.Context, t Task, w io.Writer) error { return f(ctx, t, w) }

// must fails the test on an unexpected error.
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// mustGet returns a value, failing the test if the call that produced it
// returned an error: v := mustGet(f())(t).
func mustGet[T any](v T, err error) func(*testing.T) T {
	return func(t *testing.T) T {
		t.Helper()
		must(t, err)
		return v
	}
}

func createTask(t *testing.T, s *Service, title, prompt, workspace string) Task {
	t.Helper()
	task, err := s.CreateTask(title, prompt, workspace)
	must(t, err)
	return task
}

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
	t.Fatalf("got status %s, want %s (%s)", task.Status, want, task.Error)
	return Task{}
}

// awaitIntegration waits for an asynchronous approved merge to settle.
func awaitIntegration(t *testing.T, s *Service, id string) Task {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		task, _ := s.GetTask(id)
		switch task.MergeStatus {
		case MergeMerged, MergeUpToDate, MergeFailed:
			return task
		}
		time.Sleep(time.Millisecond * 5)
	}
	task, _ := s.GetTask(id)
	t.Fatalf("integration did not settle: %+v", task)
	return Task{}
}

// approveReview approves the pending commit and waits for the merge outcome.
func approveReview(t *testing.T, s *Service, id string) Task {
	t.Helper()
	task, ok := s.GetTask(id)
	if !ok {
		t.Fatal("task not found")
	}
	if !task.AwaitingReview() {
		t.Fatalf("task is not awaiting review: %+v", task)
	}
	must(t, s.ApproveReview(id, task.ReviewCommit, ""))
	return awaitIntegration(t, s, id)
}
func TestRouteRunAndReload(t *testing.T) {
	var err error
	path := filepath.Join(t.TempDir(), "state.json")
	s := mustGet(Open(path))(t)
	s.RegisterRunner("general", runnerFunc(func(ctx context.Context, task Task, out io.Writer) error {
		_, err := io.WriteString(out, "work done")
		return err
	}))
	task := createTask(t, s, "build", "make a thing", t.TempDir())
	if task.Decision.Outcome != "accept" || task.Trigger.Text != task.Prompt {
		t.Fatalf("bad route: %+v", task)
	}
	must(t, s.Start(context.Background(), task.ID))
	done := await(t, s, task.ID, Completed)
	if done.Output != "work done" {
		t.Fatalf("output %q", done.Output)
	}
	again := mustGet(Open(path))(t)
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
	s.RegisterRunner("general", runnerFunc(func(ctx context.Context, _ Task, _ io.Writer) error { close(started); <-ctx.Done(); return ctx.Err() }))
	task := createTask(t, s, "x", "y", t.TempDir())
	must(t, s.Start(context.Background(), task.ID))
	<-started
	// A fresh service models restarting after a crash; in normal operation only one process owns the state file.
	restored := mustGet(Open(path))(t)
	if got, _ := restored.GetTask(task.ID); got.Status != Interrupted {
		t.Fatalf("got %s", got.Status)
	}
	must(t, s.Cancel(task.ID))
	await(t, s, task.ID, Canceled)
}

func TestLegacyRunningTaskMigrationThenRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	fixture := `{"tasks":[{"id":"legacy-task","title":"old task","prompt":"old mandate","workspace":"/workspace","status":"running","agent":"general","created_at":"2026-01-02T03:04:05Z","updated_at":"2026-01-02T03:04:06Z","events":[{"time":"2026-01-02T03:04:06Z","kind":"started","message":"Started"}]}]}`
	must(t, os.WriteFile(path, []byte(fixture), 0600))

	s := mustGet(Open(path))(t)
	got, ok := s.GetTask("legacy-task")
	if !ok || got.Status != Interrupted {
		t.Fatalf("legacy task status = %q, found=%t", got.Status, ok)
	}
	if len(got.Attempts) != 1 {
		t.Fatalf("legacy task attempts = %+v, want reconstructed attempt", got.Attempts)
	}
	attempt := got.Attempts[0]
	if attempt.ID == "" || attempt.Status != Interrupted || attempt.FinishedAt.IsZero() || attempt.Error != got.Error {
		t.Fatalf("reconstructed attempt not interrupted: %+v; task error=%q", attempt, got.Error)
	}

	// Migration and recovery are persisted together, so another open preserves
	// the reconstructed attempt rather than inventing a second one.
	reloaded := mustGet(Open(path))(t)
	again, _ := reloaded.GetTask("legacy-task")
	if len(again.Attempts) != 1 || again.Attempts[0].ID != attempt.ID || again.Attempts[0].Status != Interrupted {
		t.Fatalf("reloaded legacy attempt changed: %+v", again.Attempts)
	}
}

func TestValidationAndFailure(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "state.json"))
	if _, err := s.CreateTask("", "x", t.TempDir()); err == nil {
		t.Fatal("empty title accepted")
	}
	if _, err := s.CreateTask("x", "y", filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing workspace accepted")
	}
	s.RegisterRunner("general", runnerFunc(func(context.Context, Task, io.Writer) error { return errors.New("runner failed") }))
	task, _ := s.CreateTask("x", "y", t.TempDir())
	must(t, s.Start(context.Background(), task.ID))
	done := await(t, s, task.ID, Failed)
	if !strings.Contains(done.Error, "runner failed") {
		t.Fatal(done.Error)
	}
}

func TestShutdownCancelsAndWaits(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "state.json"))
	started := make(chan struct{})
	s.RegisterRunner("general", runnerFunc(func(ctx context.Context, _ Task, _ io.Writer) error { close(started); <-ctx.Done(); return ctx.Err() }))
	task, _ := s.CreateTask("x", "y", t.TempDir())
	must(t, s.Start(context.Background(), task.ID))
	<-started
	must(t, s.Shutdown())
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
	s := mustGet(Open(path))(t)
	task := createTask(t, s, "build", "original mandate", t.TempDir())
	must(t, s.AddContext(task.ID, "followup-1", "use the existing style"))
	must(t, s.AddContext(task.ID, "followup-1", "use the existing style"))
	if err := s.AddContext(task.ID, "followup-1", "different"); err == nil {
		t.Fatal("accepted conflicting trigger")
	}
	restored := mustGet(Open(path))(t)
	got, _ := restored.GetTask(task.ID)
	if got.Prompt != "original mandate" || len(got.Context) != 1 || got.Context[0].Text != "use the existing style" {
		t.Fatalf("bad restored task: %+v", got)
	}
	if len(restored.data.Threads) != 1 || len(restored.data.Threads[0].Decisions) != 2 {
		t.Fatalf("bad thread: %+v", restored.data.Threads)
	}
	received := make(chan Task, 1)
	restored.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, _ io.Writer) error { received <- task; return nil }))
	must(t, restored.Start(context.Background(), task.ID))
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
	s := mustGet(Open(path))(t)
	runs := 0
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, _ Task, out io.Writer) error {
		runs++
		if runs == 1 {
			_, _ = io.WriteString(out, "first attempt")
			return errors.New("failed first")
		}
		_, _ = io.WriteString(out, "second attempt")
		return nil
	}))
	task := createTask(t, s, "test", "original mandate", t.TempDir())
	must(t, s.Start(context.Background(), task.ID))
	await(t, s, task.ID, Failed)
	must(t, s.Retry(task.ID))
	must(t, s.Start(context.Background(), task.ID))
	done := await(t, s, task.ID, Completed)
	if len(done.Attempts) != 2 || done.Attempts[0].Status != Failed || done.Attempts[0].Output != "first attempt" || done.Attempts[1].Output != "second attempt" || done.Prompt != "original mandate" {
		t.Fatalf("unexpected attempts: %+v", done)
	}
	if err := s.Retry(task.ID); err == nil {
		t.Fatal("retried completed task")
	}
	reloaded := mustGet(Open(path))(t)
	got, _ := reloaded.GetTask(task.ID)
	if len(got.Attempts) != 2 || got.Attempts[0].Error != "failed first" {
		t.Fatalf("attempt history lost: %+v", got.Attempts)
	}
}

// TestOutputBatchesUntilFlush proves streamed output stays in memory until the
// flush threshold, then reaches disk before the attempt ends. Lifecycle saves
// carry any buffered output, so completing a task never drops it.
func TestOutputBatchesUntilFlush(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := mustGet(Open(path))(t)
	small := strings.Repeat("x", 1024)
	large := strings.Repeat("y", outputFlushSize)
	wroteSmall := make(chan struct{})
	wroteLarge := make(chan struct{})
	release := make(chan struct{})
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, _ Task, out io.Writer) error {
		if _, err := io.WriteString(out, small); err != nil {
			return err
		}
		close(wroteSmall)
		<-release
		if _, err := io.WriteString(out, large); err != nil {
			return err
		}
		close(wroteLarge)
		return nil
	}))
	task := createTask(t, s, "stream", "stream output", t.TempDir())
	must(t, s.Start(context.Background(), task.ID))
	<-wroteSmall
	onDisk := mustGet(os.ReadFile(path))(t)
	if strings.Contains(string(onDisk), small) {
		t.Fatal("small output chunk persisted before reaching the flush threshold")
	}
	close(release)
	<-wroteLarge
	flushed := mustGet(os.ReadFile(path))(t)
	if !strings.Contains(string(flushed), large) {
		t.Fatal("output crossing the flush threshold was not persisted")
	}
	done := await(t, s, task.ID, Completed)
	if done.Output != small+large {
		t.Fatalf("output lost: got %d bytes, want %d", len(done.Output), len(small+large))
	}
}

func TestIdleTimeoutCancelsSilentAgent(t *testing.T) {
	s := newTestService(t)
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
	s := newTestService(t)
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

// TestAttemptRetention proves retries cannot grow the state file without bound
// while attempt identity and outcome stay intact; zero keeps every output.
func TestAttemptRetention(t *testing.T) {
	for _, keep := range []int{1, 0} {
		s := newTestService(t)
		must(t, s.SetAttemptRetention(keep))
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
		task := createTask(t, s, "retry", "original mandate", t.TempDir())
		must(t, s.Start(context.Background(), task.ID))
		await(t, s, task.ID, Failed)
		must(t, s.Retry(task.ID))
		must(t, s.Start(context.Background(), task.ID))
		done := await(t, s, task.ID, Completed)
		if len(done.Attempts) != 2 {
			t.Fatalf("attempts %+v", done.Attempts)
		}
		first := done.Attempts[0]
		if keep == 0 {
			if first.Output != "first attempt output" || first.OutputTruncated || hasEvent(t, done, "retention") {
				t.Fatalf("disabled retention dropped output: %+v", first)
			}
			continue
		}
		if first.Output != "" || !first.OutputTruncated || first.Error != "failed first" {
			t.Fatalf("older attempt not retained correctly: %+v", first)
		}
		if done.Attempts[1].Output != "second attempt output" || done.Attempts[1].OutputTruncated {
			t.Fatalf("newest attempt output missing: %+v", done.Attempts[1])
		}
		if !hasEvent(t, done, "retention") {
			t.Fatal("retention event not recorded")
		}
	}
}

func TestSettersRejectInvalidValues(t *testing.T) {
	s := newTestService(t)
	for name, err := range map[string]error{
		"zero concurrency":        s.SetConcurrencyLimit(0),
		"negative idle timeout":   s.SetIdleTimeout(-time.Second),
		"negative retention":      s.SetAttemptRetention(-1),
		"negative subagent depth": s.SetSubagentLimits(-1, 1),
	} {
		if err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// TestConfigureProfiles covers the startup profiles file: the default beside
// the state file is optional, an explicit path is required, an invalid file
// fails startup, and the shipped profiles.json leaves coordination guidance
// to the compiled default.
func TestConfigureProfiles(t *testing.T) {
	dir := t.TempDir()
	s := openTestService(t, filepath.Join(dir, "state.json"))
	options := Options{MaxConcurrent: 1}
	if loaded, err := s.Configure(options); err != nil || loaded != "" {
		t.Fatalf("missing default profiles: %q, %v", loaded, err)
	}
	beside := filepath.Join(dir, "profiles.json")
	must(t, os.WriteFile(beside, []byte(`{"profiles":[{"name":"local"}]}`), 0600))
	if loaded, err := s.Configure(options); err != nil || loaded != beside {
		t.Fatalf("default profiles: %q, %v", loaded, err)
	}
	if _, ok := profileNamed(s.ListProfiles(), "local"); !ok {
		t.Fatal("profile from profiles.json missing")
	}
	must(t, os.WriteFile(beside, []byte("{"), 0600))
	if _, err := s.Configure(options); err == nil {
		t.Fatal("invalid profiles file ignored")
	}
	options.ProfilesPath = filepath.Join(dir, "missing.json")
	if _, err := s.Configure(options); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing explicit path: %v", err)
	}
	options.ProfilesPath = filepath.Join("..", "..", "profiles.json")
	if loaded, err := s.Configure(options); err != nil || loaded != options.ProfilesPath {
		t.Fatalf("shipped profiles: %q, %v", loaded, err)
	}
	profile, ok := profileNamed(s.ListProfiles(), CoordinatorProfileName)
	if !ok || profile.Model != "" || profile.CoordinatorInstructions != "" {
		t.Fatalf("shipped coordinator profile: %+v", profile)
	}
}
