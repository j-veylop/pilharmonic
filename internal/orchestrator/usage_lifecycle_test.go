package orchestrator

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
)

// usageRecorderFor reports the sink's usage capability as an error instead of
// panicking inside a runner goroutine, where t.Fatal cannot be used.
func usageRecorderFor(out io.Writer) (UsageRecorder, error) {
	recorder, ok := out.(UsageRecorder)
	if !ok {
		return nil, errors.New("runner sink cannot record usage")
	}
	return recorder, nil
}

// TestRetriedTaskKeepsEachAttemptUsage proves a retry starts a new attempt
// instead of overwriting the previous one: each attempt keeps its own record,
// the task sums both, and the session counts both. Output retention drops only
// the older transcript, never the usage that is part of the task's history.
func TestRetriedTaskKeepsEachAttemptUsage(t *testing.T) {
	s := newTestService(t)
	must(t, s.SetAttemptRetention(1))
	attempts := 0
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, _ Task, out io.Writer) error {
		attempts++
		recorder, err := usageRecorderFor(out)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(out, "attempt output"); err != nil {
			return err
		}
		if attempts == 1 {
			if err := recorder.RecordUsage("openai/gpt-4.1", TokenUsage{InputTokens: 1000, OutputTokens: 100}); err != nil {
				return err
			}
			return errors.New("first attempt failed")
		}
		return recorder.RecordUsage("openai/gpt-4.1", TokenUsage{InputTokens: 2000, OutputTokens: 300})
	}))
	task := createTask(t, s, "retry", "do work", t.TempDir())
	must(t, s.Start(context.Background(), task.ID))
	await(t, s, task.ID, Failed)
	must(t, s.Retry(task.ID))
	must(t, s.Start(context.Background(), task.ID))
	done := await(t, s, task.ID, Completed)
	if len(done.Attempts) != 2 {
		t.Fatalf("attempts = %+v, want two", done.Attempts)
	}
	wantAttempts := []struct {
		status       Status
		input, outpt int64
	}{
		{Failed, 1000, 100},
		{Completed, 2000, 300},
	}
	for i, want := range wantAttempts {
		attempt := done.Attempts[i]
		if attempt.Status != want.status {
			t.Errorf("attempt %d status = %s, want %s", i, attempt.Status, want.status)
		}
		if attempt.Usage == nil {
			t.Fatalf("attempt %d lost its usage record: %+v", i, attempt)
		}
		if attempt.Usage.InputTokens != want.input || attempt.Usage.OutputTokens != want.outpt {
			t.Errorf("attempt %d usage = %d/%d, want %d/%d", i,
				attempt.Usage.InputTokens, attempt.Usage.OutputTokens, want.input, want.outpt)
		}
		if attempt.Usage.Model != "openai/gpt-4.1" || !attempt.Usage.Priced {
			t.Errorf("attempt %d usage model/price = %+v", i, attempt.Usage)
		}
	}
	// Retention kept only the newest transcript, and left both usage records.
	if done.Attempts[0].Output != "" || !done.Attempts[0].OutputTruncated {
		t.Fatalf("retention did not drop the older output: %+v", done.Attempts[0])
	}
	if done.Attempts[1].Output != "attempt output" {
		t.Fatalf("retention dropped the newest output: %+v", done.Attempts[1])
	}
	wantCost := 3000*2.0/millionTokens + 400*8.0/millionTokens
	total := done.Usage()
	if total.InputTokens != 3000 || total.OutputTokens != 400 || !floatMatches(total.CostUSD, wantCost) || !total.Priced {
		t.Fatalf("task usage = %+v, want 3000/400 at %v", total, wantCost)
	}
	session := s.SessionUsage()
	if session.InputTokens != 3000 || session.OutputTokens != 400 || session.Tasks != 1 || !floatMatches(session.CostUSD, wantCost) {
		t.Fatalf("session usage = %+v, want both attempts counted", session)
	}
	if len(session.Models) != 1 || session.Models[0].InputTokens != 3000 {
		t.Fatalf("session model breakdown = %+v", session.Models)
	}
}

// TestRetainAttemptOutputsKeepsUsage checks the retention helper directly: it
// frees the copied transcript of old attempts and must not touch the identity,
// outcome or usage that the task's own total depends on.
func TestRetainAttemptOutputsKeepsUsage(t *testing.T) {
	task := Task{Attempts: []Attempt{
		{
			ID:     "first",
			Output: "first output",
			Error:  "first failed",
			Usage:  &AttemptUsage{Model: "openai/gpt-4.1", InputTokens: 10, OutputTokens: 1, CostUSD: 0.5, Priced: true},
		},
		{
			ID:     "second",
			Output: "second output",
			Usage:  &AttemptUsage{Model: "openai/gpt-4.1", InputTokens: 20, OutputTokens: 2, CostUSD: 0.25, Priced: true},
		},
	}}
	if dropped := retainAttemptOutputs(&task, 1); dropped != 1 {
		t.Fatalf("dropped %d outputs, want 1", dropped)
	}
	first := task.Attempts[0]
	if first.Output != "" || !first.OutputTruncated {
		t.Fatalf("older output not dropped: %+v", first)
	}
	if first.ID != "first" || first.Error != "first failed" {
		t.Fatalf("retention changed attempt identity or outcome: %+v", first)
	}
	if first.Usage == nil || first.Usage.InputTokens != 10 || first.Usage.OutputTokens != 1 || first.Usage.CostUSD != 0.5 {
		t.Fatalf("retention dropped the older usage: %+v", first.Usage)
	}
	if task.Attempts[1].Output != "second output" {
		t.Fatalf("retention dropped the newest output: %+v", task.Attempts[1])
	}
	if dropped := retainAttemptOutputs(&task, 1); dropped != 0 {
		t.Fatalf("second pass dropped %d outputs, want 0", dropped)
	}
	total := task.Usage()
	if total.InputTokens != 30 || total.OutputTokens != 3 || total.CostUSD != 0.75 || !total.Priced {
		t.Fatalf("task usage after retention = %+v", total)
	}
}

// TestRecordUsageThroughFailedAttemptKeepsUsage proves usage a provider
// reported is kept even when the run fails afterwards: the tokens were spent,
// so they stay on the attempt, in the task total and in the session total.
func TestRecordUsageThroughFailedAttemptKeepsUsage(t *testing.T) {
	s := newTestService(t)
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, _ Task, out io.Writer) error {
		recorder, err := usageRecorderFor(out)
		if err != nil {
			return err
		}
		if err := recorder.RecordUsage("openai/gpt-4.1", TokenUsage{InputTokens: 1000, OutputTokens: 500}); err != nil {
			return err
		}
		return errors.New("provider exploded")
	}))
	task := createTask(t, s, "fails", "do work", t.TempDir())
	must(t, s.Start(context.Background(), task.ID))
	done := await(t, s, task.ID, Failed)
	if done.Error != "provider exploded" {
		t.Fatalf("task error = %q", done.Error)
	}
	if len(done.Attempts) != 1 || done.Attempts[0].Status != Failed {
		t.Fatalf("attempts = %+v", done.Attempts)
	}
	usage := done.Attempts[0].Usage
	if usage == nil || usage.InputTokens != 1000 || usage.OutputTokens != 500 || !usage.Priced {
		t.Fatalf("failed attempt lost its usage: %+v", usage)
	}
	wantCost := 1000*2.0/millionTokens + 500*8.0/millionTokens
	if !floatMatches(usage.CostUSD, wantCost) {
		t.Fatalf("attempt cost = %v, want %v", usage.CostUSD, wantCost)
	}
	if total := done.Usage(); total.InputTokens != 1000 || total.OutputTokens != 500 || !floatMatches(total.CostUSD, wantCost) {
		t.Fatalf("task usage = %+v", total)
	}
	session := s.SessionUsage()
	if session.InputTokens != 1000 || session.OutputTokens != 500 || !floatMatches(session.CostUSD, wantCost) {
		t.Fatalf("session usage = %+v", session)
	}
	if !session.Priced || session.Tasks != 1 {
		t.Fatalf("session pricing/tasks = %+v", session)
	}
}

// TestRecordUsageThroughCanceledAttemptKeepsUsage proves a canceled run keeps
// the tokens it already reported, so cancellation cannot hide spend.
func TestRecordUsageThroughCanceledAttemptKeepsUsage(t *testing.T) {
	s := newTestService(t)
	recorded := make(chan struct{})
	s.RegisterRunner("general", runnerFunc(func(ctx context.Context, _ Task, out io.Writer) error {
		recorder, err := usageRecorderFor(out)
		if err != nil {
			return err
		}
		if err := recorder.RecordUsage("openai/gpt-4.1", TokenUsage{InputTokens: 1000, OutputTokens: 500}); err != nil {
			return err
		}
		close(recorded)
		<-ctx.Done()
		return ctx.Err()
	}))
	task := createTask(t, s, "canceled", "do work", t.TempDir())
	must(t, s.Start(context.Background(), task.ID))
	<-recorded
	must(t, s.Cancel(task.ID))
	done := await(t, s, task.ID, Canceled)
	if len(done.Attempts) != 1 || done.Attempts[0].Status != Canceled {
		t.Fatalf("attempts = %+v", done.Attempts)
	}
	usage := done.Attempts[0].Usage
	if usage == nil || usage.InputTokens != 1000 || usage.OutputTokens != 500 {
		t.Fatalf("canceled attempt lost its usage: %+v", usage)
	}
	wantCost := 1000*2.0/millionTokens + 500*8.0/millionTokens
	if !floatMatches(usage.CostUSD, wantCost) {
		t.Fatalf("attempt cost = %v, want %v", usage.CostUSD, wantCost)
	}
	session := s.SessionUsage()
	if session.InputTokens != 1000 || session.OutputTokens != 500 || !floatMatches(session.CostUSD, wantCost) {
		t.Fatalf("session usage = %+v", session)
	}
}

// TestRecordUsageAfterAttemptFinishedIsRefused keeps a late provider callback
// from charging usage to an attempt that already ended: a report through the
// sink of a finished attempt is refused and changes nothing.
func TestRecordUsageAfterAttemptFinishedIsRefused(t *testing.T) {
	s := newTestService(t)
	captured := make(chan io.Writer, 1)
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, _ Task, out io.Writer) error {
		recorder, err := usageRecorderFor(out)
		if err != nil {
			return err
		}
		if err := recorder.RecordUsage("openai/gpt-4.1", TokenUsage{InputTokens: 1000, OutputTokens: 100}); err != nil {
			return err
		}
		captured <- out
		return nil
	}))
	task := createTask(t, s, "late", "do work", t.TempDir())
	must(t, s.Start(context.Background(), task.ID))
	late := <-captured
	done := await(t, s, task.ID, Completed)
	wantCost := 1000*2.0/millionTokens + 100*8.0/millionTokens
	before := done.Attempts[0].Usage
	if before == nil || !floatMatches(before.CostUSD, wantCost) {
		t.Fatalf("attempt usage before the late report = %+v", before)
	}

	recorder, ok := late.(UsageRecorder)
	if !ok {
		t.Fatal("runner sink cannot record usage")
	}
	if err := recorder.RecordUsage("openai/gpt-4.1", TokenUsage{InputTokens: 5000, OutputTokens: 5000}); err == nil {
		t.Fatal("late usage report was accepted for a finished attempt")
	}
	after, _ := s.GetTask(task.ID)
	usage := after.Attempts[0].Usage
	if usage == nil || usage.InputTokens != 1000 || usage.OutputTokens != 100 || !floatMatches(usage.CostUSD, wantCost) {
		t.Fatalf("late report changed the attempt: %+v", usage)
	}
	session := s.SessionUsage()
	if session.InputTokens != 1000 || session.OutputTokens != 100 || !floatMatches(session.CostUSD, wantCost) {
		t.Fatalf("late report changed the session total: %+v", session)
	}
}

// TestRecordUsageFromStaleAttemptIsRefused reproduces an adapter that kept the
// sink of a failed attempt and reports after the retry started: the report is
// refused and the new attempt keeps only its own tokens. This is the retry case
// the attempt binding in taskLocked exists for.
func TestRecordUsageFromStaleAttemptIsRefused(t *testing.T) {
	s := newTestService(t)
	stale := make(chan io.Writer, 1)
	secondAttempt := make(chan struct{})
	releaseSecond := make(chan struct{})
	// A failed assertion must not leave the runner blocked, or Shutdown would
	// wait for it forever.
	released := false
	release := func() {
		if !released {
			released = true
			close(releaseSecond)
		}
	}
	defer release()
	attempts := 0
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, _ Task, out io.Writer) error {
		attempts++
		recorder, err := usageRecorderFor(out)
		if err != nil {
			return err
		}
		if attempts == 1 {
			if err := recorder.RecordUsage("openai/gpt-4.1", TokenUsage{InputTokens: 1000, OutputTokens: 100}); err != nil {
				return err
			}
			stale <- out
			return errors.New("first attempt failed")
		}
		if err := recorder.RecordUsage("openai/gpt-4.1", TokenUsage{InputTokens: 2000, OutputTokens: 200}); err != nil {
			return err
		}
		close(secondAttempt)
		<-releaseSecond
		return nil
	}))
	task := createTask(t, s, "stale", "do work", t.TempDir())
	must(t, s.Start(context.Background(), task.ID))
	staleWriter := <-stale
	await(t, s, task.ID, Failed)
	must(t, s.Retry(task.ID))
	must(t, s.Start(context.Background(), task.ID))
	<-secondAttempt
	recorder, ok := staleWriter.(UsageRecorder)
	if !ok {
		t.Fatal("runner sink cannot record usage")
	}
	if err := recorder.RecordUsage("openai/gpt-4.1", TokenUsage{InputTokens: 9999, OutputTokens: 9999}); err == nil {
		t.Fatal("a stale attempt's sink charged the running attempt")
	}
	release()
	done := await(t, s, task.ID, Completed)
	if len(done.Attempts) != 2 {
		t.Fatalf("attempts = %+v, want two", done.Attempts)
	}
	newest := done.Attempts[1].Usage
	if newest == nil || newest.InputTokens != 2000 || newest.OutputTokens != 200 {
		t.Fatalf("running attempt was charged by the stale sink: %+v", newest)
	}
	wantCost := 3000*2.0/millionTokens + 300*8.0/millionTokens
	session := s.SessionUsage()
	if session.InputTokens != 3000 || session.OutputTokens != 300 || !floatMatches(session.CostUSD, wantCost) {
		t.Fatalf("session usage = %+v, want only the two attempts' own tokens", session)
	}
}

// TestRecordUsageAfterShutdownIsRefused proves a provider callback arriving
// while the service shuts down cannot write usage to durable state.
func TestRecordUsageAfterShutdownIsRefused(t *testing.T) {
	s := mustGet(Open(filepath.Join(t.TempDir(), "state.json")))(t)
	captured := make(chan io.Writer, 1)
	recorded := make(chan struct{})
	s.RegisterRunner("general", runnerFunc(func(ctx context.Context, _ Task, out io.Writer) error {
		recorder, err := usageRecorderFor(out)
		if err != nil {
			return err
		}
		if err := recorder.RecordUsage("openai/gpt-4.1", TokenUsage{InputTokens: 1000, OutputTokens: 100}); err != nil {
			return err
		}
		captured <- out
		close(recorded)
		<-ctx.Done()
		return ctx.Err()
	}))
	task := createTask(t, s, "shutdown", "do work", t.TempDir())
	must(t, s.Start(context.Background(), task.ID))
	late := <-captured
	<-recorded
	must(t, s.Shutdown())
	recorder, ok := late.(UsageRecorder)
	if !ok {
		t.Fatal("runner sink cannot record usage")
	}
	if err := recorder.RecordUsage("openai/gpt-4.1", TokenUsage{InputTokens: 1}); err == nil {
		t.Fatal("usage recorded after shutdown")
	}
	got, _ := s.GetTask(task.ID)
	if usage := got.Attempts[0].Usage; usage == nil || usage.InputTokens != 1000 {
		t.Fatalf("usage after shutdown = %+v", usage)
	}
}
