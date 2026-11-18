package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"charm.land/fantasy"
)

// usageSink records the token usage an adapter reports, so a test can assert the
// adapter charged the right attempt without a live provider.
type usageSink struct {
	bytes.Buffer
	calls []usageCall
}

type usageCall struct {
	model string
	usage TokenUsage
}

func (s *usageSink) RecordUsage(model string, usage TokenUsage) error {
	s.calls = append(s.calls, usageCall{model: model, usage: usage})
	return nil
}

// sessionUsageRunner records a fixed token count through the attempt's sink.
func sessionUsageRunner(model string, usage TokenUsage) Runner {
	return runnerFunc(func(_ context.Context, task Task, out io.Writer) error {
		if _, err := io.WriteString(out, "done"); err != nil {
			return err
		}
		recorder, ok := out.(UsageRecorder)
		if !ok {
			return nil
		}
		return recorder.RecordUsage(model, usage)
	})
}

func TestSessionUsageIncludesSubagents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := openTestService(t, path)
	started := make(chan Task, 2)
	release := make(chan struct{})
	s.RegisterRunner("general", runnerFunc(func(ctx context.Context, task Task, out io.Writer) error {
		started <- task
		if task.ParentID == "" {
			<-release
		}
		recorder := out.(UsageRecorder)
		usage := TokenUsage{InputTokens: 1000, OutputTokens: 2000}
		if task.ParentID != "" {
			usage = TokenUsage{InputTokens: 3000, OutputTokens: 4000}
		}
		return recorder.RecordUsage("openai/gpt-4.1", usage)
	}))
	parent := createTask(t, s, "parent", "parent mandate", t.TempDir())
	must(t, s.Start(context.Background(), parent.ID))
	<-started
	child := mustGet(s.SpawnSubagent(parent.ID, "child", "child mandate", ""))(t)
	await(t, s, child.ID, Completed)
	close(release)
	await(t, s, parent.ID, Completed)

	usage := s.SessionUsage()
	if usage.InputTokens != 4000 || usage.OutputTokens != 6000 || usage.TotalTokens != 10000 {
		t.Fatalf("session tokens = %+v", usage)
	}
	if usage.SubagentInputTokens != 3000 || usage.SubagentOutputTokens != 4000 {
		t.Fatalf("subagent tokens not separated: %+v", usage)
	}
	if !usage.Priced || len(usage.Unpriced) != 0 {
		t.Fatalf("session should be fully priced: %+v", usage)
	}
	wantCost := 4000*2.0/millionTokens + 6000*8.0/millionTokens
	if math.Abs(usage.CostUSD-wantCost) > 1e-9 {
		t.Fatalf("session cost = %v, want %v", usage.CostUSD, wantCost)
	}
	wantSubagent := 3000*2.0/millionTokens + 4000*8.0/millionTokens
	if math.Abs(usage.SubagentCostUSD-wantSubagent) > 1e-9 {
		t.Fatalf("subagent cost = %v, want %v", usage.SubagentCostUSD, wantSubagent)
	}
	if usage.Tasks != 2 || usage.SubagentTasks != 1 {
		t.Fatalf("session task counts = %d/%d", usage.Tasks, usage.SubagentTasks)
	}
	if len(usage.Models) != 1 || usage.Models[0].Model != "openai/gpt-4.1" || usage.Models[0].InputTokens != 4000 {
		t.Fatalf("model breakdown = %+v", usage.Models)
	}
	if !usage.Models[0].PriceKnown || usage.Models[0].CurrentRate != "" {
		t.Fatalf("model price = %+v", usage.Models[0])
	}

	// The attempt keeps its own record, and the task totals agree with it.
	gotParent, _ := s.GetTask(parent.ID)
	if len(gotParent.Attempts) != 1 || gotParent.Attempts[0].Usage == nil {
		t.Fatalf("parent attempt usage missing: %+v", gotParent.Attempts)
	}
	if total := gotParent.Usage(); total.InputTokens != 1000 || total.OutputTokens != 2000 || !total.Priced {
		t.Fatalf("task usage = %+v", total)
	}
}

func TestSessionUsageResetsOnRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := mustGet(Open(path))(t)
	s.RegisterRunner("general", sessionUsageRunner("openai/gpt-4.1", TokenUsage{InputTokens: 1000, OutputTokens: 1000}))
	task := createTask(t, s, "t", "do work", t.TempDir())
	must(t, s.Start(context.Background(), task.ID))
	await(t, s, task.ID, Completed)
	if usage := s.SessionUsage(); usage.InputTokens != 1000 {
		t.Fatalf("usage not recorded: %+v", usage)
	}
	must(t, s.Shutdown())

	again := mustGet(Open(path))(t)
	defer func() { _ = again.Shutdown() }()
	// Durable history keeps the recorded usage on the attempt ...
	reopened, _ := again.GetTask(task.ID)
	if total := reopened.Usage(); total.InputTokens != 1000 {
		t.Fatalf("attempt usage lost on restart: %+v", total)
	}
	// ... while the new session starts at zero.
	if usage := again.SessionUsage(); usage.InputTokens != 0 || usage.CostUSD != 0 || usage.Tasks != 0 {
		t.Fatalf("reopened session inherited usage: %+v", usage)
	}
}

func TestFantasyRunnerRecordsUsagePerModelCall(t *testing.T) {
	finish := func(reason fantasy.FinishReason, input, output, cacheRead, cacheWrite int64) fantasy.StreamPart {
		return fantasy.StreamPart{
			Type:         fantasy.StreamPartTypeFinish,
			FinishReason: reason,
			Usage: fantasy.Usage{
				InputTokens: input, OutputTokens: output,
				CacheReadTokens: cacheRead, CacheCreationTokens: cacheWrite,
				TotalTokens: input + output + cacheRead + cacheWrite,
			},
		}
	}
	step := func(text string, input, output, cacheRead, cacheWrite int64) []fantasy.StreamPart {
		return []fantasy.StreamPart{
			{Type: fantasy.StreamPartTypeTextStart, ID: "text-1"},
			{Type: fantasy.StreamPartTypeTextDelta, ID: "text-1", Delta: text},
			{Type: fantasy.StreamPartTypeTextEnd, ID: "text-1"},
			finish(fantasy.FinishReasonStop, input, output, cacheRead, cacheWrite),
		}
	}
	model := &fakeFantasyModel{scripts: [][]fantasy.StreamPart{
		step("first", 100, 40, 30, 10),
		step("second", 50, 20, 5, 0),
	}}
	runner := FantasyRunner{}
	runner.ModelFactory = fakeFactory(model, "deepseek/deepseek-chat")

	sink := &usageSink{}
	follow := make(chan Trigger, 1)
	follow <- Trigger{ID: "ctx-1", Text: "more"}
	close(follow)
	if err := runner.RunLive(context.Background(), Task{Prompt: "do work", model: "deepseek/deepseek-chat", Workspace: t.TempDir()}, sink, follow,
		func(string, bool, string) {}); err != nil {
		t.Fatal(err)
	}
	if len(sink.calls) != 2 {
		t.Fatalf("usage calls = %+v", sink.calls)
	}
	for i, want := range []TokenUsage{
		{InputTokens: 100, OutputTokens: 40, CacheReadTokens: 30, CacheWriteTokens: 10},
		{InputTokens: 50, OutputTokens: 20, CacheReadTokens: 5},
	} {
		if sink.calls[i].model != "deepseek/deepseek-chat" || sink.calls[i].usage != want {
			t.Fatalf("call %d = %+v, want %+v", i, sink.calls[i], want)
		}
	}
}

// A single Stream call runs several model/tool steps. Recording each step's
// finish makes the task usage advance while the run is still in progress.
func TestFantasyRunnerRecordsUsagePerStep(t *testing.T) {
	workspace := t.TempDir()
	must(t, os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("contents"), 0o600))
	finish := func(reason fantasy.FinishReason, input, output int64) fantasy.StreamPart {
		return fantasy.StreamPart{
			Type:         fantasy.StreamPartTypeFinish,
			FinishReason: reason,
			Usage:        fantasy.Usage{InputTokens: input, OutputTokens: output},
		}
	}
	model := &fakeFantasyModel{scripts: [][]fantasy.StreamPart{
		{
			{Type: fantasy.StreamPartTypeToolCall, ID: "call-1", ToolCallName: "read", ToolCallInput: `{"path":"note.txt"}`},
			finish(fantasy.FinishReasonToolCalls, 100, 40),
		},
		{
			{Type: fantasy.StreamPartTypeTextStart, ID: "text-1"},
			{Type: fantasy.StreamPartTypeTextDelta, ID: "text-1", Delta: "read the file"},
			{Type: fantasy.StreamPartTypeTextEnd, ID: "text-1"},
			finish(fantasy.FinishReasonStop, 50, 20),
		},
	}}
	runner := FantasyRunner{}
	runner.ModelFactory = fakeFactory(model, "fake/model")

	sink := &usageSink{}
	must(t, runner.Run(context.Background(), Task{Prompt: "do work", model: "fake/model", Workspace: workspace}, sink))
	if len(sink.calls) != 2 {
		t.Fatalf("usage calls = %+v", sink.calls)
	}
	for i, want := range []TokenUsage{
		{InputTokens: 100, OutputTokens: 40},
		{InputTokens: 50, OutputTokens: 20},
	} {
		if sink.calls[i].model != "fake/model" || sink.calls[i].usage != want {
			t.Fatalf("call %d = %+v, want %+v", i, sink.calls[i], want)
		}
	}
}

func TestFantasyRunnerSkipsEmptyUsage(t *testing.T) {
	model := &fakeFantasyModel{scripts: [][]fantasy.StreamPart{textDelta("answer")}}
	runner := FantasyRunner{}
	runner.ModelFactory = fakeFactory(model, "fake/model")
	sink := &usageSink{}
	must(t, runner.Run(context.Background(), Task{Prompt: "do work", model: "fake/model", Workspace: t.TempDir()}, sink))
	for _, call := range sink.calls {
		if !call.usage.IsZero() {
			t.Fatalf("a provider that reported no usage was charged: %+v", call)
		}
	}
}

func TestCloneCopiesAttemptUsage(t *testing.T) {
	original := Task{Attempts: []Attempt{{ID: "a", Usage: &AttemptUsage{InputTokens: 1}}}}
	copied := clone(original)
	copied.Attempts[0].Usage.InputTokens = 99
	if original.Attempts[0].Usage.InputTokens != 1 {
		t.Fatal("clone shares the attempt usage record")
	}
}

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

// TestAttemptUsageAddUsageMixesRates pins the durable rate label for an attempt
// that recorded usage under more than one price. deepseekOffPeak reads the wall
// clock, so a two-call service run cannot force a rate change; the mixing rule
// is asserted on addUsage directly instead.
func TestAttemptUsageAddUsageMixesRates(t *testing.T) {
	first := TokenUsage{InputTokens: 100, OutputTokens: 10}
	second := TokenUsage{InputTokens: 200, OutputTokens: 20}
	cases := []struct {
		name  string
		rates []string
		want  string
	}{
		{"peak then off-peak is mixed", []string{RatePeak, RateOffPeak}, RateMixed},
		{"off-peak then peak is mixed", []string{RateOffPeak, RatePeak}, RateMixed},
		{"the same peak rate twice keeps peak", []string{RatePeak, RatePeak}, RatePeak},
		{"the same off-peak rate twice keeps off-peak", []string{RateOffPeak, RateOffPeak}, RateOffPeak},
		{"an all-day model keeps the empty rate", []string{"", ""}, ""},
		{"an all-day report does not discount-label a priced one", []string{RateOffPeak, ""}, RateOffPeak},
		{"an all-day report then a peak one becomes peak", []string{"", RatePeak}, RatePeak},
		{"mixed stays mixed", []string{RatePeak, RateOffPeak, RatePeak}, RateMixed},
	}
	for _, tc := range cases {
		usage := &AttemptUsage{}
		for i, rate := range tc.rates {
			// The cost is fixed by the caller at recording time, so folding in a
			// count must never rewrite it.
			usage.CostUSD = 0.5
			tokens := first
			if i%2 == 1 {
				tokens = second
			}
			usage.addUsage(tokens, rate)
			if usage.CostUSD != 0.5 {
				t.Fatalf("%s: addUsage changed the recorded cost to %v", tc.name, usage.CostUSD)
			}
		}
		if usage.Rate != tc.want {
			t.Errorf("%s: rate = %q, want %q", tc.name, usage.Rate, tc.want)
		}
		// Both halves accumulate regardless of the rate label.
		wantInput, wantOutput := int64(0), int64(0)
		for i := range tc.rates {
			tokens := first
			if i%2 == 1 {
				tokens = second
			}
			wantInput += tokens.InputTokens
			wantOutput += tokens.OutputTokens
		}
		if usage.InputTokens != wantInput || usage.OutputTokens != wantOutput {
			t.Errorf("%s: tokens = %d/%d, want %d/%d", tc.name,
				usage.InputTokens, usage.OutputTokens, wantInput, wantOutput)
		}
	}
}

// TestRecordUsageSumsTwoCallsOnOneAttempt records usage twice through the real
// sink for a model priced the same all day, so both calls use one known rate and
// the expected total is exact. Recording twice is how one attempt makes several
// provider calls.
func TestRecordUsageSumsTwoCallsOnOneAttempt(t *testing.T) {
	s := newTestService(t)
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, _ Task, out io.Writer) error {
		recorder, err := usageRecorderFor(out)
		if err != nil {
			return err
		}
		if err := recorder.RecordUsage("openai/gpt-4.1", TokenUsage{InputTokens: 1000, OutputTokens: 100}); err != nil {
			return err
		}
		return recorder.RecordUsage("openai/gpt-4.1", TokenUsage{InputTokens: 2000, OutputTokens: 300})
	}))
	task := createTask(t, s, "two calls", "do work", t.TempDir())
	must(t, s.Start(context.Background(), task.ID))
	done := await(t, s, task.ID, Completed)
	if len(done.Attempts) != 1 || done.Attempts[0].Usage == nil {
		t.Fatalf("attempt usage missing: %+v", done.Attempts)
	}
	usage := done.Attempts[0].Usage
	if usage.InputTokens != 3000 || usage.OutputTokens != 400 {
		t.Fatalf("recorded tokens = %d/%d, want 3000/400", usage.InputTokens, usage.OutputTokens)
	}
	// An all-day model keeps the empty rate: two calls at the same rate are not
	// a mixed-rate attempt.
	if usage.Rate != "" {
		t.Fatalf("all-day model recorded rate %q, want the empty label", usage.Rate)
	}
	if usage.Model != "openai/gpt-4.1" || !usage.Priced {
		t.Fatalf("attempt usage = %+v", usage)
	}
	// 3000*2/1M + 400*8/1M = 0.006 + 0.0032.
	wantCost := 3000*2.0/millionTokens + 400*8.0/millionTokens
	if !floatMatches(usage.CostUSD, wantCost) {
		t.Fatalf("attempt cost = %v, want %v", usage.CostUSD, wantCost)
	}
	total := done.Usage()
	if total.InputTokens != 3000 || total.OutputTokens != 400 || !floatMatches(total.CostUSD, wantCost) {
		t.Fatalf("task usage = %+v", total)
	}
	session := s.SessionUsage()
	if session.InputTokens != 3000 || session.OutputTokens != 400 || !floatMatches(session.CostUSD, wantCost) {
		t.Fatalf("session usage = %+v", session)
	}
}

// TestRecordUsageTwiceForTimeOfDayModelKeepsOneRate records two calls for a
// model with time-of-day pricing inside one window. Both calls are charged the
// rate in force, the costs sum, and the attempt is not labelled mixed.
func TestRecordUsageTwiceForTimeOfDayModelKeepsOneRate(t *testing.T) {
	const model = "deepseek/deepseek-reasoner"
	call := TokenUsage{InputTokens: 12345, OutputTokens: 6789}
	before := time.Now().UTC()
	s := newTestService(t)
	s.RegisterRunner("general", runnerFunc(func(_ context.Context, _ Task, out io.Writer) error {
		recorder, err := usageRecorderFor(out)
		if err != nil {
			return err
		}
		if err := recorder.RecordUsage(model, call); err != nil {
			return err
		}
		return recorder.RecordUsage(model, call)
	}))
	task := createTask(t, s, "two calls", "do work", t.TempDir())
	must(t, s.Start(context.Background(), task.ID))
	done := await(t, s, task.ID, Completed)
	after := time.Now().UTC()
	price, ok := PriceFor(model, before)
	if !ok {
		t.Fatalf("%s has no curated price", model)
	}
	rate := price.Rate(before)
	if price.Rate(after) != rate {
		// The run straddled the off-peak boundary, so the two calls legitimately
		// used different prices and an exact total cannot be asserted.
		t.Skipf("run straddled the %s boundary; rate changed from %q to %q", model, rate, price.Rate(after))
	}
	usage := done.Attempts[len(done.Attempts)-1].Usage
	if usage == nil {
		t.Fatal("attempt usage missing")
	}
	if usage.Rate != rate {
		t.Fatalf("rate = %q, want %q (two calls in one window are not mixed)", usage.Rate, rate)
	}
	if usage.InputTokens != 2*call.InputTokens || usage.OutputTokens != 2*call.OutputTokens {
		t.Fatalf("tokens = %d/%d, want %d/%d", usage.InputTokens, usage.OutputTokens, 2*call.InputTokens, 2*call.OutputTokens)
	}
	wantCost := 2 * price.Cost(call, before)
	if !floatMatches(usage.CostUSD, wantCost) {
		t.Fatalf("cost = %v, want %v", usage.CostUSD, wantCost)
	}
	session := s.SessionUsage()
	if len(session.Models) != 1 || session.Models[0].CurrentRate != rate {
		t.Fatalf("session model rate = %+v, want %q", session.Models, rate)
	}
	if !floatMatches(session.CostUSD, wantCost) {
		t.Fatalf("session cost = %v, want %v", session.CostUSD, wantCost)
	}
}

// TestRecordUsageMarksAttemptUnpricedWhenOneCallIsUnpriced checks an attempt
// that used both a priced and an unpriced model in either order: every token is
// counted, only the priced call contributes money, and an attempt with any
// unpriced call is never reported as fully priced.
func TestRecordUsageMarksAttemptUnpricedWhenOneCallIsUnpriced(t *testing.T) {
	pricedCall := usageCall{model: "openai/gpt-4.1", usage: TokenUsage{InputTokens: 1000, OutputTokens: 500}}
	unpricedCall := usageCall{model: "nosuch/model", usage: TokenUsage{InputTokens: 7, OutputTokens: 3}}
	// 1000*2/1M + 500*8/1M = 0.006, and only that half is charged.
	wantCost := 1000*2.0/millionTokens + 500*8.0/millionTokens
	cases := []struct {
		name  string
		calls []usageCall
		model string
	}{
		{"priced then unpriced", []usageCall{pricedCall, unpricedCall}, "openai/gpt-4.1"},
		{"unpriced then priced", []usageCall{unpricedCall, pricedCall}, "nosuch/model"},
	}
	for _, tc := range cases {
		calls := tc.calls
		s := newTestService(t)
		s.RegisterRunner("general", runnerFunc(func(_ context.Context, _ Task, out io.Writer) error {
			recorder, err := usageRecorderFor(out)
			if err != nil {
				return err
			}
			for _, call := range calls {
				if err := recorder.RecordUsage(call.model, call.usage); err != nil {
					return err
				}
			}
			return nil
		}))
		task, err := s.CreateTask("mixed models", "do work", t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		must(t, s.Start(context.Background(), task.ID))
		done := await(t, s, task.ID, Completed)
		usage := done.Attempts[0].Usage
		if usage == nil || usage.InputTokens != 1007 || usage.OutputTokens != 503 {
			t.Fatalf("%s: attempt tokens = %+v, want every token counted", tc.name, usage)
		}
		if usage.Priced {
			t.Fatalf("%s: attempt with an unpriced call was reported as priced: %+v", tc.name, usage)
		}
		if !floatMatches(usage.CostUSD, wantCost) {
			t.Fatalf("%s: attempt cost = %v, want only the priced call %v", tc.name, usage.CostUSD, wantCost)
		}
		// One attempt record carries one model selector, so it keeps the model of
		// its first report.
		if usage.Model != tc.model {
			t.Fatalf("%s: attempt model = %q, want %q", tc.name, usage.Model, tc.model)
		}
		session := s.SessionUsage()
		if session.Priced || !floatMatches(session.CostUSD, wantCost) || session.InputTokens != 1007 {
			t.Fatalf("%s: session usage = %+v", tc.name, session)
		}
		// The attempt record cannot name the unpriced call, so the session names
		// the attempt's recorded selector; it must still admit the work is
		// partly unpriced rather than claim a complete cost.
		if len(session.Unpriced) != 1 {
			t.Fatalf("%s: unpriced selectors = %v", tc.name, session.Unpriced)
		}
	}
}

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

// TestUnreportedUsageCreatesNoRecord covers a runner that reports zero tokens
// and one that reports nothing: neither may leave an attempt that looks like it
// cost zero. A sink that cannot record must not fail the run.
func TestUnreportedUsageCreatesNoRecord(t *testing.T) {
	if err := adapterUsage(io.Discard, "openai/gpt-4.1", TokenUsage{InputTokens: 1}); err != nil {
		t.Fatalf("discarded sink failed: %v", err)
	}
	for name, runner := range map[string]Runner{
		"zero tokens": runnerFunc(func(_ context.Context, _ Task, out io.Writer) error {
			recorder, err := usageRecorderFor(out)
			if err != nil {
				return err
			}
			return recorder.RecordUsage("openai/gpt-4.1", TokenUsage{})
		}),
		"no report": runnerFunc(func(_ context.Context, _ Task, out io.Writer) error {
			_, err := io.WriteString(out, "no usage reported")
			return err
		}),
	} {
		s := newTestService(t)
		s.RegisterRunner("general", runner)
		task := createTask(t, s, "no tokens", "do work", t.TempDir())
		must(t, s.Start(context.Background(), task.ID))
		done := await(t, s, task.ID, Completed)
		if usage := done.Attempts[0].Usage; usage != nil {
			t.Fatalf("%s: created an attempt record: %+v", name, usage)
		}
		if total := done.Usage(); !total.IsZero() || total.CostUSD != 0 || total.Priced {
			t.Fatalf("%s: task usage = %+v, want none", name, total)
		}
		if usage := s.SessionUsage(); usage.TotalTokens != 0 || usage.CostUSD != 0 || usage.Tasks != 0 || len(usage.Models) != 0 || len(usage.Unpriced) != 0 {
			t.Fatalf("%s: session usage = %+v, want none", name, usage)
		}
	}
}

// TestUsageSurvivesFailedAndCanceledAttempts proves usage a provider reported
// is kept when the run then fails or is canceled: the tokens were spent, so
// they stay on the attempt, in the task total and in the session total.
func TestUsageSurvivesFailedAndCanceledAttempts(t *testing.T) {
	for _, want := range []Status{Failed, Canceled} {
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
			if want == Failed {
				return errors.New("provider exploded")
			}
			close(recorded)
			<-ctx.Done()
			return ctx.Err()
		}))
		task := createTask(t, s, "ends", "do work", t.TempDir())
		must(t, s.Start(context.Background(), task.ID))
		if want == Canceled {
			<-recorded
			must(t, s.Cancel(task.ID))
		}
		done := await(t, s, task.ID, want)
		if len(done.Attempts) != 1 || done.Attempts[0].Status != want {
			t.Fatalf("%s: attempts = %+v", want, done.Attempts)
		}
		usage := done.Attempts[0].Usage
		if usage == nil || usage.InputTokens != 1000 || usage.OutputTokens != 500 || !usage.Priced {
			t.Fatalf("%s: attempt lost its usage: %+v", want, usage)
		}
		wantCost := 1000*2.0/millionTokens + 500*8.0/millionTokens
		if !floatMatches(usage.CostUSD, wantCost) {
			t.Fatalf("%s: attempt cost = %v, want %v", want, usage.CostUSD, wantCost)
		}
		if total := done.Usage(); total.InputTokens != 1000 || total.OutputTokens != 500 || !floatMatches(total.CostUSD, wantCost) {
			t.Fatalf("%s: task usage = %+v", want, total)
		}
		session := s.SessionUsage()
		if session.InputTokens != 1000 || session.OutputTokens != 500 || !floatMatches(session.CostUSD, wantCost) || !session.Priced || session.Tasks != 1 {
			t.Fatalf("%s: session usage = %+v", want, session)
		}
	}
}

func TestRecordUsageCountsCacheTokensAndCost(t *testing.T) {
	s := newTestService(t)
	call := TokenUsage{
		InputTokens: 1000, OutputTokens: 500,
		CacheReadTokens: 2000, CacheWriteTokens: 300,
	}
	s.RegisterRunner("general", sessionUsageRunner("openai/gpt-4.1", call))
	task := createTask(t, s, "cache", "do work", t.TempDir())
	must(t, s.Start(context.Background(), task.ID))
	done := await(t, s, task.ID, Completed)

	attempt := done.Attempts[0].Usage
	if attempt == nil || attempt.InputTokens != 1000 || attempt.OutputTokens != 500 ||
		attempt.CacheReadTokens != 2000 || attempt.CacheWriteTokens != 300 {
		t.Fatalf("attempt cache usage = %+v", attempt)
	}
	price, ok := PriceFor("openai/gpt-4.1", time.Now())
	if !ok {
		t.Fatal("gpt-4.1 price missing")
	}
	want := price.CostBreakdown(call, time.Now())
	if !floatMatches(attempt.CostUSD, want.Total) ||
		!floatMatches(attempt.InputCostUSD, want.Input) ||
		!floatMatches(attempt.OutputCostUSD, want.Output) ||
		!floatMatches(attempt.CacheReadCostUSD, want.CacheRead) ||
		!floatMatches(attempt.CacheWriteCostUSD, want.CacheWrite) {
		t.Fatalf("attempt costs = %+v, want %+v", attempt, want)
	}

	total := done.Usage()
	if total.PromptTokens() != 3300 || total.TotalTokens() != 3800 ||
		total.CacheReadTokens != 2000 || total.CacheWriteTokens != 300 {
		t.Fatalf("task usage = %+v, want prompt 3300 and total 3800", total)
	}

	session := s.SessionUsage()
	if session.PromptTokens != 3300 || session.TotalTokens != 3800 ||
		session.CacheReadTokens != 2000 || session.CacheWriteTokens != 300 {
		t.Fatalf("session tokens = %+v", session)
	}
	if wantRate := 2000.0 / 3300.0; !floatMatches(session.CacheHitRate, wantRate) {
		t.Fatalf("cache hit rate = %v, want %v", session.CacheHitRate, wantRate)
	}
	if len(session.Models) != 1 || session.Models[0].CacheReadTokens != 2000 ||
		session.Models[0].CacheWriteTokens != 300 {
		t.Fatalf("session model cache usage = %+v", session.Models)
	}
}
