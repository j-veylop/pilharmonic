package orchestrator

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeUsageState writes durable state directly, so a test can pose a task whose
// attempts recorded usage before this process opened the file without keeping a
// previous process alive.
func writeUsageState(t *testing.T, path string, tasks ...Task) {
	t.Helper()
	b := mustGet(json.Marshal(state{Version: stateVersion, Tasks: tasks}))(t)
	must(t, os.WriteFile(path, b, 0600))
}

// usageAttempt builds a completed attempt that recorded usage at when.
func usageAttempt(id string, when time.Time, usage AttemptUsage) Attempt {
	return Attempt{
		ID:             id,
		Agent:          "general",
		Status:         Completed,
		StartedAt:      when,
		FinishedAt:     when.Add(time.Minute),
		RequestedModel: usage.Model,
		Usage:          &usage,
	}
}

// TestSessionUsageExcludesEarlierAttemptsButTaskKeepsThem covers the session
// boundary with two tasks where only one has usage older than the session, and
// with one task that ran in both. Task.Usage() is the task's own history and
// keeps everything; SessionUsage counts only attempts this process started.
func TestSessionUsageExcludesEarlierAttemptsButTaskKeepsThem(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	earlierOnly := Task{
		ID: "old-task", ThreadID: "old-thread", Title: "old", Prompt: "old mandate",
		Workspace: dir, Status: Completed, Agent: "general",
		CreatedAt: old, UpdatedAt: old.Add(time.Minute),
		Attempts: []Attempt{usageAttempt("old-attempt", old, AttemptUsage{
			Model: "openai/gpt-4.1", InputTokens: 1000, OutputTokens: 2000, CostUSD: 0.018, Priced: true,
		})},
	}
	// 100*2/1M + 200*8/1M = 0.0018.
	ranEarlier := Task{
		ID: "retried-task", ThreadID: "retried-thread", Title: "retried", Prompt: "retried mandate",
		Workspace: dir, Status: Failed, Agent: "general",
		CreatedAt: old, UpdatedAt: old.Add(time.Minute),
		Attempts: []Attempt{usageAttempt("old-attempt-2", old, AttemptUsage{
			Model: "openai/gpt-4.1", InputTokens: 100, OutputTokens: 200, CostUSD: 0.0018, Priced: true,
		})},
	}
	writeUsageState(t, path, earlierOnly, ranEarlier)

	s := openTestService(t, path)
	// Nothing was recorded by this process yet, so the session is empty even
	// though durable history holds usage.
	if usage := s.SessionUsage(); usage.InputTokens != 0 || usage.OutputTokens != 0 || usage.Tasks != 0 || len(usage.Models) != 0 {
		t.Fatalf("session inherited earlier usage: %+v", usage)
	}

	s.RegisterRunner("general", sessionUsageRunner("openai/gpt-4.1", TokenUsage{InputTokens: 500, OutputTokens: 100}))
	must(t, s.Retry("retried-task"))
	must(t, s.Start(context.Background(), "retried-task"))
	done := await(t, s, "retried-task", Completed)

	// The task keeps the attempt from before the restart alongside the new one.
	total := done.Usage()
	wantTaskCost := 0.0018 + 500*2.0/millionTokens + 100*8.0/millionTokens
	if total.InputTokens != 600 || total.OutputTokens != 300 || !total.Priced || !floatMatches(total.CostUSD, wantTaskCost) {
		t.Fatalf("task usage = %+v, want 600/300 at %v", total, wantTaskCost)
	}

	// The session counts only the attempt this process started.
	usage := s.SessionUsage()
	wantSessionCost := 500*2.0/millionTokens + 100*8.0/millionTokens
	if usage.InputTokens != 500 || usage.OutputTokens != 100 || usage.TotalTokens != 600 {
		t.Fatalf("session tokens = %+v, want the new attempt only", usage)
	}
	if !floatMatches(usage.CostUSD, wantSessionCost) {
		t.Fatalf("session cost = %v, want %v", usage.CostUSD, wantSessionCost)
	}
	if usage.Tasks != 1 {
		t.Fatalf("session tasks = %d, want 1 (the task that only ran earlier contributes nothing)", usage.Tasks)
	}
	if !usage.Priced || len(usage.Unpriced) != 0 {
		t.Fatalf("session pricing = %+v", usage)
	}
	if len(usage.Models) != 1 || usage.Models[0].Model != "openai/gpt-4.1" || usage.Models[0].InputTokens != 500 {
		t.Fatalf("session model breakdown = %+v", usage.Models)
	}

	// The task that only ran in the earlier session keeps its own record.
	previous, ok := s.GetTask("old-task")
	if !ok {
		t.Fatal("old task missing")
	}
	got := previous.Usage()
	if got.InputTokens != 1000 || got.OutputTokens != 2000 || !floatMatches(got.CostUSD, 0.018) || !got.Priced {
		t.Fatalf("old task usage = %+v, want its own history", got)
	}
}

// TestSessionUsageMixesPricedAndUnpricedModels proves a session never claims a
// complete cost when part of its work is unpriced: it sums the priced cost,
// counts every token, names the unpriced selector and still lists both models
// separately in the breakdown.
func TestSessionUsageMixesPricedAndUnpricedModels(t *testing.T) {
	s := newTestService(t)
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, task Task, out io.Writer) error {
		recorder, err := usageRecorderFor(out)
		if err != nil {
			return err
		}
		if task.Title == "priced" {
			return recorder.RecordUsage("openai/gpt-4.1", TokenUsage{InputTokens: 1000, OutputTokens: 500})
		}
		return recorder.RecordUsage("nosuch/model", TokenUsage{InputTokens: 7, OutputTokens: 3})
	}))
	for _, title := range []string{"priced", "unpriced"} {
		task, err := s.CreateTask(title, "do work", t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		must(t, s.Start(context.Background(), task.ID))
		await(t, s, task.ID, Completed)
	}

	usage := s.SessionUsage()
	if usage.InputTokens != 1007 || usage.OutputTokens != 503 || usage.TotalTokens != 1510 {
		t.Fatalf("session tokens = %+v, want every token counted", usage)
	}
	if usage.Priced {
		t.Fatalf("session claimed a complete cost with an unpriced model: %+v", usage)
	}
	if len(usage.Unpriced) != 1 || usage.Unpriced[0] != "nosuch/model" {
		t.Fatalf("unpriced selectors = %v, want [nosuch/model]", usage.Unpriced)
	}
	// Only the priced work contributes money: 1000*2/1M + 500*8/1M = 0.006.
	wantCost := 1000*2.0/millionTokens + 500*8.0/millionTokens
	if !floatMatches(usage.CostUSD, wantCost) {
		t.Fatalf("session cost = %v, want the priced cost %v", usage.CostUSD, wantCost)
	}
	if usage.Tasks != 2 || len(usage.Models) != 2 {
		t.Fatalf("session tasks/models = %d/%d, want 2/2", usage.Tasks, len(usage.Models))
	}
	byModel := map[string]ModelUsage{}
	for _, entry := range usage.Models {
		byModel[entry.Model] = entry
	}
	priced, ok := byModel["openai/gpt-4.1"]
	if !ok {
		t.Fatalf("priced model missing from the breakdown: %+v", usage.Models)
	}
	if priced.InputTokens != 1000 || priced.OutputTokens != 500 || !floatMatches(priced.CostUSD, wantCost) {
		t.Fatalf("priced model entry = %+v", priced)
	}
	if !priced.Priced || !priced.PriceKnown || priced.CurrentRate != "" {
		t.Fatalf("priced model flags = %+v", priced)
	}
	unpriced, ok := byModel["nosuch/model"]
	if !ok {
		t.Fatalf("unpriced model missing from the breakdown: %+v", usage.Models)
	}
	if unpriced.InputTokens != 7 || unpriced.OutputTokens != 3 || unpriced.CostUSD != 0 {
		t.Fatalf("unpriced model entry = %+v", unpriced)
	}
	if unpriced.Priced || unpriced.PriceKnown || unpriced.CurrentRate != "" {
		t.Fatalf("unpriced model flags = %+v", unpriced)
	}
	// The priced model is listed first because the breakdown orders by cost.
	if usage.Models[0].Model != "openai/gpt-4.1" {
		t.Fatalf("model order = %+v, want the priced model first", usage.Models)
	}
	// The cost is the sum of the per-model costs, not of tokens at one price.
	if !floatMatches(usage.CostUSD, priced.CostUSD+unpriced.CostUSD) {
		t.Fatalf("session cost %v is not the per-model sum %v", usage.CostUSD, priced.CostUSD+unpriced.CostUSD)
	}
}

// TestRecordUsageIgnoresZeroTokenCount proves a provider that reports no tokens
// creates no record at all, so an attempt that reported nothing is not shown as
// having cost zero.
func TestRecordUsageIgnoresZeroTokenCount(t *testing.T) {
	var err error
	s := newTestService(t)
	reported := make(chan error, 1)
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, _ Task, out io.Writer) error {
		recorder, err := usageRecorderFor(out)
		if err != nil {
			return err
		}
		reported <- recorder.RecordUsage("openai/gpt-4.1", TokenUsage{})
		return nil
	}))
	task := createTask(t, s, "no tokens", "do work", t.TempDir())
	must(t, s.Start(context.Background(), task.ID))
	if err = <-reported; err != nil {
		t.Fatalf("zero usage report failed: %v", err)
	}
	done := await(t, s, task.ID, Completed)
	if len(done.Attempts) != 1 {
		t.Fatalf("attempts = %+v", done.Attempts)
	}
	if usage := done.Attempts[0].Usage; usage != nil {
		t.Fatalf("zero usage created an attempt record: %+v", usage)
	}
	if total := done.Usage(); !total.IsZero() || total.CostUSD != 0 || total.Priced {
		t.Fatalf("task usage = %+v, want no usage", total)
	}
	usage := s.SessionUsage()
	if usage.InputTokens != 0 || usage.OutputTokens != 0 || usage.TotalTokens != 0 || usage.CostUSD != 0 {
		t.Fatalf("session tokens = %+v, want none", usage)
	}
	if usage.Tasks != 0 || len(usage.Models) != 0 || len(usage.Unpriced) != 0 {
		t.Fatalf("session breakdown = %+v, want none", usage)
	}
}

// TestRecordUsagePricesUnpricedModelWithoutCost checks an unknown selector still
// records the tokens the provider reported, marked unpriced, so the operator can
// see the work even though no price is known for it.
func TestRecordUsagePricesUnpricedModelWithoutCost(t *testing.T) {
	s := newTestService(t)
	s.RegisterRunner("general", sessionUsageRunner("nosuch/model", TokenUsage{InputTokens: 40, OutputTokens: 2}))
	task := createTask(t, s, "unknown", "do work", t.TempDir())
	must(t, s.Start(context.Background(), task.ID))
	done := await(t, s, task.ID, Completed)
	usage := done.Attempts[0].Usage
	if usage == nil || usage.InputTokens != 40 || usage.OutputTokens != 2 {
		t.Fatalf("unpriced attempt usage = %+v", usage)
	}
	if usage.Priced || usage.CostUSD != 0 || usage.Rate != "" {
		t.Fatalf("unpriced attempt claimed a cost: %+v", usage)
	}
	if usage.Model != "nosuch/model" {
		t.Fatalf("unpriced attempt recorded model %q", usage.Model)
	}
	if total := done.Usage(); total.Priced || total.InputTokens != 40 {
		t.Fatalf("task usage = %+v", total)
	}
}

// TestRecordUsageFallsBackToAttemptModel checks a report without a selector is
// attributed to the attempt's own requested model rather than left unlabelled,
// so the price lookup and the breakdown use the model the attempt ran.
func TestRecordUsageFallsBackToAttemptModel(t *testing.T) {
	s := newTestService(t)
	// The fake runner's requested model stands in for the selector an adapter
	// would report through Runner's optional RequestedModel capability.
	s.RegisterRunner("general", modelRunner{model: "openai/gpt-4.1"})
	task := createTask(t, s, "fallback", "do work", t.TempDir())
	must(t, s.Start(context.Background(), task.ID))
	done := await(t, s, task.ID, Completed)
	usage := done.Attempts[0].Usage
	if usage == nil || usage.Model != "openai/gpt-4.1" {
		t.Fatalf("attempt usage did not fall back to the attempt model: %+v", usage)
	}
	if !usage.Priced || usage.InputTokens != 100 {
		t.Fatalf("attempt usage = %+v", usage)
	}
	if total := done.Usage(); total.Model != "openai/gpt-4.1" || !floatMatches(total.CostUSD, 100*2.0/millionTokens) {
		t.Fatalf("task usage = %+v", total)
	}
	models := s.SessionUsage().Models
	if len(models) != 1 || models[0].Model != "openai/gpt-4.1" {
		t.Fatalf("session model breakdown = %+v", models)
	}
}

// modelRunner is a fake runner that names the model it ran and reports usage
// without passing a selector, the way an adapter that relies on the attempt's
// requested model would.
type modelRunner struct{ model string }

func (r modelRunner) RequestedModel(*Profile) string { return r.model }

func (r modelRunner) Run(_ context.Context, _ Task, out io.Writer) error {
	recorder, err := usageRecorderFor(out)
	if err != nil {
		return err
	}
	return recorder.RecordUsage("", TokenUsage{InputTokens: 100, OutputTokens: 0})
}

// TestRecordUsageWithoutCapabilityIsNotCharged covers sinks that cannot record:
// a plain writer and a discarded writer must not fail the run, and a runner that
// reports nothing must not leave an attempt that looks like it cost zero.
func TestRecordUsageWithoutCapabilityIsNotCharged(t *testing.T) {
	if err := adapterUsage(io.Discard, "", TokenUsage{InputTokens: 1}); err != nil {
		t.Fatalf("discarded sink failed: %v", err)
	}
	if err := adapterUsage(io.Discard, "openai/gpt-4.1", TokenUsage{}); err != nil {
		t.Fatalf("zero usage through a discarded sink failed: %v", err)
	}
	s := newTestService(t)
	// A runner that never reports usage must leave the attempt record absent
	// rather than imply the attempt cost nothing.
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, _ Task, out io.Writer) error {
		_, err := io.WriteString(out, "no usage reported")
		return err
	}))
	task := createTask(t, s, "plain", "do work", t.TempDir())
	must(t, s.Start(context.Background(), task.ID))
	done := await(t, s, task.ID, Completed)
	if usage := done.Attempts[0].Usage; usage != nil {
		t.Fatalf("unreported usage was invented: %+v", usage)
	}
	if usage := s.SessionUsage(); usage.InputTokens != 0 || usage.Tasks != 0 {
		t.Fatalf("session tokens = %+v, want none", usage)
	}
}
