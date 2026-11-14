package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"charm.land/fantasy"
)

// fakeFantasyModel replays scripted stream parts, one script per Stream call,
// so the adapter can be tested without credentials or a network connection.
type fakeFantasyModel struct {
	mu      sync.Mutex
	scripts [][]fantasy.StreamPart
	calls   int
}

func (m *fakeFantasyModel) Generate(context.Context, fantasy.Call) (*fantasy.Response, error) {
	return &fantasy.Response{
		Content:      fantasy.ResponseContent{fantasy.TextContent{Text: "ok"}},
		FinishReason: fantasy.FinishReasonStop,
	}, nil
}

func (m *fakeFantasyModel) Stream(context.Context, fantasy.Call) (fantasy.StreamResponse, error) {
	m.mu.Lock()
	var parts []fantasy.StreamPart
	if m.calls < len(m.scripts) {
		parts = m.scripts[m.calls]
	}
	m.calls++
	m.mu.Unlock()
	return func(yield func(fantasy.StreamPart) bool) {
		for _, part := range parts {
			if !yield(part) {
				return
			}
		}
	}, nil
}

func (m *fakeFantasyModel) GenerateObject(context.Context, fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	return nil, errors.New("fake model has no object support")
}

func (m *fakeFantasyModel) StreamObject(context.Context, fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
	return nil, errors.New("fake model has no object support")
}

func (m *fakeFantasyModel) Provider() string { return "fake" }
func (m *fakeFantasyModel) Model() string    { return "fake/model" }

// fantasyTestSink records adapter reports and tool activity while keeping the
// agent's prose, so a test can assert on both.
type fantasyTestSink struct {
	bytes.Buffer
	reports    []string
	activities []ToolActivity
}

func (s *fantasyTestSink) RecordReport(kind, message string) error {
	s.reports = append(s.reports, kind+": "+message)
	return nil
}

func (s *fantasyTestSink) RecordToolActivity(activity ToolActivity) error {
	s.activities = append(s.activities, activity)
	return nil
}

func textDelta(text string) []fantasy.StreamPart {
	return []fantasy.StreamPart{
		{Type: fantasy.StreamPartTypeTextStart, ID: "text-1"},
		{Type: fantasy.StreamPartTypeTextDelta, ID: "text-1", Delta: text},
		{Type: fantasy.StreamPartTypeTextEnd, ID: "text-1"},
		{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonStop},
	}
}

func fakeFactory(model fantasy.LanguageModel, wantModel string) ModelFactory {
	return func(_ context.Context, modelID string) (fantasy.LanguageModel, error) {
		if wantModel != "" && modelID != wantModel {
			return nil, errors.New("unexpected model " + modelID)
		}
		return model, nil
	}
}

func TestFantasyRunnerStreamsAssistantText(t *testing.T) {
	model := &fakeFantasyModel{scripts: [][]fantasy.StreamPart{textDelta("Working on it")}}
	runner := FantasyRunner{}
	runner.ModelFactory = fakeFactory(model, "fake/model")

	sink := &fantasyTestSink{}
	must(t, runner.Run(context.Background(), Task{Prompt: "do work", model: "fake/model", Workspace: t.TempDir()}, sink))
	if !strings.Contains(sink.String(), "Working on it") {
		t.Fatalf("assistant text missing from output: %q", sink.String())
	}
	if len(sink.reports) == 0 || !strings.Contains(sink.reports[0], "Fantasy accepted the initial prompt") {
		t.Fatalf("progress report missing: %v", sink.reports)
	}
}

func TestFantasyRunnerLiveFollowUp(t *testing.T) {
	model := &fakeFantasyModel{scripts: [][]fantasy.StreamPart{
		textDelta("first answer"),
		textDelta("second answer"),
	}}
	runner := FantasyRunner{}
	runner.ModelFactory = fakeFactory(model, "fake/model")

	sink := &fantasyTestSink{}
	follow := make(chan Trigger, 1)
	follow <- Trigger{ID: "ctx-1", Text: "more context"}
	close(follow)
	var acked []string
	err := runner.RunLive(context.Background(), Task{Prompt: "do work", model: "fake/model", Workspace: t.TempDir()}, sink, follow,
		func(id string, _ bool, reason string) { acked = append(acked, id+":"+reason) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sink.String(), "first answer") || !strings.Contains(sink.String(), "second answer") {
		t.Fatalf("follow-up output missing: %q", sink.String())
	}
	if len(acked) != 1 || acked[0] != "ctx-1:accepted" {
		t.Fatalf("unexpected acknowledgements: %v", acked)
	}
}

func TestFantasyRunnerRecordsToolActivity(t *testing.T) {
	workspace := t.TempDir()
	must(t, os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("workspace contents"), 0o644))
	model := &fakeFantasyModel{scripts: [][]fantasy.StreamPart{
		{
			{Type: fantasy.StreamPartTypeToolCall, ID: "call-1", ToolCallName: "read", ToolCallInput: `{"path":"note.txt"}`},
			{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonToolCalls},
		},
		textDelta("read the file"),
	}}
	runner := FantasyRunner{}
	runner.ModelFactory = fakeFactory(model, "fake/model")

	sink := &fantasyTestSink{}
	must(t, runner.Run(context.Background(), Task{Prompt: "do work", model: "fake/model", Workspace: workspace}, sink))
	var running, finished *ToolActivity
	for i := range sink.activities {
		switch sink.activities[i].Status {
		case ActivityRunning:
			running = &sink.activities[i]
		case ActivityFinished:
			finished = &sink.activities[i]
		}
	}
	if running == nil || finished == nil {
		t.Fatalf("expected running and finished activity, got %+v", sink.activities)
	}
	if running.ID != "call-1" || running.Name != "read" || !json.Valid(running.Args) {
		t.Fatalf("unexpected running activity: %+v", running)
	}
	if !strings.Contains(string(finished.Result), "workspace contents") {
		t.Fatalf("tool result not recorded: %s", finished.Result)
	}
	if finished.DurationMS == nil {
		t.Fatalf("duration not recorded: %+v", finished)
	}
	output := sink.String()
	if !strings.Contains(output, "[running] read") || !strings.Contains(output, "[finished] read") {
		t.Fatalf("tool markers missing from output: %q", output)
	}
	if !strings.Contains(output, "read the file") {
		t.Fatalf("assistant text after the tool call missing: %q", output)
	}
}

func TestFantasyRunnerFailures(t *testing.T) {
	script := func(reason fantasy.FinishReason) ModelFactory {
		return fakeFactory(&fakeFantasyModel{scripts: [][]fantasy.StreamPart{{{Type: fantasy.StreamPartTypeFinish, FinishReason: reason}}}}, "")
	}
	for _, tc := range []struct {
		name, want, model string
		runner            FantasyRunner
		reported          bool
	}{
		{"failure finish reason", "finish reason", "fake/model", FantasyRunner{ModelFactory: script(fantasy.FinishReasonError)}, true},
		{"no assistant text", "no assistant response", "fake/model", FantasyRunner{ModelFactory: script(fantasy.FinishReasonStop)}, true},
		{"empty model", "provider/model", "", FantasyRunner{}, false},
		{"factory error", "no credentials", "fake/model", FantasyRunner{ModelFactory: func(context.Context, string) (fantasy.LanguageModel, error) {
			return nil, errors.New("no credentials")
		}}, false},
	} {
		sink := &fantasyTestSink{}
		err := tc.runner.Run(context.Background(), Task{Prompt: "do work", model: tc.model, Workspace: t.TempDir()}, sink)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: got %v, want %q", tc.name, err, tc.want)
		}
		if tc.reported && (len(sink.reports) == 0 || !strings.HasPrefix(sink.reports[len(sink.reports)-1], "failed: ")) {
			t.Fatalf("%s: failed report missing: %v", tc.name, sink.reports)
		}
	}
}

func TestFantasyRunnerSupportsProfile(t *testing.T) {
	runner := FantasyRunner{}
	if err := runner.SupportsProfile(Profile{Name: "tester", Tools: []string{"read", "bash"}, Model: "openai/gpt-4.1"}); err != nil {
		t.Fatalf("ordinary profile rejected: %v", err)
	}
	if err := runner.SupportsProfile(Profile{Name: "tester", Tools: []string{strings.Repeat("t", 129)}}); err == nil {
		t.Fatal("over-long tool name accepted")
	}
	if err := runner.SupportsProfile(Profile{Name: "tester", Model: strings.Repeat("m", 201)}); err == nil {
		t.Fatal("over-long model selector accepted")
	}
}

func TestFantasyToolCallSummaryIsBounded(t *testing.T) {
	long := strings.Repeat("x", 500)
	got := toolCallSummary("read", "\x1b[2J"+`{"path":"`+long+`"}`)
	if strings.Contains(got, "\x1b") {
		t.Fatalf("control bytes not sanitized: %q", got)
	}
	if len(got) > 300 || !strings.HasPrefix(got, "read: ") {
		t.Fatalf("summary not bounded: %q", got)
	}
	if got := toolCallSummary("bash", ""); got != "bash" {
		t.Fatalf("empty input summary: %q", got)
	}
}
