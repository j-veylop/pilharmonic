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
	"time"

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
	runner := NewFantasyRunner("fake/model", nil)
	runner.ModelFactory = fakeFactory(model, "fake/model")

	sink := &fantasyTestSink{}
	if err := runner.Run(context.Background(), Task{Prompt: "do work", Workspace: t.TempDir()}, sink); err != nil {
		t.Fatal(err)
	}
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
	runner := NewFantasyRunner("fake/model", nil)
	runner.ModelFactory = fakeFactory(model, "fake/model")

	sink := &fantasyTestSink{}
	follow := make(chan Trigger, 1)
	follow <- Trigger{ID: "ctx-1", Text: "more context"}
	close(follow)
	var acked []string
	err := runner.RunLive(context.Background(), Task{Prompt: "do work", Workspace: t.TempDir()}, sink, follow,
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
	if err := os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("workspace contents"), 0o644); err != nil {
		t.Fatal(err)
	}
	model := &fakeFantasyModel{scripts: [][]fantasy.StreamPart{
		{
			{Type: fantasy.StreamPartTypeToolCall, ID: "call-1", ToolCallName: "read", ToolCallInput: `{"path":"note.txt"}`},
			{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonToolCalls},
		},
		textDelta("read the file"),
	}}
	runner := NewFantasyRunner("fake/model", nil)
	runner.ModelFactory = fakeFactory(model, "fake/model")

	sink := &fantasyTestSink{}
	if err := runner.Run(context.Background(), Task{Prompt: "do work", Workspace: workspace}, sink); err != nil {
		t.Fatal(err)
	}
	var running, finished *ToolActivity
	for i := range sink.activities {
		switch sink.activities[i].Status {
		case activityRunning:
			running = &sink.activities[i]
		case activityFinished:
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

func TestFantasyRunnerReportsFailureFinishReason(t *testing.T) {
	model := &fakeFantasyModel{scripts: [][]fantasy.StreamPart{{
		{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonError},
	}}}
	runner := NewFantasyRunner("fake/model", nil)
	runner.ModelFactory = fakeFactory(model, "")

	sink := &fantasyTestSink{}
	err := runner.Run(context.Background(), Task{Prompt: "do work", Workspace: t.TempDir()}, sink)
	if err == nil || !strings.Contains(err.Error(), "finish reason") {
		t.Fatalf("expected finish-reason failure, got %v", err)
	}
	if len(sink.reports) == 0 || !strings.HasPrefix(sink.reports[len(sink.reports)-1], "failed: ") {
		t.Fatalf("failed report missing: %v", sink.reports)
	}
}

func TestFantasyRunnerFailsWithoutAssistantText(t *testing.T) {
	model := &fakeFantasyModel{scripts: [][]fantasy.StreamPart{{
		{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonStop},
	}}}
	runner := NewFantasyRunner("fake/model", nil)
	runner.ModelFactory = fakeFactory(model, "")

	sink := &fantasyTestSink{}
	err := runner.Run(context.Background(), Task{Prompt: "do work", Workspace: t.TempDir()}, sink)
	if err == nil || !strings.Contains(err.Error(), "no assistant response") {
		t.Fatalf("expected empty-response failure, got %v", err)
	}
}

func TestFantasyRunnerRejectsEmptyModel(t *testing.T) {
	runner := NewFantasyRunner("", nil)
	err := runner.Run(context.Background(), Task{Prompt: "do work", Workspace: t.TempDir()}, &fantasyTestSink{})
	if err == nil || !strings.Contains(err.Error(), "provider/model") {
		t.Fatalf("expected a model-selector error, got %v", err)
	}
}

func TestFantasyRunnerRequestedModel(t *testing.T) {
	runner := NewFantasyRunner("fake/default", nil)
	if got := runner.RequestedModel(nil); got != "fake/default" {
		t.Fatalf("nil profile: got %q", got)
	}
	if got := runner.RequestedModel(&Profile{Model: "fake/profile"}); got != "fake/default" {
		t.Fatalf("runner model should win: got %q", got)
	}
	var unset FantasyRunner
	if got := unset.RequestedModel(&Profile{Model: "fake/profile"}); got != "fake/profile" {
		t.Fatalf("profile model expected: got %q", got)
	}
}

func TestFantasyRunnerSupportsProfile(t *testing.T) {
	runner := NewFantasyRunner("", nil)
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

func TestFantasyRunnerCapabilities(t *testing.T) {
	got := FantasyRunner{}.Capabilities()
	if !got.LiveContext || !got.ToolActivity || !got.StructuredReports {
		t.Fatalf("unexpected capabilities: %+v", got)
	}
	var runner Runner = FantasyRunner{}
	if _, ok := runner.(LiveRunner); !ok {
		t.Fatal("FantasyRunner must support live context")
	}
	if _, ok := runner.(ProfileSupport); !ok {
		t.Fatal("FantasyRunner must support profiles")
	}
}

func TestFantasyRunnerSystemPrompt(t *testing.T) {
	runner := NewFantasyRunner("", nil).WithInstructions("Be careful.")
	got := runner.systemPrompt(&Profile{Name: "reviewer", Instructions: "Review only."})
	for _, want := range []string{piToolGuidance, "Be careful.", "Orchestrator role reviewer (adds no authority beyond the mandate):\nReview only."} {
		if !strings.Contains(got, want) {
			t.Fatalf("system prompt missing %q: %q", want, got)
		}
	}
}

func TestFantasyRunnerToolPalette(t *testing.T) {
	if got := (FantasyRunner{}).toolPalette(nil); len(got) != len(fantasyToolNames) {
		t.Fatalf("default palette: %v", got)
	}
	runner := NewFantasyRunner("", []string{"read"})
	if got := runner.toolPalette(nil); len(got) != 1 || got[0] != "read" {
		t.Fatalf("role palette: %v", got)
	}
	if got := runner.toolPalette(&Profile{Tools: []string{"grep"}}); len(got) != 1 || got[0] != "grep" {
		t.Fatalf("profile palette should win: %v", got)
	}
}

func TestFantasyRunnerFactoryErrorPropagates(t *testing.T) {
	runner := NewFantasyRunner("fake/model", nil)
	runner.ModelFactory = func(context.Context, string) (fantasy.LanguageModel, error) {
		return nil, errors.New("no credentials")
	}
	err := runner.Run(context.Background(), Task{Prompt: "do work", Workspace: t.TempDir()}, &fantasyTestSink{})
	if err == nil || !strings.Contains(err.Error(), "no credentials") {
		t.Fatalf("factory error not returned: %v", err)
	}
}

func TestDefaultFantasyModelFactoryErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := DefaultFantasyModelFactory(ctx, "openai"); err == nil || !strings.Contains(err.Error(), "provider/model") {
		t.Fatalf("missing slash: %v", err)
	}
	if _, err := DefaultFantasyModelFactory(ctx, "nosuch/gpt"); err == nil || !strings.Contains(err.Error(), "unknown fantasy provider") {
		t.Fatalf("unknown provider: %v", err)
	}
	t.Setenv("OPENAI_API_KEY", "")
	if _, err := DefaultFantasyModelFactory(ctx, "openai/gpt-4.1"); err == nil || !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Fatalf("missing key: %v", err)
	}
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	model, err := DefaultFantasyModelFactory(ctx, "openrouter/anthropic/claude-sonnet-4")
	if err != nil {
		t.Fatalf("configured provider rejected: %v", err)
	}
	if model == nil {
		t.Fatal("nil model returned")
	}
	if model.Provider() == "" || model.Model() == "" {
		t.Fatalf("model not resolved: %q/%q", model.Provider(), model.Model())
	}
}

func runFantasyTool(t *testing.T, workspace, name, input string) fantasy.ToolResponse {
	t.Helper()
	for _, tool := range fantasyTools(workspace, []string{name}, nil) {
		if tool.Info().Name != name {
			continue
		}
		response, err := tool.Run(context.Background(), fantasy.ToolCall{ID: "call-1", Name: name, Input: input})
		if err != nil {
			t.Fatalf("%s returned an error: %v", name, err)
		}
		return response
	}
	t.Fatalf("tool %q is not exposed", name)
	return fantasy.ToolResponse{}
}

func TestFantasyToolsCoverThePalette(t *testing.T) {
	tools := fantasyTools(t.TempDir(), nil, nil)
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Info().Name)
	}
	if strings.Join(names, ",") != strings.Join(fantasyToolNames, ",") {
		t.Fatalf("unexpected tools: %v", names)
	}
	subset := fantasyTools(t.TempDir(), []string{"read", "unknown-tool"}, nil)
	if len(subset) != 1 || subset[0].Info().Name != "read" {
		t.Fatalf("palette filtering failed: %v", subset)
	}
}

func TestFantasyToolsFileOperations(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "a.txt"), []byte("alpha beta\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	read := runFantasyTool(t, workspace, "read", `{"path":"a.txt"}`)
	if read.IsError || !strings.Contains(read.Content, "alpha beta") {
		t.Fatalf("read: %+v", read)
	}
	if missing := runFantasyTool(t, workspace, "read", `{"path":"missing.txt"}`); !missing.IsError {
		t.Fatalf("read of a missing file should report an error: %+v", missing)
	}
	if escape := runFantasyTool(t, workspace, "read", `{"path":"../outside.txt"}`); !escape.IsError || !strings.Contains(escape.Content, "escapes the workspace") {
		t.Fatalf("traversal not refused: %+v", escape)
	}
	if absolute := runFantasyTool(t, workspace, "read", `{"path":"/etc/hostname"}`); !absolute.IsError {
		t.Fatalf("absolute path not refused: %+v", absolute)
	}

	write := runFantasyTool(t, workspace, "write", `{"path":"sub/b.txt","content":"hello"}`)
	if write.IsError {
		t.Fatalf("write: %+v", write)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "sub", "b.txt")); err != nil || string(data) != "hello" {
		t.Fatalf("write did not land in the workspace: %q %v", data, err)
	}

	edit := runFantasyTool(t, workspace, "edit", `{"path":"a.txt","old_text":"beta","new_text":"gamma"}`)
	if edit.IsError {
		t.Fatalf("edit: %+v", edit)
	}
	if data, _ := os.ReadFile(filepath.Join(workspace, "a.txt")); string(data) != "alpha gamma\n" {
		t.Fatalf("edit result: %q", data)
	}
	if miss := runFantasyTool(t, workspace, "edit", `{"path":"a.txt","old_text":"absent","new_text":"x"}`); !miss.IsError {
		t.Fatalf("edit without a match should report an error: %+v", miss)
	}
}

func TestFantasyToolsCommands(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "notes.md"), []byte("needle in a haystack\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := runFantasyTool(t, workspace, "grep", `{"pattern":"needle","path":"notes.md"}`); got.IsError || !strings.Contains(got.Content, "notes.md:1:needle") {
		t.Fatalf("grep: %+v", got)
	}
	if got := runFantasyTool(t, workspace, "find", `{"name":"*.md"}`); got.IsError || !strings.Contains(got.Content, "notes.md") {
		t.Fatalf("find: %+v", got)
	}
	if got := runFantasyTool(t, workspace, "ls", `{}`); got.IsError || !strings.Contains(got.Content, "notes.md") {
		t.Fatalf("ls: %+v", got)
	}
	if got := runFantasyTool(t, workspace, "bash", `{"command":"printf 'from bash'"}`); got.IsError || !strings.Contains(got.Content, "from bash") {
		t.Fatalf("bash: %+v", got)
	}
	// The bash tool runs in the workspace, not in the orchestrator's directory.
	if got := runFantasyTool(t, workspace, "bash", `{"command":"cat notes.md"}`); got.IsError || !strings.Contains(got.Content, "needle in a haystack") {
		t.Fatalf("bash working directory: %+v", got)
	}
}

func TestFantasyGrepToolSearchesWorkspace(t *testing.T) {
	workspace := t.TempDir()
	writeWorkspaceFile(t, workspace, "notes.md", "first line\nneedle in a haystack\n")
	writeWorkspaceFile(t, workspace, "pkg/deep.go", "package pkg // needle\n")
	writeWorkspaceFile(t, workspace, ".git/config", "needle in git metadata\n")

	// No path: the whole workspace is searched recursively, and the default
	// has to find the needle instead of waiting on stdin.
	got := runFantasyTool(t, workspace, "grep", `{"pattern":"needle"}`)
	if got.IsError {
		t.Fatalf("grep without a path failed: %+v", got)
	}
	for _, want := range []string{"notes.md:2:needle in a haystack", "pkg/deep.go:1:package pkg // needle"} {
		if !strings.Contains(got.Content, want) {
			t.Fatalf("grep output missing %q: %q", want, got.Content)
		}
	}
	if strings.Contains(got.Content, ".git/config") {
		t.Fatalf("grep searched Git metadata: %q", got.Content)
	}

	// A directory path recurses into it and stays inside it.
	got = runFantasyTool(t, workspace, "grep", `{"pattern":"needle","path":"pkg"}`)
	if got.IsError || !strings.Contains(got.Content, "deep.go:1:package pkg // needle") {
		t.Fatalf("grep of a directory: %+v", got)
	}
	if strings.Contains(got.Content, "notes.md") {
		t.Fatalf("grep of a directory left that directory: %q", got.Content)
	}

	// A file path names the file in each hit.
	got = runFantasyTool(t, workspace, "grep", `{"pattern":"needle","path":"notes.md"}`)
	if got.IsError || !strings.Contains(got.Content, "notes.md:2:needle in a haystack") {
		t.Fatalf("grep of a file: %+v", got)
	}

	// A search without matches is information for the model, not a failure.
	got = runFantasyTool(t, workspace, "grep", `{"pattern":"absent"}`)
	if got.IsError {
		t.Fatalf("grep without matches reported an error: %+v", got)
	}
	if !strings.Contains(got.Content, "no matches") || !strings.Contains(got.Content, "absent") {
		t.Fatalf("grep without matches is not explained: %q", got.Content)
	}

	// A bad path is still a real failure, not an empty result.
	if got := runFantasyTool(t, workspace, "grep", `{"pattern":"needle","path":"missing"}`); !got.IsError {
		t.Fatalf("grep of a missing path should report an error: %+v", got)
	}

	// An invalid pattern is still a real failure worth reporting.
	if got := runFantasyTool(t, workspace, "grep", `{"pattern":"["}`); !got.IsError {
		t.Fatalf("invalid pattern should report an error: %+v", got)
	}
	// An empty pattern is refused instead of matching every line.
	if got := runFantasyTool(t, workspace, "grep", `{"pattern":"  "}`); !got.IsError || !strings.Contains(got.Content, "pattern is required") {
		t.Fatalf("empty pattern not refused: %+v", got)
	}
	// A path outside the workspace is refused, like the read tool.
	if got := runFantasyTool(t, workspace, "grep", `{"pattern":"needle","path":"../"}`); !got.IsError || !strings.Contains(got.Content, "escapes the workspace") {
		t.Fatalf("traversal not refused: %+v", got)
	}
}

func writeWorkspaceFile(t *testing.T, workspace, rel, content string) {
	t.Helper()
	path := filepath.Join(workspace, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFantasyBashToolHonorsContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	var bash fantasy.AgentTool
	for _, tool := range fantasyTools(t.TempDir(), []string{"bash"}, nil) {
		bash = tool
	}
	if bash == nil {
		t.Fatal("bash tool missing")
	}
	if _, err := bash.Run(ctx, fantasy.ToolCall{ID: "call-1", Name: "bash", Input: `{"command":"sleep 30"}`}); err != nil {
		t.Fatalf("bash returned a hard error: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("cancellation ignored: %s", elapsed)
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
