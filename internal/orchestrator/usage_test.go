package orchestrator

import (
	"bytes"
	"context"
	"io"
	"math"
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

func TestRecordUsageReportsDeepSeekRate(t *testing.T) {
	s := newTestService(t)
	s.RegisterRunner("general", sessionUsageRunner("deepseek/deepseek-chat", TokenUsage{InputTokens: millionTokens, OutputTokens: millionTokens}))
	task := createTask(t, s, "t", "do work", t.TempDir())
	startedAt := time.Now()
	must(t, s.Start(context.Background(), task.ID))
	done := await(t, s, task.ID, Completed)
	price, _ := PriceFor("deepseek/deepseek-chat", startedAt)
	wantRate := price.Rate(startedAt)
	attempt := done.Attempts[len(done.Attempts)-1]
	if attempt.Usage == nil || attempt.Usage.Rate != wantRate {
		t.Fatalf("recorded rate = %+v, want %q", attempt.Usage, wantRate)
	}
	// Allow for a recording that straddles the window boundary.
	wantCost := price.Cost(TokenUsage{InputTokens: millionTokens, OutputTokens: millionTokens}, startedAt)
	if math.Abs(attempt.Usage.CostUSD-wantCost) > 0.3 {
		t.Fatalf("cost = %v, want about %v", attempt.Usage.CostUSD, wantCost)
	}
	usage := s.SessionUsage()
	if len(usage.Models) != 1 || usage.Models[0].CurrentRate != wantRate {
		t.Fatalf("session model rate = %+v", usage.Models)
	}
}

func TestFantasyRunnerRecordsUsagePerStreamCall(t *testing.T) {
	finish := func(input, output int64) fantasy.StreamPart {
		return fantasy.StreamPart{
			Type:         fantasy.StreamPartTypeFinish,
			FinishReason: fantasy.FinishReasonStop,
			Usage:        fantasy.Usage{InputTokens: input, OutputTokens: output, TotalTokens: input + output},
		}
	}
	step := func(text string, input, output int64) []fantasy.StreamPart {
		return []fantasy.StreamPart{
			{Type: fantasy.StreamPartTypeTextStart, ID: "text-1"},
			{Type: fantasy.StreamPartTypeTextDelta, ID: "text-1", Delta: text},
			{Type: fantasy.StreamPartTypeTextEnd, ID: "text-1"},
			finish(input, output),
		}
	}
	model := &fakeFantasyModel{scripts: [][]fantasy.StreamPart{
		step("first", 100, 40),
		step("second", 50, 20),
	}}
	runner := FantasyRunner{model: "deepseek/deepseek-chat"}
	runner.ModelFactory = fakeFactory(model, "deepseek/deepseek-chat")

	sink := &usageSink{}
	follow := make(chan Trigger, 1)
	follow <- Trigger{ID: "ctx-1", Text: "more"}
	close(follow)
	if err := runner.RunLive(context.Background(), Task{Prompt: "do work", Workspace: t.TempDir()}, sink, follow,
		func(string, bool, string) {}); err != nil {
		t.Fatal(err)
	}
	if len(sink.calls) != 2 {
		t.Fatalf("usage calls = %+v", sink.calls)
	}
	for i, want := range []TokenUsage{{100, 40}, {50, 20}} {
		if sink.calls[i].model != "deepseek/deepseek-chat" || sink.calls[i].usage != want {
			t.Fatalf("call %d = %+v, want %+v", i, sink.calls[i], want)
		}
	}
}

func TestFantasyRunnerSkipsEmptyUsage(t *testing.T) {
	model := &fakeFantasyModel{scripts: [][]fantasy.StreamPart{textDelta("answer")}}
	runner := FantasyRunner{model: "fake/model"}
	runner.ModelFactory = fakeFactory(model, "fake/model")
	sink := &usageSink{}
	must(t, runner.Run(context.Background(), Task{Prompt: "do work", Workspace: t.TempDir()}, sink))
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

func TestTaskUsageIgnoresUnrecordedAttempts(t *testing.T) {
	task := Task{Attempts: []Attempt{{ID: "a"}, {ID: "b"}}}
	if total := task.Usage(); !total.IsZero() || total.Priced {
		t.Fatalf("attempts without usage reported a total: %+v", total)
	}
	task.Attempts[1].Usage = &AttemptUsage{InputTokens: 7, OutputTokens: 3, Priced: true, CostUSD: 0.5, Rate: RateOffPeak}
	task.Attempts[1].RequestedModel = "deepseek/deepseek-chat"
	total := task.Usage()
	if total.InputTokens != 7 || total.OutputTokens != 3 || total.CostUSD != 0.5 || !total.Priced {
		t.Fatalf("task usage = %+v", total)
	}
	if total.Model != "deepseek/deepseek-chat" || total.Rate != RateOffPeak {
		t.Fatalf("task usage model/rate = %q/%q", total.Model, total.Rate)
	}
}
